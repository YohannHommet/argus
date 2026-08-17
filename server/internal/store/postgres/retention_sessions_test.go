package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// TestDeleteExpiredSessions_DeletesOnlyOlderSessionsCascadingToTurns pins the
// m11 fix: ARGUS_RETENTION_SESSION_DAYS was parsed, validated, and
// documented (SPEC §2.4/§3.7) but read by no code — this is the first test
// exercising the store-level primitive the retention job now calls. A
// session older than cutoff (by last_event_at) must be deleted, its turns
// cascade-deleted with it (SPEC §2.1's `ON DELETE CASCADE`), and a
// still-recent session must be left untouched.
func TestDeleteExpiredSessions_DeletesOnlyOlderSessionsCascadingToTurns(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	now := time.Now().UTC()
	ensureRange(t, st, base, now)

	oldSessionID := "session-retention-old"
	oldEvents := []model.Event{
		mkEvent(t, oldSessionID, model.KindSessionStart, model.SourceHook, base, withAttrs(map[string]any{"cwd": "/x/old"})),
		mkEvent(t, oldSessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Minute), withModel("claude-a"), withTokens(1, 2)),
	}
	_, err := st.WriteBatch(ctx, oldEvents)
	require.NoError(t, err)

	// The recent session's own event ts is "now" (not tied to the fixed,
	// long-past `base`), so its last_event_at genuinely lands inside the
	// cutoff window below without any fixture patching.
	recentSessionID := "session-retention-recent"
	recentEvents := []model.Event{
		mkEvent(t, recentSessionID, model.KindSessionStart, model.SourceHook, now, withAttrs(map[string]any{"cwd": "/x/recent"})),
	}
	_, err = st.WriteBatch(ctx, recentEvents)
	require.NoError(t, err)

	// Force the old session's last_event_at far enough in the past to be
	// older than the cutoff below.
	_, err = pool.Exec(ctx, `UPDATE sessions SET last_event_at = $1 WHERE id = $2`,
		now.Add(-100*24*time.Hour), oldSessionID)
	require.NoError(t, err)

	var oldTurnCountBefore int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM turns WHERE session_id = $1`, oldSessionID).Scan(&oldTurnCountBefore))

	cutoff := now.Add(-90 * 24 * time.Hour)
	deleted, err := st.DeleteExpiredSessions(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	var oldExists, recentExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, oldSessionID).Scan(&oldExists))
	require.False(t, oldExists, "the session older than cutoff must be deleted")
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, recentSessionID).Scan(&recentExists))
	require.True(t, recentExists, "a session more recent than cutoff must be left alone")

	var oldTurnCountAfter int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM turns WHERE session_id = $1`, oldSessionID).Scan(&oldTurnCountAfter))
	require.Zero(t, oldTurnCountAfter, "ON DELETE CASCADE must remove the deleted session's turns too")
}

// TestDeleteExpiredSessions_NeverDeletesRollups pins SPEC §2.4's "rollups
// and projections are never deleted by raw retention", applied to the
// session-retention half of the same guarantee: rollup_hourly carries no
// session_id (it keys on bucket/project/vendor/model/source), so deleting
// an expired session's row must leave its already-computed rollup bucket
// exactly as it was.
func TestDeleteExpiredSessions_NeverDeletesRollups(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 2, 5, 8, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-retention-rollup"
	events := []model.Event{
		mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base, withAttrs(map[string]any{"cwd": "/x/rollup-proj"})),
		mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Minute), withModel("claude-a"), withTokens(5, 7), withCost(0.02, "reported")),
	}
	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var rollupCountBefore int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_hourly WHERE bucket = $1 AND source = 'event'`,
		base.Truncate(time.Hour)).Scan(&rollupCountBefore))
	require.Positive(t, rollupCountBefore, "fixture must have produced a rollup_hourly row before the delete")

	_, err = pool.Exec(ctx, `UPDATE sessions SET last_event_at = $1 WHERE id = $2`,
		time.Now().Add(-100*24*time.Hour), sessionID)
	require.NoError(t, err)

	deleted, err := st.DeleteExpiredSessions(ctx, time.Now().Add(-90*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	var rollupCountAfter int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_hourly WHERE bucket = $1 AND source = 'event'`,
		base.Truncate(time.Hour)).Scan(&rollupCountAfter))
	require.Equal(t, rollupCountBefore, rollupCountAfter, "deleting the session must not touch rollup_hourly")
}
