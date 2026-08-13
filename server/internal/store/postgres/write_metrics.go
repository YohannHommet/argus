// Package postgres — write_metrics.go implements store.Writer.WriteMetrics,
// the P2-06 deviation recorded in store.go's Writer doc: SPEC §3.3 only
// lists WriteBatch, but §1.8/§2.3 require OTLP metric data points to reach
// metric_samples. Same ledger, same relative lock order (dedup ->
// metric_samples -> rollup_dirty), same too_old handling as WriteBatch —
// see write.go's package doc, which this mirrors at smaller scale (no
// projections: metrics never touch sessions/turns/tool_calls/subagents,
// SPEC §1.8: "No metric is ever mirrored into events").
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// WriteMetrics implements store.Writer.WriteMetrics.
func (s *Store) WriteMetrics(ctx context.Context, samples []model.MetricSample) (store.BatchResult, error) {
	if len(samples) == 0 {
		return store.BatchResult{}, nil
	}

	sorted := make([]model.MetricSample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TS.Equal(sorted[j].TS) {
			return sorted[i].TS.Before(sorted[j].TS)
		}
		return sorted[i].DedupKey < sorted[j].DedupKey
	})

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.BatchResult{}, fmt.Errorf("postgres: write metrics: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	dedupKeys := make([]string, len(sorted))
	for i, m := range sorted {
		dedupKeys[i] = m.DedupKey
	}
	survived, err := insertIngestDedup(ctx, tx, dedupKeys)
	if err != nil {
		return store.BatchResult{}, err
	}

	covers, err := partitionCoverage(ctx, tx, "metric_samples")
	if err != nil {
		return store.BatchResult{}, err
	}

	seenKey := map[string]bool{}
	var candidates []model.MetricSample
	deduped, tooOld := 0, 0
	for _, m := range sorted {
		if !survived[m.DedupKey] || seenKey[m.DedupKey] {
			deduped++
			continue
		}
		seenKey[m.DedupKey] = true
		if !covers(m.TS) {
			tooOld++
			continue
		}
		candidates = append(candidates, m)
	}

	result := store.BatchResult{Deduped: deduped, TooOld: tooOld, Rejected: tooOld}
	if len(candidates) == 0 {
		if err = tx.Commit(ctx); err != nil {
			return store.BatchResult{}, fmt.Errorf("postgres: write metrics: commit: %w", err)
		}
		return result, nil
	}

	insertedTS, err := insertMetricSamples(ctx, tx, candidates)
	if err != nil {
		return store.BatchResult{}, err
	}
	if len(insertedTS) < len(candidates) {
		result.Deduped += len(candidates) - len(insertedTS)
	}

	marks := make([]dirtyMark, 0, len(insertedTS))
	for _, ts := range insertedTS {
		marks = append(marks, dirtyMark{Bucket: hourBucket(ts), Source: sourceMetric})
	}
	if err = markRollupDirty(ctx, tx, marks); err != nil {
		return store.BatchResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return store.BatchResult{}, fmt.Errorf("postgres: write metrics: commit: %w", err)
	}

	result.Written = len(insertedTS)
	return result, nil
}

// insertMetricSamples bulk-inserts candidates into metric_samples, sorted by
// (ts, dedup_key) ascending for the same reason events.go's insertEvents is:
// consistent, deterministic statement-internal ordering. Returns the ts of
// every row actually admitted (for the rollup_dirty hour marks).
func insertMetricSamples(ctx context.Context, tx pgx.Tx, candidates []model.MetricSample) ([]time.Time, error) {
	sorted := make([]model.MetricSample, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TS.Equal(sorted[j].TS) {
			return sorted[i].TS.Before(sorted[j].TS)
		}
		return sorted[i].DedupKey < sorted[j].DedupKey
	})

	n := len(sorted)
	ts := make([]time.Time, n)
	ingestedAt := make([]time.Time, n)
	name := make([]string, n)
	vendor := make([]string, n)
	sessionID := make([]*string, n)
	value := make([]float64, n)
	delta := make([]*float64, n)
	temporality := make([]string, n)
	seriesHash := make([][]byte, n)
	attrs := make([]string, n)
	dedupKey := make([]string, n)

	for i, m := range sorted {
		ts[i] = m.TS
		ingestedAt[i] = m.IngestedAt
		name[i] = m.Name
		vendor[i] = m.Vendor
		sessionID[i] = m.SessionID
		value[i] = m.Value
		delta[i] = m.Delta
		temporality[i] = m.Temporality
		seriesHash[i] = m.SeriesHash
		dedupKey[i] = m.DedupKey

		raw := m.Attrs
		if raw == nil {
			raw = map[string]any{}
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("postgres: marshal metric attrs (dedup_key=%s): %w", m.DedupKey, err)
		}
		attrs[i] = string(b)
	}

	rows, err := tx.Query(ctx, `
		INSERT INTO metric_samples (ts, ingested_at, name, vendor, session_id, value, delta, temporality, series_hash, attrs, dedup_key)
		SELECT * FROM unnest(
		    $1::timestamptz[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::float8[], $7::float8[],
		    $8::text[], $9::bytea[], $10::jsonb[], $11::text[]
		)
		ON CONFLICT (ts, series_hash, dedup_key) DO NOTHING
		RETURNING ts`,
		ts, ingestedAt, name, vendor, sessionID, value, delta, temporality, seriesHash, attrs, dedupKey,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: insert metric_samples: %w", err)
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("postgres: scan inserted metric_sample: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: insert metric_samples: %w", err)
	}
	return out, nil
}
