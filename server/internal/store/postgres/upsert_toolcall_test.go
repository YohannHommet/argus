package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// --- fixture helpers -------------------------------------------------------

// otelFixture decodes a live-capture-derived OTLP fixture from
// internal/ingest/normalize/testdata/otel (shared with that package's own
// tests — SPEC hard rule: reuse existing capture-derived fixtures, don't
// invent new tool_decision/tool_result payloads) and normalizes it with a
// fixed clock safely after the fixture's own timestamps (2026-08-11) and
// within retention.
func otelFixtureEvents(t *testing.T, name string) []model.Event {
	t.Helper()
	path := filepath.Join("..", "..", "ingest", "normalize", "testdata", "otel", name)
	b, err := os.ReadFile(path) //nolint:gosec // test-only: name is always a literal from this file's own call sites
	require.NoError(t, err, "reading fixture %s", name)

	var data logspb.LogsData
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, &data))

	now := func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) }
	n := normalize.NewNormalizer(now, 30*24*time.Hour)
	events, rejections := n.FromOTLPLogs(&data)
	require.Empty(t, rejections, "fixture %s produced unexpected rejections", name)
	require.NotEmpty(t, events)
	// FromOTLPLogs does not assign Event.ID (that is the ingest pipeline's
	// job, upstream of normalize — P2-09 territory, not this ticket's);
	// these tests drive WriteBatch directly, so they must stamp a valid
	// uuid themselves, exactly like mkEvent does for hand-built fixtures.
	for i := range events {
		events[i].ID = nextID()
	}
	return events
}

// hookEvent builds one hook-sourced model.Event via the real
// FromHookPayload path (not a hand-built model.Event), so these tests
// exercise the same mapping P2-03 shipped. fields are merged into the
// common hook payload shape.
func hookEvent(t *testing.T, ts time.Time, hookEventName, sessionID string, fields map[string]any) model.Event {
	t.Helper()
	body := map[string]any{
		"hook_event_name": hookEventName,
		"session_id":      sessionID,
		"timestamp":       ts.UTC().Format(time.RFC3339Nano),
	}
	for k, v := range fields {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	now := func() time.Time { return ts }
	n := normalize.NewHookNormalizer(now, 30*24*time.Hour, false)
	events, err := n.FromHookPayload(raw)
	require.NoError(t, err)
	require.Len(t, events, 1)
	e := events[0]
	e.ID = nextID() // see otelFixtureEvents's comment: ID assignment is the ingest pipeline's job, not normalize's.
	return e
}

type toolCallRow struct {
	ID                              string
	SessionID                       string
	PromptID, ToolUseID             *string
	ToolName                        string
	Decision, DecisionSource        *string
	ToolSource, AgentID             *string
	StartedAt                       time.Time
	DecidedAt, EndedAt              *time.Time
	DurationMS, WaitMS              *int
	Success                         *bool
	ErrorType                       *string
	InputSizeBytes, ResultSizeBytes *int
	Correlation                     string
	EventCount                      int
}

func queryToolCall(t *testing.T, pool *pgxpool.Pool, id string) toolCallRow {
	t.Helper()
	var r toolCallRow
	var waitMS *int
	err := pool.QueryRow(context.Background(), `
		SELECT id, session_id, prompt_id, tool_use_id, tool_name, decision, decision_source, tool_source,
		       agent_id, started_at, decided_at, ended_at, duration_ms,
		       (EXTRACT(EPOCH FROM (decided_at - started_at)) * 1000)::int,
		       success, error_type, input_size_bytes, result_size_bytes, correlation, event_count
		FROM tool_calls WHERE id = $1`, id).Scan(
		&r.ID, &r.SessionID, &r.PromptID, &r.ToolUseID, &r.ToolName, &r.Decision, &r.DecisionSource, &r.ToolSource,
		&r.AgentID, &r.StartedAt, &r.DecidedAt, &r.EndedAt, &r.DurationMS,
		&waitMS,
		&r.Success, &r.ErrorType, &r.InputSizeBytes, &r.ResultSizeBytes, &r.Correlation, &r.EventCount,
	)
	require.NoError(t, err)
	r.WaitMS = waitMS
	return r
}

func toolCallCountBySession(t *testing.T, pool *pgxpool.Pool, sessionID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM tool_calls WHERE session_id = $1`, sessionID).Scan(&n))
	return n
}

func sessionToolCounters(t *testing.T, pool *pgxpool.Pool, sessionID string) (calls, rejects int) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT tool_call_count, tool_reject_count FROM sessions WHERE id = $1`, sessionID).Scan(&calls, &rejects))
	return calls, rejects
}

// --- AC: pre+decision+result all with tool_use_id -> one row; exact when a
// hook also matched, otel_only when not.

func TestWriteBatch_ToolCall_OtelOnlyWithoutHook(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-otel-only"
	toolUseID := "toolu_otelonly_1"
	promptID := "prompt-1"

	pre := mkEvent(t, sessionID, model.KindToolPre, model.SourceOTelLog, base,
		withPromptID(promptID), func(e *model.Event) { e.ToolName = ptrString("Read"); e.ToolUseID = &toolUseID })
	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(10*time.Millisecond),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Read")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("accept")
			e.DecisionSource = ptrString("config")
			e.ToolSource = ptrString("builtin")
		})
	result := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base.Add(20*time.Millisecond),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Read")
			e.ToolUseID = &toolUseID
			ok := true
			e.Success = &ok
			d := 5
			e.DurationMS = &d
		})

	_, err := st.WriteBatch(ctx, []model.Event{pre, decision, result})
	require.NoError(t, err)

	id := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.Equal(t, "otel_only", row.Correlation)
	require.Equal(t, ptrString("accept"), row.Decision)
	require.Equal(t, ptrString("config"), row.DecisionSource)
	require.True(t, row.StartedAt.Equal(base))
	require.NotNil(t, row.EndedAt)
	require.Equal(t, 3, row.EventCount)
	require.Equal(t, 1, toolCallCountBySession(t, pool, sessionID))
}

func TestWriteBatch_ToolCall_ExactWhenHookAlsoMatches(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-exact"
	toolUseID := "toolu_exact_1"
	promptID := "prompt-1"

	pre := mkEvent(t, sessionID, model.KindToolPre, model.SourceOTelLog, base,
		withPromptID(promptID), func(e *model.Event) { e.ToolName = ptrString("Edit"); e.ToolUseID = &toolUseID })
	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(10*time.Millisecond),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Edit")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("accept")
			e.DecisionSource = ptrString("config")
		})
	hookPre := hookEvent(t, base.Add(-5*time.Millisecond), "PreToolUse", sessionID, map[string]any{
		"prompt_id": promptID, "tool_name": "Edit", "tool_use_id": toolUseID,
	})

	_, err := st.WriteBatch(ctx, []model.Event{pre, decision, hookPre})
	require.NoError(t, err)

	id := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.Equal(t, "exact", row.Correlation, "a hook event that itself carries tool_use_id is exact-join evidence, not heuristic")
}

// --- AC: decision-provenance case from live capture's tool_decision
// resolves to correlation != heuristic (pinning the verified exact join).

func TestWriteBatch_ToolCall_DecisionProvenanceFromLiveCapture(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base.Add(-24*time.Hour), base.Add(24*time.Hour))

	events := otelFixtureEvents(t, "tool_decision.json")
	require.Len(t, events, 1)
	e := events[0]
	require.Equal(t, model.KindToolDecision, e.Kind)

	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)

	id := normalize.ToolCallID(e.SessionID, e.ToolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.NotEqual(t, "heuristic", row.Correlation, "decision provenance must never depend on the heuristic (SPEC §1.6)")
	require.Equal(t, ptrString("accept"), row.Decision)
	require.Equal(t, ptrString("config"), row.DecisionSource)
	require.Equal(t, ptrString("builtin"), row.ToolSource)
}

// --- AC: input_size_bytes/result_size_bytes populated from a live-capture
// tool_result fixture (review m2).

func TestWriteBatch_ToolCall_SizeBytesFromLiveCaptureFixture(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base.Add(-24*time.Hour), base.Add(24*time.Hour))

	events := otelFixtureEvents(t, "tool_result.json")
	require.Len(t, events, 1)
	e := events[0]
	require.Equal(t, model.KindToolResult, e.Kind)

	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)

	id := normalize.ToolCallID(e.SessionID, e.ToolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.NotNil(t, row.InputSizeBytes)
	require.Equal(t, 80, *row.InputSizeBytes, "attrs->>'tool_input_size_bytes' from the fixture")
	require.Nil(t, row.ResultSizeBytes, "the fixture's tool_result record has no tool_result_size_bytes key")
}

// --- AC: hooks-only without tool_use_id -> hook_only.

func TestWriteBatch_ToolCall_HooksOnlyWithoutToolUseID(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-hookonly"
	promptID := "prompt-1"

	pre := hookEvent(t, base, "PreToolUse", sessionID, map[string]any{"prompt_id": promptID, "tool_name": "Bash"})
	post := hookEvent(t, base.Add(200*time.Millisecond), "PostToolUse", sessionID, map[string]any{"prompt_id": promptID, "tool_name": "Bash"})

	_, err := st.WriteBatch(ctx, []model.Event{pre})
	require.NoError(t, err)
	_, err = st.WriteBatch(ctx, []model.Event{post})
	require.NoError(t, err)

	require.Equal(t, 1, toolCallCountBySession(t, pool, sessionID), "the pre+post pair must stitch into ONE hook_only row, not two")

	var correlation string
	var successVal *bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT correlation, success FROM tool_calls WHERE session_id=$1`, sessionID).Scan(&correlation, &successVal))
	require.Equal(t, "hook_only", correlation)
	require.NotNil(t, successVal)
	require.True(t, *successVal)
}

// --- AC: a hook without tool_use_id alongside three concurrent Edit calls
// matches exactly one call each, never two (one-to-one).

func TestWriteBatch_ToolCall_HeuristicOneToOneAcrossConcurrentCalls(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-concurrent"
	promptID := "prompt-1"

	var events []model.Event
	for i := 0; i < 3; i++ {
		id := "toolu_edit_" + string(rune('a'+i))
		ts := base.Add(time.Duration(i) * time.Second)
		events = append(events, mkEvent(t, sessionID, model.KindToolPre, model.SourceOTelLog, ts,
			withPromptID(promptID), func(e *model.Event) { tn := "Edit"; e.ToolName = &tn; tid := id; e.ToolUseID = &tid }))
	}
	// One hook PreToolUse per concurrent call, each lacking tool_use_id
	// (SPEC §1.5.2's "[unverified-safe]" case), landing close in time to
	// its matching OTel call.
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i)*time.Second + 50*time.Millisecond)
		events = append(events, hookEvent(t, ts, "PreToolUse", sessionID, map[string]any{"prompt_id": promptID, "tool_name": "Edit"}))
	}

	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)

	require.Equal(t, 3, toolCallCountBySession(t, pool, sessionID), "one-to-one: three OTel calls + three keyless hooks must yield exactly three rows, never merged or duplicated")

	rows, err := pool.Query(ctx, `SELECT correlation FROM tool_calls WHERE session_id=$1`, sessionID)
	require.NoError(t, err)
	defer rows.Close()
	var correlations []string
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		correlations = append(correlations, c)
	}
	require.Len(t, correlations, 3)
	for _, c := range correlations {
		require.Equal(t, "heuristic", c, "each concurrent call's hook must be heuristically matched to its own OTel call")
	}
}

// --- AC: a hook arriving 5 minutes late does not match (hook_only row).

func TestWriteBatch_ToolCall_LateHookDoesNotMatch(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base.Add(10*time.Minute))

	sessionID := "session-tc-late-hook"
	toolUseID := "toolu_late_1"
	promptID := "prompt-1"

	otel := mkEvent(t, sessionID, model.KindToolPre, model.SourceOTelLog, base,
		withPromptID(promptID), func(e *model.Event) { e.ToolName = ptrString("Read"); e.ToolUseID = &toolUseID })
	_, err := st.WriteBatch(ctx, []model.Event{otel})
	require.NoError(t, err)

	lateHook := hookEvent(t, base.Add(5*time.Minute), "PreToolUse", sessionID, map[string]any{"prompt_id": promptID, "tool_name": "Read"})
	_, err = st.WriteBatch(ctx, []model.Event{lateHook})
	require.NoError(t, err)

	require.Equal(t, 2, toolCallCountBySession(t, pool, sessionID), "outside the 60s window, the late hook must create its own row rather than matching")

	var correlations []string
	rows, err := pool.Query(ctx, `SELECT correlation FROM tool_calls WHERE session_id=$1 ORDER BY started_at`, sessionID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		correlations = append(correlations, c)
	}
	require.Equal(t, []string{"otel_only", "hook_only"}, correlations)
}

// --- AC: tool_decision source=user_reject overrides a decision_source
// previously written by tool_result.

func TestWriteBatch_ToolCall_ToolDecisionOverridesToolResultDecisionSource(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-precedence"
	toolUseID := "toolu_precedence_1"
	promptID := "prompt-1"

	result := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base,
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Edit")
			e.ToolUseID = &toolUseID
			e.DecisionSource = ptrString("config") // tool_result's own decision_source (SPEC §1.5.1)
		})
	_, err := st.WriteBatch(ctx, []model.Event{result})
	require.NoError(t, err)

	id := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.Equal(t, ptrString("config"), row.DecisionSource)

	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(time.Second),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Edit")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("reject")
			e.DecisionSource = ptrString("user_reject")
		})
	_, err = st.WriteBatch(ctx, []model.Event{decision})
	require.NoError(t, err)

	row = queryToolCall(t, pool, id)
	require.Equal(t, ptrString("user_reject"), row.DecisionSource, "tool_decision must win over a previously-written tool_result decision_source (SPEC §1.5.3)")
	require.Equal(t, ptrString("reject"), row.Decision)
}

// --- AC: an unrecognised source value is stored verbatim (SPEC §0).

func TestWriteBatch_ToolCall_UnrecognisedDecisionSourceStoredVerbatim(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-unconstrained"
	toolUseID := "toolu_unconstrained_1"

	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base,
		func(e *model.Event) {
			e.ToolName = ptrString("Edit")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("accept")
			e.DecisionSource = ptrString("a_totally_novel_vendor_value")
		})
	_, err := st.WriteBatch(ctx, []model.Event{decision})
	require.NoError(t, err)

	id := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.Equal(t, ptrString("a_totally_novel_vendor_value"), row.DecisionSource, "SPEC §0: unconstrained text is stored verbatim, never rejected or coerced")
}

// --- AC: wait_ms computed from PreToolUse -> tool_decision.

func TestWriteBatch_ToolCall_WaitMSFromPreToDecision(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-waitms"
	toolUseID := "toolu_waitms_1"
	promptID := "prompt-1"

	pre := hookEvent(t, base, "PreToolUse", sessionID, map[string]any{"prompt_id": promptID, "tool_name": "Write", "tool_use_id": toolUseID})
	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(3*time.Second),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Write")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("accept")
			e.DecisionSource = ptrString("config")
		})

	_, err := st.WriteBatch(ctx, []model.Event{pre, decision})
	require.NoError(t, err)

	id := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.NotNil(t, row.WaitMS)
	require.InDelta(t, 3000, *row.WaitMS, 5, "wait_ms = decided_at - started_at")
}

// --- AC: the same input replayed twice produces the same id (determinism,
// P3-10's rebuild test depends on this).

func TestWriteBatch_ToolCall_DeterministicIDAcrossReplays(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-determinism"
	toolUseID := "toolu_determinism_1"

	build := func() model.Event {
		return mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base,
			func(e *model.Event) { e.ToolName = ptrString("Read"); e.ToolUseID = &toolUseID }, withID(nextID()))
	}

	first := build()
	_, err := st.WriteBatch(ctx, []model.Event{first})
	require.NoError(t, err)

	wantID := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, wantID)
	require.Equal(t, wantID, row.ID)

	// A second, independently-built event describing the same logical tool
	// call (same session_id/tool_use_id) must resolve to the identical id.
	second := build()
	second.DedupKey = first.DedupKey + ":replay" // force it past dedup so the upsert path runs again
	_, err = st.WriteBatch(ctx, []model.Event{second})
	require.NoError(t, err)

	row2 := queryToolCall(t, pool, wantID)
	require.Equal(t, wantID, row2.ID, "replaying the same (session, tool_use_id) must resolve to the same id")
	require.Equal(t, 1, toolCallCountBySession(t, pool, sessionID))
}

// --- AC: session/turn tool_call_count and tool_reject_count maintenance,
// redelivery-safe (lead note 4: recomputed from the projection, never
// incremented per event).

func TestWriteBatch_ToolCall_SessionAndTurnCountersRedeliverySafe(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-counters"
	promptID := "prompt-1"
	toolUseID1 := "toolu_counters_1"
	toolUseID2 := "toolu_counters_2"

	accept := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base,
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Read")
			e.ToolUseID = &toolUseID1
			e.Decision = ptrString("accept")
		})
	reject := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(time.Second),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Edit")
			e.ToolUseID = &toolUseID2
			e.Decision = ptrString("reject")
		})

	_, err := st.WriteBatch(ctx, []model.Event{accept, reject})
	require.NoError(t, err)

	calls, rejects := sessionToolCounters(t, pool, sessionID)
	require.Equal(t, 2, calls)
	require.Equal(t, 1, rejects)

	var turnCalls, turnRejects int
	require.NoError(t, pool.QueryRow(ctx, `SELECT tool_call_count, tool_reject_count FROM turns WHERE session_id=$1 AND prompt_id=$2`, sessionID, promptID).Scan(&turnCalls, &turnRejects))
	require.Equal(t, 2, turnCalls)
	require.Equal(t, 1, turnRejects)

	// Redeliver the exact same reject event (same dedup_key): counts must
	// not double, because they are recomputed from tool_calls, not
	// incremented per candidate event.
	redelivered := reject
	_, err = st.WriteBatch(ctx, []model.Event{redelivered})
	require.NoError(t, err)

	calls, rejects = sessionToolCounters(t, pool, sessionID)
	require.Equal(t, 2, calls, "a deduped redelivery must not double-count tool_call_count")
	require.Equal(t, 1, rejects, "a deduped redelivery must not double-count tool_reject_count")
}
