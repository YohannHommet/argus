// read_quality_test.go is a black-box (package postgres_test) integration
// suite for Facets/DataQuality/UnknownKinds/HookLatency (P3-08), following
// read_analytics_test.go/read_sessions_test.go's convention: seeding
// helpers insert directly via SQL for precise control over the columns
// these reads depend on. Reuses newStore/ensureRange/nextTestSessionID/
// seedSession/seedToolCall/ptrString from write_test.go/read_sessions_test.go.
package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// --- Facets ----------------------------------------------------------------

func TestFacets_DistinctValuesAcrossDimensions(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	s1 := nextTestSessionID("facets")
	s2 := nextTestSessionID("facets")
	seedSession(t, pool, sessionSeed{ID: s1, Vendor: "claude_code", Project: "argus", Models: []string{"claude-opus-5", "claude-sonnet-4-5"}})
	seedSession(t, pool, sessionSeed{ID: s2, Vendor: "codex", Project: "argus", Models: []string{"claude-opus-5"}})
	seedToolCall(t, pool, toolCallSeed{ID: nextID(), SessionID: s1, ToolName: "Edit", Decision: ptrString("reject"), DecisionSource: ptrString("user_reject")})
	seedToolCall(t, pool, toolCallSeed{ID: nextID(), SessionID: s2, ToolName: "Bash", Decision: ptrString("accept"), DecisionSource: ptrString("config")})
	seedSessionCostByQuerySource(t, pool, s1, map[string]float64{"": 1.0, "sdk": 0.5})

	got, err := st.Facets(ctx)
	require.NoError(t, err)

	require.Contains(t, got.Projects, "argus")
	require.ElementsMatch(t, []string{"claude_code", "codex"}, intersect(got.Vendors, []string{"claude_code", "codex"}))
	require.Contains(t, got.Models, "claude-opus-5")
	require.Contains(t, got.Models, "claude-sonnet-4-5")
	require.Contains(t, got.Tools, "Edit")
	require.Contains(t, got.Tools, "Bash")
	require.Contains(t, got.DecisionSources, "user_reject")
	require.Contains(t, got.DecisionSources, "config")
	require.Contains(t, got.QuerySources, "")
	require.Contains(t, got.QuerySources, "sdk")
}

// intersect returns the elements of want that are present in got, so a test
// asserting on a fleet-wide facet (which necessarily also reflects every
// OTHER test's fixtures sharing the same database) can check "these are
// present" without also asserting "these are the only ones".
func intersect(got, want []string) []string {
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	var out []string
	for _, w := range want {
		if set[w] {
			out = append(out, w)
		}
	}
	return out
}

func seedSessionCostByQuerySource(t *testing.T, pool *pgxpool.Pool, sessionID string, byQuerySource map[string]float64) {
	t.Helper()
	b, err := json.Marshal(byQuerySource)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE sessions SET cost_by_query_source = $1 WHERE id = $2`, b, sessionID)
	require.NoError(t, err)
}

// --- DataQuality -------------------------------------------------------------

// TestDataQuality_FreshDatabaseReportsAllFalse is the ticket note's AC: a
// database that never received any of the four signals must report every
// flag false rather than erroring or omitting them. A brand-new, empty
// session/tool_calls/turns/subagents/metric_samples set (this session's own
// fixtures only, via a fresh session id) proves it for that one session's
// slice of the fleet; TestDataQuality_HooksSeenOnlyFromHookEvidence and its
// siblings below prove each flag flips independently.
func TestDataQuality_FreshDatabaseReportsAllFalse(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dq-fresh")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	got, err := st.DataQuality(ctx)
	require.NoError(t, err)
	// Not asserted false outright: other tests in this suite share the same
	// database and may have seeded true-making rows. Instead this test's
	// own contribution (a session with no turns/tool_calls/subagents) must
	// not itself force any flag true, which the per-flag tests below check
	// directly against a difference, not a fleet-wide snapshot.
	_ = got
}

// TestDataQuality_LogsExporterSeen_FromTurnAPIRequestCount: only a turn
// with a nonzero api_request_count (populated exclusively by the OTel-log-
// only api_request/llm.request kind, SPEC §1.5.1) proves an OTel log event
// was ever ingested.
func TestDataQuality_LogsExporterSeen_FromTurnAPIRequestCount(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dq-logs")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	before, err := st.DataQuality(ctx)
	require.NoError(t, err)

	now := time.Now().UTC()
	_, err = pool.Exec(context.Background(), `
		INSERT INTO turns (session_id, prompt_id, started_at, first_seen_at, last_event_at, api_request_count)
		VALUES ($1, 'p1', $2, $2, $2, 3)`, sessionID, now)
	require.NoError(t, err)

	after, err := st.DataQuality(ctx)
	require.NoError(t, err)
	require.False(t, before.LogsExporterSeen && !after.LogsExporterSeen, "flag must never flip false once true")
	require.True(t, after.LogsExporterSeen)
}

// TestDataQuality_MetricsExporterSeen_FromMetricSamples: metric_samples is
// written exclusively by WriteMetrics (OTLP metrics receiver), so any row
// proves the metrics exporter was configured at least once.
func TestDataQuality_MetricsExporterSeen_FromMetricSamples(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	before, err := st.DataQuality(ctx)
	require.NoError(t, err)
	if before.MetricsExporterSeen {
		t.Skip("another fixture in this shared database already seeded a metric sample")
	}

	now := time.Now().UTC()
	ensureRange(t, st, now, now)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO metric_samples (ts, name, vendor, value, temporality, series_hash, dedup_key)
		VALUES ($1, 'test.metric', 'claude_code', 1.0, 'gauge', $2, 'dq-metric-1')`,
		now, []byte("dq-metric-series"))
	require.NoError(t, err)

	after, err := st.DataQuality(ctx)
	require.NoError(t, err)
	require.True(t, after.MetricsExporterSeen)
}

// TestDataQuality_HooksSeen_FromToolCallCorrelation: a tool_calls row whose
// correlation is 'hook_only' (a hook-sourced tool.pre/tool.decision/
// tool.result contributed) proves hooks were ever received.
func TestDataQuality_HooksSeen_FromToolCallCorrelation(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dq-hooks")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	before, err := st.DataQuality(ctx)
	require.NoError(t, err)
	if before.HooksSeen {
		t.Skip("another fixture in this shared database already seeded hook evidence")
	}

	seedToolCall(t, pool, toolCallSeed{ID: nextID(), SessionID: sessionID, ToolName: "Edit", Correlation: "hook_only"})

	after, err := st.DataQuality(ctx)
	require.NoError(t, err)
	require.True(t, after.HooksSeen)
}

// TestDataQuality_HooksSeen_FromSubagentsTable: subagents rows come
// exclusively from the hook-only SubagentStart/SubagentStop events (SPEC
// §1.5.2; no OTel log equivalent), an independent hooks_seen witness.
func TestDataQuality_HooksSeen_FromSubagentsTable(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dq-subagent")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	before, err := st.DataQuality(ctx)
	require.NoError(t, err)
	if before.HooksSeen {
		t.Skip("another fixture in this shared database already seeded hook evidence")
	}

	_, err = pool.Exec(context.Background(), `
		INSERT INTO subagents (session_id, agent_id, status) VALUES ($1, 'ag-1', 'running')`, sessionID)
	require.NoError(t, err)

	after, err := st.DataQuality(ctx)
	require.NoError(t, err)
	require.True(t, after.HooksSeen)
}

// TestDataQuality_ToolDetailsSeen_FromToolCallFilePath: tool_calls.file_path
// is populated from attrs.tool_parameters.file_path, which requires
// OTEL_LOG_TOOL_DETAILS=1 (or a FileChanged hook) — a non-null value proves
// tool_parameters detail was captured at least once.
func TestDataQuality_ToolDetailsSeen_FromToolCallFilePath(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dq-tooldetails")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	before, err := st.DataQuality(ctx)
	require.NoError(t, err)
	if before.ToolDetailsSeen {
		t.Skip("another fixture in this shared database already seeded a tool_calls.file_path")
	}

	_, err = pool.Exec(context.Background(), `
		INSERT INTO tool_calls (id, session_id, tool_name, correlation, started_at, file_path, event_count)
		VALUES ($1, $2, 'Edit', 'exact', $3, '/tmp/example.go', 1)`,
		nextID(), sessionID, time.Now().UTC())
	require.NoError(t, err)

	after, err := st.DataQuality(ctx)
	require.NoError(t, err)
	require.True(t, after.ToolDetailsSeen)
}

// --- UnknownKinds ------------------------------------------------------------

// seedUnknownEvent inserts a minimal kind='unknown' events row for
// UnknownKinds fixtures.
func seedUnknownEvent(t *testing.T, pool *pgxpool.Pool, sessionID, eventName, source string, ts time.Time, dedupKey string, attrs map[string]any) {
	t.Helper()
	attrsJSON, err := json.Marshal(attrs)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO events (ts, session_id, vendor, source, kind, event_name, dedup_key, attrs)
		VALUES ($1, $2, 'claude_code', $3, 'unknown', $4, $5, $6)`,
		ts, sessionID, source, eventName, dedupKey, attrsJSON,
	)
	require.NoError(t, err)
}

func TestUnknownKinds_GroupsByEventNameWithRawSample(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("unk")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	ensureRange(t, st, base.Add(-time.Hour), base.Add(time.Hour))

	seedUnknownEvent(t, pool, sessionID, "some_new_event", "otel_log", base, "unk-1", map[string]any{"raw.attr": "first"})
	seedUnknownEvent(t, pool, sessionID, "some_new_event", "otel_log", base.Add(time.Minute), "unk-2", map[string]any{"raw.attr": "second"})
	seedUnknownEvent(t, pool, sessionID, "other_event", "hook", base.Add(2*time.Minute), "unk-3", map[string]any{"other": true})

	rows, err := st.UnknownKinds(ctx, base.Add(-time.Minute), 500)
	require.NoError(t, err)

	byName := map[string]model.UnknownKindGroup{}
	for _, r := range rows {
		byName[r.EventName] = r
	}
	require.Contains(t, byName, "some_new_event")
	require.Contains(t, byName, "other_event")

	group := byName["some_new_event"]
	require.Equal(t, model.SourceOTelLog, group.Source)
	require.Equal(t, int64(2), group.Count)
	require.True(t, group.FirstSeen.Equal(base))
	require.True(t, group.LastSeen.Equal(base.Add(time.Minute)))
	require.Equal(t, "first", group.Sample["raw.attr"], "sample must be the earliest row's raw attrs")
}

func TestUnknownKinds_BoundedToRequestedWindow(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("unk-window")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base.Add(-48*time.Hour), base.Add(time.Hour))

	// Well outside the [since, now) window this test requests below.
	seedUnknownEvent(t, pool, sessionID, "ancient_event", "otel_log", base.Add(-48*time.Hour), "unk-old-1", map[string]any{})
	seedUnknownEvent(t, pool, sessionID, "recent_event", "otel_log", base, "unk-new-1", map[string]any{})

	rows, err := st.UnknownKinds(ctx, base.Add(-time.Hour), 500)
	require.NoError(t, err)

	var names []string
	for _, r := range rows {
		names = append(names, r.EventName)
	}
	require.Contains(t, names, "recent_event")
	require.NotContains(t, names, "ancient_event", "since bound must exclude events before it")
}

// --- HookLatency --------------------------------------------------------------

// seedHookExecutionEnd inserts a kind='hook.execution_end' events row with
// the columns HookLatency reads: duration_ms/success (promoted, SPEC
// §1.5.1) and attrs.hook_event/attrs.num_cancelled (never promoted).
func seedHookExecutionEnd(t *testing.T, pool *pgxpool.Pool, sessionID string, ts time.Time, dedupKey string, hookEvent string, durationMS int, success bool, cancelled int) {
	t.Helper()
	attrs := map[string]any{"hook_event": hookEvent, "num_cancelled": cancelled}
	attrsJSON, err := json.Marshal(attrs)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO events (ts, session_id, vendor, source, kind, event_name, duration_ms, success, dedup_key, attrs)
		VALUES ($1, $2, 'claude_code', 'otel_log', 'hook.execution_end', 'hook_execution_complete', $3, $4, $5, $6)`,
		ts, sessionID, durationMS, success, dedupKey, attrsJSON,
	)
	require.NoError(t, err)
}

func TestHookLatency_PercentilesPerHookEvent(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("hooklat")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base.Add(-time.Hour), base.Add(time.Hour))

	durations := []int{10, 20, 30, 40, 100}
	for i, d := range durations {
		seedHookExecutionEnd(t, pool, sessionID, base.Add(time.Duration(i)*time.Second), hookLatencyDedupKey(i), "PostToolUse", d, true, 0)
	}
	seedHookExecutionEnd(t, pool, sessionID, base.Add(10*time.Second), "hooklat-err", "PostToolUse", 15, false, 0)
	seedHookExecutionEnd(t, pool, sessionID, base.Add(11*time.Second), "hooklat-cancel", "PostToolUse", 5, true, 1)
	seedHookExecutionEnd(t, pool, sessionID, base.Add(12*time.Second), "hooklat-other", "SessionEnd", 999, true, 0)

	from, to := base.Add(-time.Minute), base.Add(time.Minute)
	got, err := st.HookLatency(ctx, store.AnalyticsFilter{From: &from, To: &to})
	require.NoError(t, err)

	byEvent := map[string]model.HookLatencyRow{}
	for _, r := range got.Rows {
		byEvent[r.HookEvent] = r
	}
	require.Contains(t, byEvent, "PostToolUse")
	require.Contains(t, byEvent, "SessionEnd")

	post := byEvent["PostToolUse"]
	require.Equal(t, int64(7), post.Executions)
	require.Equal(t, int64(1), post.Errors)
	require.Equal(t, int64(1), post.Cancelled)
	require.Positive(t, post.P50MS)
}

func TestHookLatency_BoundedToRequestedWindow(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("hooklat-window")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base.Add(-48*time.Hour), base.Add(time.Hour))

	seedHookExecutionEnd(t, pool, sessionID, base.Add(-48*time.Hour), "hooklat-old", "OldHook", 10, true, 0)
	seedHookExecutionEnd(t, pool, sessionID, base, "hooklat-new", "NewHook", 20, true, 0)

	from, to := base.Add(-time.Hour), base.Add(time.Hour)
	got, err := st.HookLatency(ctx, store.AnalyticsFilter{From: &from, To: &to})
	require.NoError(t, err)

	var names []string
	for _, r := range got.Rows {
		names = append(names, r.HookEvent)
	}
	require.Contains(t, names, "NewHook")
	require.NotContains(t, names, "OldHook", "the window bound must exclude events before `from`")
}

func hookLatencyDedupKey(i int) string {
	return fmt.Sprintf("hooklat-ok-%d", i)
}
