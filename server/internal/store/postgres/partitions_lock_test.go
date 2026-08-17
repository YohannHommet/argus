package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// partitionsLockKeyForTest mirrors partitions.go's unexported
// partitionsLockKey (0x41_52_47_55_53_30_34, "ARGUS04"): this test holds
// this same key from a separate session/transaction to simulate a
// concurrent EnsurePartitions caller (another argusd process, or this
// process's own PartitionJob tick racing App.New's startup call), so it
// must stay in sync with that constant.
const partitionsLockKeyForTest = int64(0x41_52_47_55_53_30_34)

// TestEnsurePartitions_SerializedByAdvisoryLock pins m34: EnsurePartitions
// must take partitionsLockKey before issuing any CREATE TABLE/INDEX
// statement, so two concurrent callers serialize instead of racing
// CREATE TABLE IF NOT EXISTS's own existence check (which Postgres performs
// before locking the parent, per the finding) and risking a
// 42P07/23505 collision.
//
// Before the fix, EnsurePartitions ran every statement directly against the
// bare pool with no lock at all, so a concurrent caller was never blocked by
// another one already in progress — this test's first phase (asserting
// EnsurePartitions does NOT return while the lock is held elsewhere) is
// exactly what distinguishes the two: it fails immediately against the
// unlocked implementation, because there the call was never blocked in the
// first place.
func TestEnsurePartitions_SerializedByAdvisoryLock(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	now := time.Now()

	// Warm up: pay for connection setup/plan caching outside the timed race
	// below, so that race only ever measures whether the advisory lock
	// actually blocks the second caller, not first-connection latency.
	require.NoError(t, store.EnsurePartitions(ctx, now, now))

	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()

	var locked bool
	require.NoError(t, holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, partitionsLockKeyForTest).Scan(&locked))
	require.True(t, locked, "test setup must hold the lock for this assertion to mean anything")

	done := make(chan error, 1)
	go func() {
		done <- store.EnsurePartitions(ctx, now, now)
	}()

	select {
	case err := <-done:
		t.Fatalf("EnsurePartitions must block while partitionsLockKey is held by another session, but it returned (err=%v)", err)
	case <-time.After(2 * time.Second):
		// still blocked, as expected — a plain CREATE TABLE IF NOT EXISTS
		// against an already-migrated, otherwise-idle test database never
		// takes anywhere close to 2s on its own, so surviving this window
		// means EnsurePartitions is genuinely waiting on something (the
		// advisory lock), not just slow.
	}

	require.NoError(t, holder.Rollback(ctx))

	select {
	case err := <-done:
		require.NoError(t, err, "EnsurePartitions must succeed once the lock is released")
	case <-time.After(15 * time.Second):
		t.Fatal("EnsurePartitions did not complete after the advisory lock was released")
	}
}
