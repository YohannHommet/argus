package postgres_test

// m3: a too_old rejection must not burn its ingest_dedup key.
//
// Context (D-18, docs/review/phase-3-deviations.md): too_old is no longer
// routinely reachable through normal ingest — the backward partition
// creation plus the §1.2 clamp leave no timestamp that is both in-window
// and unpartitioned — so it now fires only when a partition is genuinely
// absent (operator dropped one, or the partition manager fell behind).
// These tests reach that state the same way
// TestApplyRetention_DroppedPartitionRejectsInWindowEventAsTooOld
// (retention_test.go) does: ApplyRetention itself drops the partition,
// rather than asserting a state normal ingest can no longer produce.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

func TestWriteBatch_TooOldReplayAfterPartitionRestoredIsWritten(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	inWindow := time.Now().UTC().AddDate(0, -2, 0)
	ensureRange(t, st, inWindow, inWindow)
	partitionName := fmt.Sprintf("events_%04d_%02d", inWindow.Year(), int(inWindow.Month()))
	require.True(t, partitionExists(t, pool, partitionName))

	cutoff := time.Now().UTC()
	dropped, err := st.ApplyRetention(ctx, cutoff, false)
	require.NoError(t, err)
	require.Contains(t, dropped, partitionName)
	require.False(t, partitionExists(t, pool, partitionName))

	sessionID := "session-m3-too-old-replay"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, inWindow.Add(time.Hour))

	result, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)
	require.Equal(t, 0, result.Written)
	require.Equal(t, 1, result.TooOld)
	require.Equal(t, 0, result.Deduped, "the m3 bug: a too_old event's key must never be admitted to ingest_dedup in the first place")

	// The ledger must not carry this event's dedup_key at all — the whole
	// point of fixing the write order (partition coverage before the dedup
	// gate) is that a key never admitted into `events` never burns its
	// ledger row either.
	var dedupCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ingest_dedup WHERE dedup_key = $1`, ev.DedupKey).Scan(&dedupCount))
	require.Equal(t, 0, dedupCount, "a too_old event's dedup_key must not be admitted to ingest_dedup")

	// Restore the partition (an operator fixing the gap D-18 describes) and
	// replay the exact same event: it must be WRITTEN, not silently
	// reported as `deduped`.
	ensureRange(t, st, inWindow, inWindow)
	require.True(t, partitionExists(t, pool, partitionName))

	replay, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)
	require.Equal(t, 1, replay.Written, "a replay after the partition is restored must be written, not deduped")
	require.Equal(t, 0, replay.Deduped)
	require.Equal(t, 0, replay.TooOld)
}

func TestWriteMetrics_TooOldReplayAfterPartitionRestoredIsWritten(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	inWindow := time.Now().UTC().AddDate(0, -2, 0)
	ensureRange(t, st, inWindow, inWindow)
	partitionName := fmt.Sprintf("metric_samples_%04d_%02d", inWindow.Year(), int(inWindow.Month()))
	require.True(t, partitionExists(t, pool, partitionName))

	cutoff := time.Now().UTC()
	dropped, err := st.ApplyRetention(ctx, cutoff, false)
	require.NoError(t, err)
	require.Contains(t, dropped, partitionName)
	require.False(t, partitionExists(t, pool, partitionName))

	dedupKey, err := model.DedupKeyMetric("claude_code.cost.usage", inWindow.Add(time.Hour), map[string]any{"model": "claude-opus-5"})
	require.NoError(t, err)
	sample := model.MetricSample{
		TS:          inWindow.Add(time.Hour),
		IngestedAt:  inWindow.Add(time.Hour),
		Name:        "claude_code.cost.usage",
		Vendor:      "claude_code",
		Value:       1.5,
		Temporality: "delta",
		SeriesHash:  []byte("m3-series-hash-0000000000000001"),
		Attrs:       map[string]any{"model": "claude-opus-5"},
		DedupKey:    dedupKey,
	}

	result, err := st.WriteMetrics(ctx, []model.MetricSample{sample})
	require.NoError(t, err)
	require.Equal(t, 0, result.Written)
	require.Equal(t, 1, result.TooOld)
	require.Equal(t, 0, result.Deduped)

	var dedupCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ingest_dedup WHERE dedup_key = $1`, dedupKey).Scan(&dedupCount))
	require.Equal(t, 0, dedupCount, "a too_old metric sample's dedup_key must not be admitted to ingest_dedup")

	ensureRange(t, st, inWindow, inWindow)
	require.True(t, partitionExists(t, pool, partitionName))

	replay, err := st.WriteMetrics(ctx, []model.MetricSample{sample})
	require.NoError(t, err)
	require.Equal(t, 1, replay.Written, "a replay after the partition is restored must be written, not deduped")
	require.Equal(t, 0, replay.Deduped)
	require.Equal(t, 0, replay.TooOld)
}
