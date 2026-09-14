package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// Every Claude Code hook payload carries `cwd`, but SPEC §1.5.3 only names
// SessionStart/CwdChanged as cwd sources. Claude Code never delivers a
// SessionStart over an http hook, so in practice those two never arrive and
// sessions.project stayed NULL for real traffic. Any hook event's cwd is now
// a lower-ranked fallback: it fills an empty column and never overwrites a
// value one of the two authoritative events wrote.

func TestWriteBatch_ProjectFromCWDOnNonSessionStartHookEvent(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-project-from-tool-result"
	ev := mkEvent(t, sessionID, model.KindToolResult, model.SourceHook, base,
		withAttrs(map[string]any{"cwd": "/home/u/Labs/argus", "hook_event_name": "PostToolUse"}))

	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	var cwd, project string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(cwd, ''), COALESCE(project, '') FROM sessions WHERE id = $1`, sessionID,
	).Scan(&cwd, &project))
	require.Equal(t, "/home/u/Labs/argus", cwd)
	require.Equal(t, "argus", project)
}

func TestWriteBatch_ProjectFromCWDOnSessionEndHookEvent(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 5, 1, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-project-from-session-end"
	ev := mkEvent(t, sessionID, model.KindSessionEnd, model.SourceHook, base,
		withAttrs(map[string]any{"cwd": "/home/u/Labs/argus", "reason": "clear"}))

	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	var project, status string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(project, ''), status FROM sessions WHERE id = $1`, sessionID,
	).Scan(&project, &status))
	require.Equal(t, "argus", project)
	require.Equal(t, "ended", status)
}

// The fallback must never outrank SessionStart's own cwd, in either arrival
// order — a PostToolUse from a subagent running elsewhere must not rewrite
// the project a SessionStart already established.
func TestWriteBatch_SessionStartCWDOutranksFallbackRegardlessOfOrder(t *testing.T) {
	base := time.Date(2026, 6, 5, 2, 0, 0, 0, time.UTC)

	t.Run("fallback arrives after session start", func(t *testing.T) {
		st, pool := newStore(t)
		ctx := context.Background()
		ensureRange(t, st, base, base.Add(time.Hour))

		sessionID := "session-project-start-then-fallback"
		start := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base,
			withAttrs(map[string]any{"cwd": "/home/u/Labs/argus", "source": "startup"}))
		later := mkEvent(t, sessionID, model.KindToolResult, model.SourceHook, base.Add(time.Minute),
			withAttrs(map[string]any{"cwd": "/home/u/other/elsewhere"}))

		_, err := st.WriteBatch(ctx, []model.Event{start})
		require.NoError(t, err)
		_, err = st.WriteBatch(ctx, []model.Event{later})
		require.NoError(t, err)

		var cwd, project string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT COALESCE(cwd, ''), COALESCE(project, '') FROM sessions WHERE id = $1`, sessionID,
		).Scan(&cwd, &project))
		require.Equal(t, "/home/u/Labs/argus", cwd)
		require.Equal(t, "argus", project)
	})

	t.Run("session start arrives after fallback", func(t *testing.T) {
		st, pool := newStore(t)
		ctx := context.Background()
		ensureRange(t, st, base, base.Add(time.Hour))

		sessionID := "session-project-fallback-then-start"
		early := mkEvent(t, sessionID, model.KindToolResult, model.SourceHook, base,
			withAttrs(map[string]any{"cwd": "/home/u/other/elsewhere"}))
		start := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base.Add(time.Minute),
			withAttrs(map[string]any{"cwd": "/home/u/Labs/argus", "source": "startup"}))

		_, err := st.WriteBatch(ctx, []model.Event{early})
		require.NoError(t, err)
		_, err = st.WriteBatch(ctx, []model.Event{start})
		require.NoError(t, err)

		var cwd, project string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT COALESCE(cwd, ''), COALESCE(project, '') FROM sessions WHERE id = $1`, sessionID,
		).Scan(&cwd, &project))
		require.Equal(t, "/home/u/Labs/argus", cwd)
		require.Equal(t, "argus", project)
	})

	t.Run("both in one batch", func(t *testing.T) {
		st, pool := newStore(t)
		ctx := context.Background()
		ensureRange(t, st, base, base.Add(time.Hour))

		sessionID := "session-project-same-batch"
		start := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base,
			withAttrs(map[string]any{"cwd": "/home/u/Labs/argus", "source": "startup"}))
		later := mkEvent(t, sessionID, model.KindToolResult, model.SourceHook, base.Add(time.Minute),
			withAttrs(map[string]any{"cwd": "/home/u/other/elsewhere"}))

		_, err := st.WriteBatch(ctx, []model.Event{start, later})
		require.NoError(t, err)

		var cwd, project string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT COALESCE(cwd, ''), COALESCE(project, '') FROM sessions WHERE id = $1`, sessionID,
		).Scan(&cwd, &project))
		require.Equal(t, "/home/u/Labs/argus", cwd)
		require.Equal(t, "argus", project)
	})
}

// An OTel-sourced cwd attribute is not a hook fact and must stay ignored
// (SPEC §1.5.3: cwd/project come from hooks).
func TestWriteBatch_OTelCWDAttrDoesNotSetProject(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 5, 3, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-project-otel-cwd-ignored"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withAttrs(map[string]any{"cwd": "/home/u/Labs/argus"}))

	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	var cwd, project string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(cwd, ''), COALESCE(project, '') FROM sessions WHERE id = $1`, sessionID,
	).Scan(&cwd, &project))
	require.Empty(t, cwd)
	require.Empty(t, project)
}
