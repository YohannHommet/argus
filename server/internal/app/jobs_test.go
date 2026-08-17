package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// TestRetentionJob_NextRun pins RetentionJob.Run's "daily at
// ARGUS_RETENTION_HOUR" scheduling (SPEC §2.4): the next occurrence of the
// configured local hour, today if still ahead, tomorrow otherwise.
func TestRetentionJob_NextRun(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	job := NewRetentionJob(nil, logger, 90, 7*24*time.Hour, 0, 4)

	beforeHour := time.Date(2026, 6, 15, 1, 0, 0, 0, time.UTC)
	want := time.Date(2026, 6, 15, 4, 0, 0, 0, time.UTC)
	require.True(t, job.nextRun(beforeHour).Equal(want), "before 04:00 must schedule later today")

	afterHour := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	wantTomorrow := time.Date(2026, 6, 16, 4, 0, 0, 0, time.UTC)
	require.True(t, job.nextRun(afterHour).Equal(wantTomorrow), "after 04:00 must schedule tomorrow")

	exactHour := time.Date(2026, 6, 15, 4, 0, 0, 0, time.UTC)
	require.True(t, job.nextRun(exactHour).Equal(wantTomorrow), "exactly at the hour must schedule tomorrow, not immediately")
}

// TestRetentionJob_TickDropsExpiredPartitionAndPrunesDedup is the AC that
// RetentionJob.tick (the daily job's actual pass) both drops a fully-expired
// partition (store.ApplyRetention) and prunes ingest_dedup
// (store.PruneDedup) in one call — exercised directly, the same way
// TestPartitionJob_TickReachesBackToRetentionHorizon calls job.tick rather
// than waiting on Run's real-time scheduling loop.
func TestRetentionJob_TickDropsExpiredPartitionAndPrunesDedup(t *testing.T) {
	pool := storetesting.NewPool(t)
	st := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	const retentionRawDays = 90
	const dedupWindow = 7 * 24 * time.Hour
	job := NewRetentionJob(st, logger, retentionRawDays, dedupWindow, 0, 4)

	now := time.Now().UTC()
	job.now = func() time.Time { return now }

	old := now.AddDate(0, -6, 0)
	require.NoError(t, st.EnsurePartitions(ctx, old, old))
	partitionName := fmt.Sprintf("events_%04d_%02d", old.Year(), int(old.Month()))

	_, err := pool.Exec(ctx, `INSERT INTO ingest_dedup (dedup_key, first_seen_at) VALUES ($1, $2)`,
		"retention-job-old-dedup", now.Add(-dedupWindow-time.Hour))
	require.NoError(t, err)

	job.tick(ctx)

	var partitionExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, partitionName).Scan(&partitionExists))
	require.False(t, partitionExists, "tick must drop the fully-expired partition")

	var dedupExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingest_dedup WHERE dedup_key = 'retention-job-old-dedup')`).Scan(&dedupExists))
	require.False(t, dedupExists, "tick must prune the expired ingest_dedup row")
}

// insertBareSession inserts a minimal `sessions` row directly (no events,
// no WriteBatch) with the given last_event_at, satisfying only the table's
// NOT NULL columns — enough for RetentionJob.tick's session-delete step,
// which only reads last_event_at.
func insertBareSession(t *testing.T, pool *pgxpool.Pool, id string, lastEventAt time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO sessions (id, first_seen_at, last_event_at) VALUES ($1, $2, $2)`, id, lastEventAt)
	require.NoError(t, err)
}

// TestRetentionJob_TickDeletesExpiredSessionsWhenEnabled pins the m11 fix's
// wiring into the job: with ARGUS_RETENTION_SESSION_DAYS > 0, tick deletes a
// session whose last_event_at is older than that horizon and leaves a
// recent one alone.
func TestRetentionJob_TickDeletesExpiredSessionsWhenEnabled(t *testing.T) {
	pool := storetesting.NewPool(t)
	st := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	const sessionRetentionDays = 30
	job := NewRetentionJob(st, logger, 90, 7*24*time.Hour, sessionRetentionDays, 4)

	now := time.Now().UTC()
	job.now = func() time.Time { return now }

	insertBareSession(t, pool, "session-retention-job-old", now.AddDate(0, 0, -sessionRetentionDays-1))
	insertBareSession(t, pool, "session-retention-job-recent", now.AddDate(0, 0, -1))

	job.tick(ctx)

	var oldExists, recentExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = 'session-retention-job-old')`).Scan(&oldExists))
	require.False(t, oldExists, "tick must delete a session older than ARGUS_RETENTION_SESSION_DAYS")
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = 'session-retention-job-recent')`).Scan(&recentExists))
	require.True(t, recentExists, "tick must leave a session within the horizon untouched")
}

// TestRetentionJob_TickSkipsSessionDeleteWhenDisabled pins the documented
// "0 = never" meaning of ARGUS_RETENTION_SESSION_DAYS (SPEC §3.7): with the
// default 0, tick must not delete even a very old session — 0 must not be
// silently treated as "cutoff = now" (which would delete everything).
func TestRetentionJob_TickSkipsSessionDeleteWhenDisabled(t *testing.T) {
	pool := storetesting.NewPool(t)
	st := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	job := NewRetentionJob(st, logger, 90, 7*24*time.Hour, 0, 4)

	now := time.Now().UTC()
	job.now = func() time.Time { return now }

	insertBareSession(t, pool, "session-retention-job-ancient", now.AddDate(-1, 0, 0))

	job.tick(ctx)

	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = 'session-retention-job-ancient')`).Scan(&exists))
	require.True(t, exists, "ARGUS_RETENTION_SESSION_DAYS=0 must mean never delete sessions")
}
