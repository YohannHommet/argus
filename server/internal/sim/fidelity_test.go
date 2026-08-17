package sim

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/stretchr/testify/require"
)

// generateManySessions builds enough sessions (spanning the full project
// set, several turns, tool calls, and subagents each) that the low-
// probability draws this test cares about (an invented query_source,
// api_request/tool_result attribute shape, etc.) are virtually certain to
// appear at least once, without relying on a hand-picked seed that happens
// to roll a rare value on the first try.
func generateManySessions(t *testing.T, n int) []sessionResult {
	t.Helper()
	cfg := DefaultConfig()
	clock := NewClock(FixedEpoch)
	out := make([]sessionResult, n)
	for i := 0; i < n; i++ {
		project := projects[i%len(projects)]
		out[i] = generateSession(cfg, clock, i, 0, project)
	}
	return out
}

// TestFidelity_NoAgentIDOnAPIRequestOrToolResult is the ticket's B3 AC:
// "a test asserts no emitted OTel api_request/tool_result payload contains
// agent_id" (SPEC §1.9, live capture §3: "No agent_id / parent_agent_id /
// subagent attribute" on api_request; §1.5.1: OTel tool_result likewise
// never carries it). This is the load-bearing fidelity check — a
// regression here would make the demo lie about per-subagent cost
// attribution.
func TestFidelity_NoAgentIDOnAPIRequestOrToolResult(t *testing.T) {
	t.Parallel()

	sessions := generateManySessions(t, 40)
	checked := 0
	for _, s := range sessions {
		for _, e := range s.Logs {
			name := eventNameOf(e.Rec)
			if name != "api_request" && name != "tool_result" {
				continue
			}
			checked++
			for _, kv := range e.Rec.GetAttributes() {
				require.NotEqual(t, "agent_id", kv.GetKey(), "OTel %s must never carry agent_id (SPEC §1.9)", name)
				require.NotEqual(t, "parent_agent_id", kv.GetKey(), "OTel %s must never carry parent_agent_id (SPEC §1.9)", name)
			}
		}
	}
	require.Positive(t, checked, "test setup did not generate any api_request/tool_result records to check")
}

// TestFidelity_QuerySourceEscapesAnyArgusConstant is the ticket's AC: "a
// test asserts at least one emitted query_source value is outside any
// Argus constant" — proving query_source is genuinely unconstrained text
// (SPEC §0, §7.1's invented `a_future_query_source`), not a Go enum in
// disguise. "Any Argus constant" is operationalized as the closed set SPEC
// §1.5.1/telemetry-surfaces.md ever *documents* for this column
// (main|subagent|auxiliary) plus the two live-capture-observed values
// (sdk|generate_session_title) — invalidQuerySource must lie outside all
// of them.
func TestFidelity_QuerySourceEscapesAnyArgusConstant(t *testing.T) {
	t.Parallel()

	documented := map[string]bool{
		"main": true, "subagent": true, "auxiliary": true,
		"sdk": true, "generate_session_title": true,
	}

	sessions := generateManySessions(t, 200)
	found := false
	for _, s := range sessions {
		for _, e := range s.Logs {
			if eventNameOf(e.Rec) != "api_request" {
				continue
			}
			qs := attrString(e.Rec, "query_source")
			if qs == "" {
				continue
			}
			if !documented[qs] {
				found = true
				require.Equal(t, invalidQuerySource, qs, "the only undocumented value this generator draws should be the invented one")
			}
		}
	}
	require.True(t, found, "expected at least one query_source value outside the documented+observed set across %d sessions", len(sessions))
}

// TestFidelity_BothEventNameForms asserts the fidelity rule's second named
// consequence: every log record carries the prefixed body
// ("claude_code.<name>") and the unprefixed event.name attribute, matching
// the live capture (finding 4.1) and what eventname.go's ResolveEventName
// expects as input.
func TestFidelity_BothEventNameForms(t *testing.T) {
	t.Parallel()

	sessions := generateManySessions(t, 5)
	checked := 0
	for _, s := range sessions {
		for _, e := range s.Logs {
			checked++
			name := eventNameOf(e.Rec)
			require.NotEmpty(t, name)
			require.Equal(t, "claude_code."+name, bodyOf(e.Rec))
		}
	}
	require.Positive(t, checked)
}

// TestFidelity_ToolUseIDFlagsRoundTrip exercises the --tool-use-id-in-hooks
// / --tool-use-id-in-decision toggles the ticket names explicitly,
// confirming both directions actually change the wire output rather than
// being dead flags.
func TestFidelity_ToolUseIDFlagsRoundTrip(t *testing.T) {
	t.Parallel()

	clock := NewClock(FixedEpoch)

	withHookIDs := DefaultConfig()
	withHookIDs.ToolUseIDInHooks = true
	withoutDecisionID := DefaultConfig()
	withoutDecisionID.ToolUseIDInDecision = false

	rWith := generateSession(withHookIDs, clock, 0, 0, "argus")
	rWithoutDecision := generateSession(withoutDecisionID, clock, 0, 0, "argus")

	hookHasToolUseID := false
	for _, e := range rWith.Hooks {
		if _, ok := e.Payload["tool_use_id"]; ok {
			hookHasToolUseID = true
		}
	}
	require.True(t, hookHasToolUseID, "--tool-use-id-in-hooks=true should add tool_use_id to at least one hook payload")

	decisionHasToolUseID := false
	for _, e := range rWithoutDecision.Logs {
		if eventNameOf(e.Rec) != "tool_decision" {
			continue
		}
		if attrString(e.Rec, "tool_use_id") != "" {
			decisionHasToolUseID = true
		}
	}
	require.False(t, decisionHasToolUseID, "--tool-use-id-in-decision=false should omit tool_use_id from every tool_decision record")

	// The default (ToolUseIDInDecision: true, config.go's DefaultConfig) is
	// never otherwise exercised by this test: rWith only turns on the hooks
	// flag, and rWithoutDecision only turns the decision flag off. Without
	// this session, a regression where tool_decision never carries
	// tool_use_id at all would still pass both assertions above, since both
	// only look for its documented off states.
	rDefault := generateSession(DefaultConfig(), clock, 0, 0, "argus")
	defaultDecisionHasToolUseID := false
	for _, e := range rDefault.Logs {
		if eventNameOf(e.Rec) != "tool_decision" {
			continue
		}
		if attrString(e.Rec, "tool_use_id") != "" {
			defaultDecisionHasToolUseID = true
		}
	}
	require.True(t, defaultDecisionHasToolUseID, "the default ToolUseIDInDecision=true should include tool_use_id on at least one tool_decision record")
}

// eventNameOf/attrString/bodyOf are small OTLP accessor helpers local to
// this test file. This package's non-test code never reads back its own
// generated attributes (only builds them, otel_log_events.go); these
// mirror otlpattrs.go's decoding logic just enough for test assertions
// without importing internal/ingest/normalize into a non-test file.
func eventNameOf(rec *logspb.LogRecord) string {
	return attrString(rec, "event.name")
}

func attrString(rec *logspb.LogRecord, key string) string {
	for _, kv := range rec.GetAttributes() {
		if kv.GetKey() == key {
			if sv, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_StringValue); ok {
				return sv.StringValue
			}
		}
	}
	return ""
}

func bodyOf(rec *logspb.LogRecord) string {
	if sv, ok := rec.GetBody().GetValue().(*commonpb.AnyValue_StringValue); ok {
		return sv.StringValue
	}
	return ""
}
