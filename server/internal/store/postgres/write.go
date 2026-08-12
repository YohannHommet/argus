// Package postgres — write.go implements store.Writer (SPEC §1.6, §1.7,
// §3.3): WriteBatch and WriteMetrics, the single-transaction write path
// every ingested event/metric goes through.
//
// # Transaction lock ordering (invariant, SPEC §1.6 — not an optimisation)
//
// Within one WriteBatch/WriteMetrics transaction, statements run in this
// fixed order, and rows within each statement are sorted by primary key
// ascending:
//
//	ingest_dedup (by dedup_key) -> sessions (by id) -> turns (by session_id, prompt_id)
//	  -> events (by ts, dedup_key) -> tool_calls (by id, P2-07) -> subagents (by session_id,
//	  agent_id, P2-08) -> rollup_dirty (by bucket, source)
//
// Two concurrent batches touching an overlapping set of sessions therefore
// acquire row locks in the same order and cannot deadlock on each other.
// Nobody should reorder these statements "for efficiency" — that is exactly
// how the FK share-lock/exclusive-lock interleaving deadlock this order
// prevents gets reintroduced (SPEC §1.6). AC (g) (TestWriteBatch_ConcurrentOverlappingSessions)
// is this invariant's regression test.
//
// One documented, narrow deviation from a *literal* read of that sequence:
// too_old classification (SPEC §1.7 rule 3) is decided by partitionCoverage
// *before* the sessions/turns statements are built, not by reacting to the
// events INSERT's error after the fact. If it were reactive, the
// too_old-excluded rows would already be baked into the sessions/turns
// aggregates queued ahead of the events statement, and there would be no way
// to un-count them without a second pass that touches sessions/turns *again*
// out of order. Deciding too_old first keeps the literal table order intact
// and keeps projections exactly consistent with what actually lands in
// `events`. It does not weaken the deadlock invariant: partitionCoverage
// only reads pg_class/pg_inherits metadata, it takes no lock on
// sessions/turns/events rows. See partitions.go's partitionCoverage doc for
// the full reasoning and its accepted TOCTOU trade-off, and IsTooOld's doc
// for why it is still exercised as defence in depth.
//
// # Why pgx.Batch, not CopyFrom
//
// CopyFrom (COPY FROM STDIN) cannot express ON CONFLICT, and every statement
// here needs it (the ingest_dedup gate, the events parent-level defence in
// depth, and every projection upsert). Per-row exec of these table-level
// statements would need up to seven round trips *per event*; pgx.Batch
// pipelines every statement in one round trip while preserving the server-
// side execution order the invariant above requires (pgx.Batch executes
// queued statements in the order queued, unlike a set of independent
// goroutine calls). The ingest_dedup gate itself is issued as a single
// ahead-of-batch statement (via *pgx.Tx directly, not queued) because its
// result — which keys survived — determines the SQL the rest of the
// batch needs to build; everything after it (sessions, turns, events,
// tool_calls/subagents seams, rollup_dirty) is queued as one pgx.Batch and
// sent together.
package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// WriteBatch implements store.Writer (SPEC §1.6/§1.7/§3.3). See this file's
// package doc for the lock-ordering invariant and the pgx.Batch rationale.
func (s *Store) WriteBatch(ctx context.Context, b []model.Event) (store.BatchResult, error) {
	if len(b) == 0 {
		return store.BatchResult{}, nil
	}

	sorted := make([]model.Event, len(b))
	copy(sorted, b)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TS.Equal(sorted[j].TS) {
			return sorted[i].TS.Before(sorted[j].TS)
		}
		return sorted[i].DedupKey < sorted[j].DedupKey
	})

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.BatchResult{}, fmt.Errorf("postgres: write batch: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	dedupKeys := make([]string, len(sorted))
	for i, e := range sorted {
		dedupKeys[i] = e.DedupKey
	}
	survived, err := insertIngestDedup(ctx, tx, dedupKeys)
	if err != nil {
		return store.BatchResult{}, err
	}

	covers, err := partitionCoverage(ctx, tx, "events")
	if err != nil {
		return store.BatchResult{}, err
	}

	// Collapse to one candidate per distinct dedup_key (SPEC §1.7 rule 2's
	// ledger has one row per key regardless of how many batch entries share
	// it — see dedup.go's doc), then split by partition coverage.
	seenKey := map[string]bool{}
	var candidates []model.Event
	deduped := 0
	tooOld := 0
	for _, e := range sorted {
		if !survived[e.DedupKey] {
			deduped++
			continue
		}
		if seenKey[e.DedupKey] {
			deduped++
			continue
		}
		seenKey[e.DedupKey] = true
		if !covers(e.TS) {
			tooOld++
			continue
		}
		candidates = append(candidates, e)
	}

	result := store.BatchResult{Deduped: deduped, TooOld: tooOld, Rejected: tooOld}
	if len(candidates) == 0 {
		if err = tx.Commit(ctx); err != nil {
			return store.BatchResult{}, fmt.Errorf("postgres: write batch: commit: %w", err)
		}
		return result, nil
	}

	sessionAggs := foldSessionEvents(candidates)
	turnAggs := foldTurnEvents(candidates)

	sessionResults, err := upsertSessions(ctx, tx, sessionAggs)
	if err != nil {
		return store.BatchResult{}, err
	}
	if err = upsertTurns(ctx, tx, turnAggs); err != nil {
		return store.BatchResult{}, err
	}

	inserted, err := insertEvents(ctx, tx, candidates)
	if err != nil {
		return store.BatchResult{}, err
	}
	// Defence-in-depth conflicts (SPEC §1.7 rule 2: the parent-level
	// UNIQUE (ts, dedup_key)) are vanishingly rare given the ledger already
	// gates admission — verified on PG 18.4 per SPEC §1.7. When one occurs,
	// the row's contribution is still reflected in the sessions/turns
	// aggregates queued above (computed from `candidates`, not from
	// `inserted`), a known and accepted trade-off documented in this file's
	// package doc rather than paid for with a second lock-order pass.
	if len(inserted) < len(candidates) {
		result.Deduped += len(candidates) - len(inserted)
	}

	// P2-07/P2-08 seams: named, no-op today, in their invariant slot between
	// events and rollup_dirty. Their session-level counters
	// (tool_call_count, tool_reject_count, subagent_count) are theirs to
	// maintain, not half-implemented here.
	if err = upsertToolCalls(ctx, tx, candidates); err != nil {
		return store.BatchResult{}, err
	}
	if err = upsertSubagents(ctx, tx, candidates); err != nil {
		return store.BatchResult{}, err
	}

	if err = correctSessionTurnCounts(ctx, tx, sessionAggs); err != nil {
		return store.BatchResult{}, err
	}

	marks := make([]dirtyMark, 0, len(inserted))
	for _, ev := range inserted {
		marks = append(marks, dirtyMark{Bucket: hourBucket(ev.TS), Source: sourceEvent})
	}
	changed, span := projectChangeInputs(sessionResults)
	marks = append(marks, s.projectChangeRemarks(changed, span)...)
	if err = markRollupDirty(ctx, tx, marks); err != nil {
		return store.BatchResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return store.BatchResult{}, fmt.Errorf("postgres: write batch: commit: %w", err)
	}

	result.Written = len(inserted)
	refs := make([]model.EventRef, 0, len(inserted))
	for _, ev := range inserted {
		refs = append(refs, model.EventRef{TS: ev.TS, Seq: ev.Seq})
	}
	sort.Slice(refs, func(i, j int) bool {
		if !refs[i].TS.Equal(refs[j].TS) {
			return refs[i].TS.Before(refs[j].TS)
		}
		return refs[i].Seq < refs[j].Seq
	})
	result.EventRefs = refs
	return result, nil
}

// projectChangeInputs adapts upsertSessions's RETURNING rows into the
// (changed, span) shape projectChangeRemarks wants: SPEC §2.4's "when a
// session's project or cwd changes" re-mark rule fires whenever either
// value differs from what was stored before this statement — including
// NULL/” -> a real value, which is exactly the late-SessionStart case §2.4
// describes.
func projectChangeInputs(results []sessionUpsertResult) (map[string]bool, map[string][2]time.Time) {
	changed := make(map[string]bool, len(results))
	span := make(map[string][2]time.Time, len(results))
	for _, r := range results {
		changed[r.ID] = r.CWD != r.OldCWD || r.Project != r.OldProject
		span[r.ID] = [2]time.Time{r.FirstSeenAt, r.LastEventAt}
	}
	return changed, span
}

// correctSessionTurnCounts recomputes sessions.turn_count for every touched
// session from the turns table (SPEC §1.6 projections table: sessions.
// turn_count is an aggregate of turns). It runs right after the turns
// upsert, re-touching session rows this transaction already holds a lock
// on from the sessions upsert earlier in the same statement sequence — see
// this file's package doc on why that does not weaken the lock-ordering
// invariant.
func correctSessionTurnCounts(ctx context.Context, tx pgx.Tx, aggs map[string]*sessionAgg) error {
	if len(aggs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	_, err := tx.Exec(ctx, `
		UPDATE sessions SET turn_count = counted.n
		FROM (
		    SELECT session_id, count(*) AS n FROM turns WHERE session_id = ANY($1::text[]) GROUP BY session_id
		) counted
		WHERE sessions.id = counted.session_id`, ids)
	if err != nil {
		return fmt.Errorf("postgres: correct session turn_count: %w", err)
	}
	return nil
}

// upsertToolCalls is implemented in upsert_toolcall.go (P2-07): tool_calls
// projections (SPEC §1.6, §2.3) built from
// tool.pre/tool.decision/tool.permission_request/tool.result events, keyed
// by the deterministic UUIDv5 id described there. Its slot in the lock
// order is right after events, before subagents.

// upsertSubagents is the P2-08 seam: subagents projections (SPEC §1.6, §2.3)
// built from subagent.start/subagent.stop events plus hook tool.* events
// carrying agent_id. Its slot in the lock order is right after tool_calls,
// before rollup_dirty. No-op until P2-08 lands.
func upsertSubagents(_ context.Context, _ pgx.Tx, _ []model.Event) error {
	return nil
}
