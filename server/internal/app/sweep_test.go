//go:build e2e

// Package app's sweep test (M2 fix) proves the abandoned-session sweep is
// actually scheduled by a running server, not just unit-testable in
// isolation: it starts a real App the same way e2e_ingest_test.go's
// TestE2E_Phase2ExitCriteria does (New+Serve against a real Postgres), lets
// a genuinely idle session cross ARGUS_SESSION_IDLE_TIMEOUT, and polls for
// SweepJob (jobs.go) to flip its status to 'abandoned' on its own —
// something no test reached before this fix, since Serve never started a
// sweep job at all (M2's evidence: SweepAbandoned had zero non-test
// callers).
package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sweepTestPollTimeout bounds how long this test waits for the
// server's own SweepJob (running on ARGUS_SWEEP_INTERVAL, set very small
// below) to notice and sweep the fixture session — analogous to
// e2eDrainTimeout's "wait for the async pipeline to catch up" role for the
// ingest tests in this package.
const sweepTestPollTimeout = 10 * time.Second

// TestE2E_ServeSchedulesAbandonedSessionSweep is the M2 AC: a session whose
// last_event_at is already older than ARGUS_SESSION_IDLE_TIMEOUT when the
// server starts, with status='active' and ended_at IS NULL (the exact
// sessions_sweep_idx predicate, SPEC §2.1), is flipped to 'abandoned' by the
// running server without any request ever touching it — proving Serve
// itself schedules the sweep (jobs.go's SweepJob), not just that
// store.SweepAbandoned works when called directly (write_test.go already
// covers that at the store layer).
func TestE2E_ServeSchedulesAbandonedSessionSweep(t *testing.T) {
	// Small enough that the fixture session (inserted with an already-past
	// last_event_at) is eligible on the very first tick, and the job ticks
	// again well within sweepTestPollTimeout if it somehow isn't.
	t.Setenv("ARGUS_SWEEP_INTERVAL", "100ms")
	t.Setenv("ARGUS_SESSION_IDLE_TIMEOUT", "1s")

	_, _, pool := newE2EApp(t)
	ctx := context.Background()

	sessionID := "e2e-sweep-abandoned-session"
	_, err := pool.Exec(ctx, `
		INSERT INTO sessions (id, status, ended_at, first_seen_at, last_event_at)
		VALUES ($1, 'active', NULL, $2, $2)`,
		sessionID, time.Now().Add(-time.Hour))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, sessionID).Scan(&status); err != nil {
			return false
		}
		return status == "abandoned"
	}, sweepTestPollTimeout, 50*time.Millisecond,
		"a running server must sweep an idle session to 'abandoned' on its own (SweepJob never started before the M2 fix)")
}
