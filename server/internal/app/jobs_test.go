package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// TestPartitionJob_TickReachesBackToRetentionHorizon pins P3-12's actual
// change: the *caller* (this job, and App.New's startup call, which computes
// the same range) passes `from = now - ARGUS_RETENTION_RAW_DAYS` rather than
// `from = now`, so the months an in-retention backfill can land in already
// exist (SPEC §2.4 "Backward creation", deviation D-14).
//
// It asserts on the caller rather than on EnsurePartitions because
// EnsurePartitions already honoured any [from, to] range before P3-12 — a
// test that calls it directly with a backward range passes on either side of
// this change and so proves nothing about it.
func TestPartitionJob_TickReachesBackToRetentionHorizon(t *testing.T) {
	pool := storetesting.NewPool(t)
	store := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	const retentionRawDays = 90
	job := NewPartitionJob(store, logger, retentionRawDays)

	now := time.Now().UTC()
	job.now = func() time.Time { return now }
	job.tick(context.Background())

	// Every month from the retention floor through the forward horizon must
	// now have an `events` partition, including the ones behind the current
	// month — which is the part that did not exist before P3-12.
	floor := now.AddDate(0, 0, -retentionRawDays)
	for month := time.Date(floor.Year(), floor.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(now.Add(partitionJobHorizon)); month = month.AddDate(0, 1, 0) {
		name := fmt.Sprintf("events_%04d_%02d", month.Year(), int(month.Month()))
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists))
		require.True(t, exists, "partition %s must exist after one tick: the job's range is [now-%dd, now+horizon]", name, retentionRawDays)
	}

	// And the month just before the retention floor must NOT: backward
	// creation closes the in-window backfill gap, it does not widen the
	// retention window itself.
	beyond := time.Date(floor.Year(), floor.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	name := fmt.Sprintf("events_%04d_%02d", beyond.Year(), int(beyond.Month()))
	var exists bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists))
	require.False(t, exists, "partition %s is older than the retention horizon and must not be created", name)
}
