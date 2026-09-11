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
// too_old classification (SPEC §1.7 rule 3) is decided before ingest_dedup to avoid
// baking too_old events' keys into the ledger (m3 fix). See partitionCoverage doc.
//
// # Round trips, not pgx.Batch (m26 correction, 2026-08-14)
//
// Each statement in the lock-order sequence above issues separately (individually round-tripped),
// not batched, in order to preserve the lock-ordering invariant (deferred optimization: m26).
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
	// P3-05 defect 1: tool_calls started_at can differ from triggering event's ts hour (P2-07).
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
	// DedupKey on every ref (M1 fix): internal/ingest's matchPersisted maps persisted ref to batch event.
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

// loadPricesIfNeeded is D-30: load prices only when needed (hot path: most batches have all costs).
// Reads via tx to avoid pool exhaustion under concurrent writes.
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

// projectChangeInputs adapts sessions upsert results for projectChangeRemarks (SPEC §2.4 re-mark rule).
func projectChangeInputs(results []sessionUpsertResult) (map[string]bool, map[string][2]time.Time) {
	changed := make(map[string]bool, len(results))
	span := make(map[string][2]time.Time, len(results))
	for _, r := range results {
		changed[r.ID] = r.CWD != r.OldCWD || r.Project != r.OldProject
		span[r.ID] = [2]time.Time{r.FirstSeenAt, r.LastEventAt}
	}
	return changed, span
}

// correctSessionTurnCounts recomputes sessions.turn_count from turns (SPEC §1.6 projection).
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

// upsertToolCalls: tool_calls projections (P2-07, SPEC §1.6, §2.3).
// upsertSubagents: subagents projections (P2-08, SPEC §1.6, §2.3).
