package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// checksumTable returns a stable, order-independent full-row checksum of
// every row `selectSQL` returns (one text-castable expression per row,
// aggregated with string_agg ordered by orderBy so two runs over identical
// data always produce the same digest regardless of physical row order).
// sessions passes a column list that excludes `updated_at` (a DEFAULT now()
// bookkeeping column, SPEC has no such column on the other three projection
// tables — see rebuild_test.go's checksum tests for why it must be
// excluded); the other three tables pass "t.*".
func checksumTable(t *testing.T, pool *pgxpool.Pool, rowExpr, from, orderBy string) string {
	t.Helper()
	var sum string
	err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(md5(string_agg((`+rowExpr+`)::text, '|' ORDER BY `+orderBy+`)), 'empty')
		FROM `+from+` t`).Scan(&sum)
	require.NoError(t, err)
	return sum
}

// projectionChecksums is a full-row checksum of all four SPEC §1.6
// projection tables (sessions, turns, tool_calls, subagents), used to assert
// RebuildProjections reproduces byte-identical rows. sessions excludes
// `updated_at` (SPEC has no equivalent bookkeeping column on the other
// three) since that column legitimately differs between the original write
// and a later rebuild.
type projectionChecksums struct {
	Sessions, Turns, ToolCalls, Subagents string
}

func checksumProjections(t *testing.T, pool *pgxpool.Pool) projectionChecksums {
	t.Helper()
	return projectionChecksums{
		Sessions: checksumTable(t, pool,
			`t.id, t.vendor, t.project, t.cwd, t.status, t.start_type, t.end_reason, t.permission_mode,
			 t.app_version, t.entrypoint, t.terminal_type, t.user_email, t.user_account_uuid, t.organization_id,
			 t.started_at, t.ended_at, t.first_seen_at, t.last_event_at, t.event_count, t.turn_count,
			 t.tool_call_count, t.tool_reject_count, t.subagent_count, t.error_count, t.input_tokens,
			 t.output_tokens, t.cache_read_tokens, t.cache_creation_tokens, t.cost_usd, t.cost_estimated_usd,
			 t.cost_by_query_source, t.models, t.field_ranks`,
			"sessions", "t.id"),
		Turns:     checksumTable(t, pool, "t.*", "turns", "t.session_id, t.prompt_id"),
		ToolCalls: checksumTable(t, pool, "t.*", "tool_calls", "t.id"),
		Subagents: checksumTable(t, pool, "t.*", "subagents", "t.session_id, t.agent_id"),
	}
}

func toolCallIDs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id FROM tool_calls ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// withAgentID/withParentAgentID/withAgentType/withSuccess are local eventOpt
// helpers (see write_test.go's eventOpt type) for the subagent-lifecycle and
// hook-agent-attribution fields this file's fixture needs that no existing
// with* helper in write_test.go/upsert_toolcall_test.go covers.
func withAgentID(id string) eventOpt   { return func(e *model.Event) { e.AgentID = &id } }
func withAgentType(a string) eventOpt  { return func(e *model.Event) { e.AgentType = &a } }
func withSuccess(ok bool) eventOpt     { return func(e *model.Event) { e.Success = &ok } }
func withToolUseID(id string) eventOpt { return func(e *model.Event) { e.ToolUseID = &id } }
func withToolName(n string) eventOpt   { return func(e *model.Event) { e.ToolName = &n } }
func withDecision(d, src string) eventOpt {
	return func(e *model.Event) { e.Decision = &d; e.DecisionSource = &src }
}

// buildRebuildFixture writes a moderately rich, multi-batch dataset spanning
// all four projection tables: a session with an LLM request (turn), an
// otel-only tool call, and a subagent with its own hook-attributed tool
// call — exactly the shape RebuildProjections's four TRUNCATEd tables must
// reproduce identically. Returns the session id and the fixture's base ts
// (the earliest event), so callers can pick a fromTS at or before it.
func buildRebuildFixture(t *testing.T, st *postgres.Store, base time.Time, sessionID string) time.Time {
	t.Helper()
	ctx := context.Background()
	ensureRange(t, st, base, base)

	promptID := "prompt-1"

	// Batch 1: session lifecycle + an LLM request (populates sessions/turns).
	start := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base,
		withAttrs(map[string]any{"cwd": "/x/rebuild-proj"}))
	llm := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Second),
		withPromptID(promptID), withModel("claude-a"), withTokens(10, 20), withCost(0.02, "reported"))
	_, err := st.WriteBatch(ctx, []model.Event{start, llm})
	require.NoError(t, err)

	// Batch 2: an otel-only tool call under the main agent.
	mainToolUseID := "toolu_main_1"
	pre := mkEvent(t, sessionID, model.KindToolPre, model.SourceOTelLog, base.Add(2*time.Second),
		withPromptID(promptID), withToolName("Read"), withToolUseID(mainToolUseID))
	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(3*time.Second),
		withPromptID(promptID), withToolName("Read"), withToolUseID(mainToolUseID), withDecision("accept", "config"))
	result := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base.Add(4*time.Second),
		withPromptID(promptID), withToolName("Read"), withToolUseID(mainToolUseID), withSuccess(true))
	_, err = st.WriteBatch(ctx, []model.Event{pre, decision, result})
	require.NoError(t, err)

	// Batch 3: a subagent with its own hook-attributed, exact-correlated
	// tool call (agent_id set on both the lifecycle and the tool events, so
	// subagents.tool_call_count gets a real, non-NULL value — SPEC §1.9).
	agentID := "agent-1"
	subagentStart := mkEvent(t, sessionID, model.KindSubagentStart, model.SourceHook, base.Add(5*time.Second),
		withAgentID(agentID), withAgentType("Explore"), withPromptID(promptID))
	subToolUseID := "toolu_sub_1"
	subHookPre := mkEvent(t, sessionID, model.KindToolPre, model.SourceHook, base.Add(6*time.Second),
		withAgentID(agentID), withPromptID(promptID), withToolName("Grep"), withToolUseID(subToolUseID))
	subDecision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(7*time.Second),
		withPromptID(promptID), withToolName("Grep"), withToolUseID(subToolUseID), withDecision("accept", "config"))
	subResult := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base.Add(8*time.Second),
		withPromptID(promptID), withToolName("Grep"), withToolUseID(subToolUseID), withSuccess(true))
	subagentStop := mkEvent(t, sessionID, model.KindSubagentStop, model.SourceHook, base.Add(9*time.Second),
		withAgentID(agentID), withSuccess(true))
	_, err = st.WriteBatch(ctx, []model.Event{subagentStart, subHookPre, subDecision, subResult, subagentStop})
	require.NoError(t, err)

	// Batch 4: session end.
	end := mkEvent(t, sessionID, model.KindSessionEnd, model.SourceHook, base.Add(10*time.Second))
	_, err = st.WriteBatch(ctx, []model.Event{end})
	require.NoError(t, err)

	return base
}

// --- AC: RebuildProjections, after truncating the four projection tables, -
// --- reproduces byte-identical rows (full-row checksum, tool_calls.id -----
// --- included) — possible because tool_calls.id is deterministic UUIDv5. --

func TestRebuildProjections_ReproducesIdenticalRowsByChecksum(t *testing.T) {
	st, pool := newStore(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sessionID := "session-rebuild-checksum"
	buildRebuildFixture(t, st, base, sessionID)

	// Sanity: the fixture actually populated all four tables (a checksum
	// match over four empty tables would prove nothing).
	var sessions, turns, toolCalls, subagents int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&sessions))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM turns`).Scan(&turns))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM tool_calls`).Scan(&toolCalls))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM subagents`).Scan(&subagents))
	require.Equal(t, int64(1), sessions)
	require.Positive(t, turns)
	require.Equal(t, int64(2), toolCalls)
	require.Equal(t, int64(1), subagents)

	before := checksumProjections(t, pool)
	idsBefore := toolCallIDs(t, pool)
	require.Len(t, idsBefore, 2)

	require.NoError(t, st.RebuildProjections(context.Background(), base.Add(-time.Hour)))

	after := checksumProjections(t, pool)
	require.Equal(t, before, after, "RebuildProjections must reproduce byte-identical projection rows")
	require.Equal(t, idsBefore, toolCallIDs(t, pool), "tool_calls.id must be reproduced exactly (deterministic UUIDv5, SPEC §1.6)")

	// The rebuild must have actually run (not silently no-op'd): job_state
	// records a completed pass with its watermark cleared.
	var lastRunAt *time.Time
	var watermark *int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT last_run_at, watermark FROM job_state WHERE job = 'rebuild'`).Scan(&lastRunAt, &watermark))
	require.NotNil(t, lastRunAt)
	require.Nil(t, watermark, "a completed rebuild must clear its watermark")
}

// --- AC: resuming from a partial watermark completes correctly. -----------
//
// Simulated by forging the state an interrupted rebuild leaves behind: the
// four projection tables truncated and a job_state watermark parked at a
// real event's (ts, seq) partway through the timeline — exactly what
// RebuildProjections itself would have left had it crashed after committing
// that page. A resumed call must (a) NOT re-truncate (proven by a session
// whose only events are before the watermark staying absent afterwards —
// if the resume had instead restarted from fromTS, that early session would
// reappear) and (b) fully reconstruct everything after the watermark.
func TestRebuildProjections_ResumesFromPartialWatermark(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	earlySessionID := "session-rebuild-early"
	earlyEvent := mkEvent(t, earlySessionID, model.KindSessionStart, model.SourceHook, base,
		withAttrs(map[string]any{"cwd": "/x/early"}))
	_, err := st.WriteBatch(ctx, []model.Event{earlyEvent})
	require.NoError(t, err)

	lateSessionID := "session-rebuild-late"
	lateBase := base.Add(time.Hour)
	buildRebuildFixture(t, st, lateBase, lateSessionID)

	// The boundary: the last event seq/ts strictly before the late session's
	// fixture begins, so resuming from it skips the early session entirely
	// and replays the late session's fixture in full.
	var boundaryTS time.Time
	var boundarySeq int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT ts, seq FROM events WHERE session_id = $1 ORDER BY ts DESC, seq DESC LIMIT 1`, earlySessionID).
		Scan(&boundaryTS, &boundarySeq))

	// Forge the interrupted-rebuild state: truncate (as a real fresh start
	// would have already done) and park the watermark at the boundary.
	_, err = pool.Exec(ctx, `TRUNCATE tool_calls, subagents, turns, sessions RESTART IDENTITY`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO job_state (job, watermark, watermark_ts, last_run_at)
		VALUES ('rebuild', $1, $2, now())
		ON CONFLICT (job) DO UPDATE SET watermark = EXCLUDED.watermark, watermark_ts = EXCLUDED.watermark_ts`,
		boundarySeq, boundaryTS)
	require.NoError(t, err)

	// fromTS is deliberately far earlier than everything: if the resume
	// branch were broken and this fell back to a fresh start, the early
	// session would reappear.
	require.NoError(t, st.RebuildProjections(ctx, base.Add(-24*time.Hour)))

	var earlyExists, lateExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, earlySessionID).Scan(&earlyExists))
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, lateSessionID).Scan(&lateExists))
	require.False(t, earlyExists, "resuming from the watermark must NOT re-truncate-and-replay the pre-watermark session")
	require.True(t, lateExists, "the post-watermark session must be fully replayed")

	var toolCalls, subagents int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE session_id = $1`, lateSessionID).Scan(&toolCalls))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM subagents WHERE session_id = $1`, lateSessionID).Scan(&subagents))
	require.Equal(t, int64(2), toolCalls)
	require.Equal(t, int64(1), subagents)

	var watermark *int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT watermark FROM job_state WHERE job = 'rebuild'`).Scan(&watermark))
	require.Nil(t, watermark, "a completed resume must clear the watermark")
}

// --- AC (implicit correctness guard): RebuildProjections never touches ----
// --- rollups — SPEC §2.4's "rollups and projections are never deleted" ----
// --- applies to a rebuild exactly as it does to retention.

func TestRebuildProjections_NeverTouchesRollups(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	sessionID := "session-rebuild-rollups"
	buildRebuildFixture(t, st, base, sessionID)

	_, err := st.RunRollups(ctx, 1000)
	require.NoError(t, err)

	var before int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_hourly`).Scan(&before))
	require.Positive(t, before)

	require.NoError(t, st.RebuildProjections(ctx, base.Add(-time.Hour)))

	var after int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_hourly`).Scan(&after))
	require.Equal(t, before, after, "RebuildProjections must never touch rollup_hourly")
}
