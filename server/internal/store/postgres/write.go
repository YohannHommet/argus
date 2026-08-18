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
// *before* the ingest_dedup gate and before the sessions/turns statements
// are built, not by reacting to the events INSERT's error after the fact.
// If it were reactive, the too_old-excluded rows would already be baked
// into the sessions/turns aggregates queued ahead of the events statement
// (or, worse, into the dedup ledger — see the next paragraph), and there
// would be no way to un-count them without a second pass that touches
// sessions/turns *again* out of order. Deciding too_old first keeps the
// literal table order intact and keeps projections exactly consistent with
// what actually lands in `events`. It does not weaken the deadlock
// invariant: partitionCoverage only reads pg_class/pg_inherits metadata, it
// takes no lock on sessions/turns/events rows. See partitions.go's
// partitionCoverage doc for the full reasoning and its accepted TOCTOU
// trade-off, and IsTooOld's doc for why it is still exercised as defence in
// depth.
//
// A consequence of deciding too_old first (audit finding m3, fixed here):
// only the dedup keys of events that *do* have a partition to land in are
// ever admitted into ingest_dedup. Gating dedup admission on every sorted
// event regardless of coverage would burn a too_old event's key against the
// ledger even though the row never reached `events` — so a legitimate
// replay after an operator restores the missing partition (D-18: the only
// way too_old is reachable at all) would come back `deduped`, not written,
// and the event would be lost for the ledger's full ARGUS_DEDUP_WINDOW.
//
// # Round trips, not pgx.Batch (m26 correction, 2026-08-14)
//
// This section previously claimed everything after the dedup gate "is
// queued as one pgx.Batch and sent together". That was aspirational, not
// descriptive: nothing in this package calls pgx.Batch or Tx.SendBatch.
// Every statement in the sequence documented above — ingest_dedup, the
// sessions/turns upserts, the events insert, the tool_calls/subagents
// seams, rollup_dirty — is issued as its own individually round-tripped
// statement or query (insertIngestDedup via *pgx.Tx directly; the rest via
// upsert_session.go:335, upsert_turn.go:179/:245, events.go:134,
// upsert_toolcall.go:557, upsert_subagent.go:395, this file's
// correctSessionTurnCounts, dirty.go:95), in the fixed order the invariant
// above requires — order is preserved because each call blocks the tx on
// the previous one completing, the same guarantee pgx.Batch would have
// given, just paid for as ~8 network round trips per transaction instead of
// one. That is the window every concurrent overlapping-session write blocks
// on for the row locks this transaction holds. Collapsing the sequence into
// one real pgx.Batch (CopyFrom is not an option: it cannot express ON
// CONFLICT, which the ingest_dedup gate, the events parent-level defence in
// depth, and every projection upsert all need) is a real latency win, but a
// ~1-day change across files this ticket does not own — deferred, not
// implemented, by audit finding m26.
package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/pricing"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
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

	covers, err := partitionCoverage(ctx, tx, "events")
	if err != nil {
		return store.BatchResult{}, err
	}

	// Split by partition coverage *before* the ingest_dedup gate (m3 fix):
	// see this file's package doc for why a too_old event's dedup_key must
	// never be admitted to the ledger. partitionCoverage takes no row
	// locks, so doing this first does not disturb the lock-ordering
	// invariant.
	covered := make([]model.Event, 0, len(sorted))
	tooOld := 0
	for _, e := range sorted {
		if covers(e.TS) {
			covered = append(covered, e)
		} else {
			tooOld++
		}
	}

	dedupKeys := make([]string, len(covered))
	for i, e := range covered {
		dedupKeys[i] = e.DedupKey
	}
	survived, err := insertIngestDedup(ctx, tx, dedupKeys)
	if err != nil {
		return store.BatchResult{}, err
	}

	// Collapse to one candidate per distinct dedup_key (SPEC §1.7 rule 2's
	// ledger has one row per key regardless of how many batch entries share
	// it — see dedup.go's doc). Every entry here already has a partition to
	// land in.
	seenKey := map[string]bool{}
	var candidates []model.Event
	deduped := 0
	for _, e := range covered {
		if !survived[e.DedupKey] {
			deduped++
			continue
		}
		if seenKey[e.DedupKey] {
			deduped++
			continue
		}
		seenKey[e.DedupKey] = true
		candidates = append(candidates, e)
	}

	result := store.BatchResult{Deduped: deduped, TooOld: tooOld, Rejected: tooOld}
	if len(candidates) == 0 {
		if err = tx.Commit(ctx); err != nil {
			return store.BatchResult{}, fmt.Errorf("postgres: write batch: commit: %w", err)
		}
		return result, nil
	}

	prices, err := loadPricesIfNeeded(ctx, tx, candidates)
	if err != nil {
		return store.BatchResult{}, err
	}
	sessionAggs := foldSessionEvents(candidates, prices)
	turnAggs := foldTurnEvents(candidates, prices)

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

	// P2-07/P2-08 seams: filled in by upsertToolCalls/upsertSubagents, in
	// their invariant slot between events and rollup_dirty. Their
	// session-level counters (tool_call_count, tool_reject_count,
	// subagent_count) are theirs to maintain.
	toolCallStartedAts, err := upsertToolCalls(ctx, tx, candidates)
	if err != nil {
		return store.BatchResult{}, err
	}
	if err = upsertSubagents(ctx, tx, candidates); err != nil {
		return store.BatchResult{}, err
	}

	if err = correctSessionTurnCounts(ctx, tx, sessionAggs); err != nil {
		return store.BatchResult{}, err
	}

	marks := make([]dirtyMark, 0, len(inserted)+len(toolCallStartedAts))
	for _, ev := range inserted {
		marks = append(marks, dirtyMark{Bucket: hourBucket(ev.TS), Source: sourceEvent})
	}
	// P3-05 defect 1: rollup_hourly.tool_calls/tool_rejects is now bucketed
	// on tool_calls.started_at (AggregateToolCallRollup), which can fall in
	// a different hour than the ts of the event that just touched the row
	// (e.g. a late tool_decision arriving an hour after the PreToolUse hook
	// that set started_at) — so every touched call's started_at hour is
	// marked dirty too, not just the triggering events' own ts hours. See
	// upsertToolCalls's doc comment for the full reasoning.
	for _, ts := range toolCallStartedAts {
		marks = append(marks, dirtyMark{Bucket: hourBucket(ts), Source: sourceEvent})
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
	// DedupKey rides along on every ref (M1 fix): internal/ingest's
	// matchPersisted keys off it to map a persisted ref back to the
	// submitted batch event it belongs to, since `batch` arrives in
	// arrival order, not the (ts, seq) order these refs are sorted into
	// below — see matchPersisted's doc.
	refs := make([]model.EventRef, 0, len(inserted))
	for dk, ins := range inserted {
		refs = append(refs, model.EventRef{TS: ins.TS, Seq: ins.Seq, DedupKey: dk})
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

// loadPricesIfNeeded is D-30's price-loading step (docs/review/
// phase-4-gauntlet.md, owner-ratified 2026-08-18): sessions/turns'
// cost_estimated_usd (SPEC §2.4) needs a []pricing.Price to estimate an
// uncosted llm.request's tokens, the same way the rollup job already does
// (rollups.go:142-154 — fromModelPrice/toPricingPrices are reused verbatim,
// not duplicated).
//
// The scan-first, load-only-if-needed shape matters for the hot path:
// Claude Code reports cost_usd on effectively every api_request, so the
// overwhelmingly common WriteBatch call has zero KindLLMRequest events with
// CostUSD == nil and must not pay for a model_prices round trip it will
// throw away. And when a price *is* needed, this reads via tx (the same
// transaction already inside its Begin/Commit), not s.pool: a pool read
// nested inside a write transaction would take a second connection out of a
// 10-connection pool while up to 4 ingest workers are concurrently writing
// (SPEC §7.2's worker count) — exactly the kind of self-inflicted pool
// exhaustion a nested s.pool.Query here would risk under load.
func loadPricesIfNeeded(ctx context.Context, tx pgx.Tx, candidates []model.Event) ([]pricing.Price, error) {
	needsPricing := false
	for _, e := range candidates {
		if e.Kind == model.KindLLMRequest && e.CostUSD == nil {
			needsPricing = true
			break
		}
	}
	if !needsPricing {
		return nil, nil
	}

	priceRows, err := gen.New(tx).ListModelPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: write batch: list model prices: %w", err)
	}
	converted := make([]PriceRow, 0, len(priceRows))
	for _, p := range priceRows {
		pp, convErr := fromModelPrice(p)
		if convErr != nil {
			return nil, fmt.Errorf("postgres: write batch: model price %s: %w", p.Model, convErr)
		}
		converted = append(converted, pp)
	}
	return toPricingPrices(converted), nil
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

// upsertSubagents is implemented in upsert_subagent.go (P2-08): subagents
// projections (SPEC §1.6, §2.3) built from subagent.start/subagent.stop
// events plus hook tool.* events carrying agent_id (the latter via a
// post-upsert recompute against the already-settled tool_calls table, not
// a fold here — see that file's package doc). Its slot in the lock order
// is right after tool_calls, before rollup_dirty.
