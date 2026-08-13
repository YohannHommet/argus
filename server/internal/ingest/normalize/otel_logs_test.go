package normalize

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// fixedNow freezes the clock every test uses so model.ClampTimestamp and the
// skew computation are deterministic (ticket P2-02 rule 8): fixture
// timestamps are all well inside [fixedNow-retention, fixedNow+1h].
var fixedNow = time.Date(2026, 8, 11, 22, 0, 0, 0, time.UTC)

const testRetention = 30 * 24 * time.Hour

func newTestNormalizer() *Normalizer {
	return NewNormalizer(func() time.Time { return fixedNow }, testRetention)
}

// loadFixture decodes a testdata/otel/<name>.json file (protojson encoding
// of logspb.LogsData) into the real OTLP type FromOTLPLogs decodes in
// production, exercising the same DiscardUnknown-tolerant path the SPEC
// §3.4 receiver uses.
func loadFixture(t *testing.T, name string) *logspb.LogsData {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "otel", name)) //nolint:gosec // test-only: name is always a literal from this file's own test table, never external input
	require.NoError(t, err, "reading fixture %s", name)

	var data logspb.LogsData
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	require.NoError(t, opts.Unmarshal(b, &data), "unmarshaling fixture %s", name)
	return &data
}

// normalizeOne loads a single-record fixture, runs FromOTLPLogs, and
// asserts it produced exactly one event and no rejections — the common
// shape for every §1.5.1 mapping-row fixture.
func normalizeOne(t *testing.T, fixture string) model.Event {
	t.Helper()
	data := loadFixture(t, fixture)
	n := newTestNormalizer()
	events, rejections := n.FromOTLPLogs(data)
	require.Empty(t, rejections, "fixture %s produced unexpected rejections", fixture)
	require.Len(t, events, 1, "fixture %s did not produce exactly one event", fixture)
	return events[0]
}

func strp(s string) *string { return &s }

// TestFromOTLPLogs_MappingTableRows asserts one case per row of the SPEC
// §1.5.1 mapping table (18 event names), each checking every column that
// row promotes, plus the `unknown` fallback row.
func TestFromOTLPLogs_MappingTableRows(t *testing.T) {
	t.Parallel()

	t.Run("user_prompt", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "user_prompt.json")
		require.Equal(t, model.KindTurnStart, e.Kind)
		require.Equal(t, "user_prompt", e.EventName)
		require.Equal(t, "0d7f3a8f-0ae3-4d5b-b256-70394e9e7a56", e.SessionID)
		require.Equal(t, strp("ae79adf6-3ad3-4e51-976d-c0605c46791d"), e.PromptID)
		// user_prompt promotes no other columns (SPEC §1.5.1: "prompt_length,
		// message.uuid stay in attrs").
		require.Nil(t, e.MessageUUID)
		v, ok := e.Attrs["message.uuid"]
		require.True(t, ok)
		require.Equal(t, "0ba46801-9c3a-4bdb-95c7-9cb36279a2f1", v)
	})

	t.Run("assistant_response", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "assistant_response.json")
		require.Equal(t, model.KindAssistantMessage, e.Kind)
		require.Equal(t, "assistant_response", e.EventName)
		require.Equal(t, strp("5e209ea9-59d5-402a-a090-89b3cd2928a6"), e.MessageUUID)
	})

	t.Run("api_request", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "api_request_sdk.json")
		require.Equal(t, model.KindLLMRequest, e.Kind)
		require.Equal(t, "api_request", e.EventName)
		require.Equal(t, strp("claude-haiku-4-5-20251001"), e.Model)
		require.Equal(t, int64(10), *e.InputTokens)
		require.Equal(t, int64(394), *e.OutputTokens)
		require.Equal(t, int64(19634), *e.CacheReadTokens)
		require.Equal(t, int64(5268), *e.CacheCreationTokens)
		require.Equal(t, 4607, *e.DurationMS)
		require.InDelta(t, 0.014479, *e.CostUSD, 1e-9, "cost_usd_micros/1e6 must be preferred over the less precise cost_usd")
		require.Equal(t, strp("reported"), e.CostSource)
		require.Equal(t, strp("req_011CdwhkADsdZ7m2Sp9cCwoY"), e.RequestID)
		require.Equal(t, strp("sdk"), e.QuerySource)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success)
		require.Nil(t, e.AgentID, "OTel log events never carry agent_id (SPEC §1.9)")
	})

	t.Run("api_error", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "api_error.json")
		require.Equal(t, model.KindLLMError, e.Kind)
		require.Equal(t, strp("claude-haiku-4-5-20251001"), e.Model)
		require.Equal(t, 842, *e.DurationMS)
		require.Equal(t, strp("529"), e.ErrorType, "falls back to attrs.status_code, stringified, when error_type is absent")
		require.Equal(t, strp("req_synthetic_api_error"), e.RequestID)
		require.Equal(t, strp("sdk"), e.QuerySource)
		require.False(t, *e.Success)
	})

	t.Run("api_refusal", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "api_refusal.json")
		require.Equal(t, model.KindLLMRefusal, e.Kind)
		require.Equal(t, strp("claude-haiku-4-5-20251001"), e.Model)
		require.Equal(t, strp("policy"), e.ErrorType)
		require.False(t, *e.Success)
	})

	t.Run("api_request_body", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "api_request_body.json")
		require.Equal(t, model.KindLLMRequestBody, e.Kind)
		// Raw-only: no column promotion.
		require.Nil(t, e.Model)
		require.Nil(t, e.Success)
	})

	t.Run("api_response_body", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "api_response_body.json")
		require.Equal(t, model.KindLLMResponseBody, e.Kind)
		require.Nil(t, e.Model)
		require.Nil(t, e.Success)
	})

	t.Run("tool_result", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "tool_result.json")
		require.Equal(t, model.KindToolResult, e.Kind)
		require.Equal(t, strp("Read"), e.ToolName)
		require.Equal(t, strp("toolu_01Ub3xEggLhfVwt11qvDQRhX"), e.ToolUseID)
		require.NotNil(t, e.Success)
		require.False(t, *e.Success, "success is emitted as the OTel string \"false\" in the capture; must still coerce")
		require.Equal(t, 12, *e.DurationMS)
		require.Equal(t, strp("TelemetrySafeError"), e.ErrorType)
		require.Equal(t, strp("/workspace/hello.txt"), e.FilePath, "file_path = attrs.tool_parameters.file_path")
		// attrs.error is kept too (verbatim), alongside the promoted error_type.
		errAttr, ok := e.Attrs["error"]
		require.True(t, ok)
		require.Contains(t, errAttr, "File does not exist")
	})

	t.Run("tool_decision", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "tool_decision.json")
		require.Equal(t, model.KindToolDecision, e.Kind)
		require.Equal(t, strp("Read"), e.ToolName)
		require.Equal(t, strp("toolu_01Ub3xEggLhfVwt11qvDQRhX"), e.ToolUseID, "M10: tool_decision must carry tool_use_id")
		require.Equal(t, strp("accept"), e.Decision)
		require.Equal(t, strp("config"), e.DecisionSource)
		require.Equal(t, strp("builtin"), e.ToolSource)
	})

	t.Run("permission_mode_changed", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "permission_mode_changed.json")
		require.Equal(t, model.KindPermissionModeChanged, e.Kind)
		require.Equal(t, strp("acceptEdits"), e.PermissionMode)
		fromMode, ok := e.Attrs["from_mode"]
		require.True(t, ok)
		require.Equal(t, "default", fromMode)
	})

	t.Run("hook_registered", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "hook_registered.json")
		require.Equal(t, model.KindHookRegistered, e.Kind)
		require.Nil(t, e.DurationMS)
		require.Nil(t, e.Success)
	})

	t.Run("hook_execution_start", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "hook_execution_start.json")
		require.Equal(t, model.KindHookExecutionStart, e.Kind)
		require.Nil(t, e.DurationMS)
		require.Nil(t, e.Success)
	})

	t.Run("hook_execution_complete", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "hook_execution_complete.json")
		require.Equal(t, model.KindHookExecutionEnd, e.Kind)
		require.Equal(t, 52, *e.DurationMS, "total_duration_ms → duration_ms")
		require.NotNil(t, e.Success)
		require.True(t, *e.Success, "num_non_blocking_error=0 AND num_cancelled=0 => success")
	})

	t.Run("auth", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "auth.json")
		require.Equal(t, model.KindAgentAuth, e.Kind)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success)
	})

	t.Run("mcp_server_connection", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "mcp_server_connection.json")
		require.Equal(t, model.KindMCPConnection, e.Kind)
		// This fixture has no `success` attribute (the capture's mcp
		// events use `status`, not `success`) — promoted Success stays nil,
		// while the raw `status` value survives in attrs.
		require.Nil(t, e.Success)
		status, ok := e.Attrs["status"]
		require.True(t, ok)
		require.Equal(t, "connected", status)
	})

	t.Run("internal_error", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "internal_error.json")
		require.Equal(t, model.KindAgentInternalError, e.Kind)
		require.Equal(t, strp("UnhandledPromiseRejection"), e.ErrorType)
		require.NotNil(t, e.Success)
		require.False(t, *e.Success)
	})

	t.Run("plugin_installed", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "plugin_installed.json")
		require.Equal(t, model.KindAgentPlugin, e.Kind)
	})

	t.Run("plugin_loaded", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "plugin_loaded.json")
		require.Equal(t, model.KindAgentPlugin, e.Kind)
	})

	t.Run("unknown falls back to KindUnknown, never dropped", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "unknown_event.json")
		require.Equal(t, model.KindUnknown, e.Kind)
		require.Equal(t, "some_future_event", e.EventName, "event_name is preserved even when unrecognized")
		v, ok := e.Attrs["weird_attr"]
		require.True(t, ok, "attrs stays intact for an unknown event")
		require.Equal(t, "weird_value", v)
	})
}

// TestFromOTLPLogs_EventNameResolutionOrder covers SPEC §1.5.1's three-step
// resolution order and the capture-derived fact (finding 4.1) that the
// unprefixed event.name attribute and the prefixed body must agree once
// normalized.
func TestFromOTLPLogs_EventNameResolutionOrder(t *testing.T) {
	t.Parallel()

	t.Run("LogRecord.EventName field wins over event.name attr and body", func(t *testing.T) {
		t.Parallel()
		e := normalizeOne(t, "resolution_eventname_field_wins.json")
		require.Equal(t, "tool_result", e.EventName)
	})

	unprefixed := normalizeOne(t, "resolution_unprefixed_attr.json")
	bodyOnly := normalizeOne(t, "resolution_body_only.json")

	t.Run("unprefixed event.name attribute resolves to the unprefixed name", func(t *testing.T) {
		require.Equal(t, "tool_result", unprefixed.EventName)
	})
	t.Run("prefixed body-only record resolves to the same unprefixed name", func(t *testing.T) {
		require.Equal(t, "tool_result", bodyOnly.EventName)
	})
	t.Run("both forms agree", func(t *testing.T) {
		require.Equal(t, unprefixed.EventName, bodyOnly.EventName)
	})
}

// TestFromOTLPLogs_APIRequestQuerySourceVerbatim pins the SPEC §0/§1.9
// "no vendor vocabulary is ever constrained" rule for query_source: the
// live-capture-observed `generate_session_title` value (undocumented, not
// `main|subagent|auxiliary`) must be stored verbatim with no error and no
// mapping, and no OTel log event may carry agent_id.
func TestFromOTLPLogs_APIRequestQuerySourceVerbatim(t *testing.T) {
	t.Parallel()
	e := normalizeOne(t, "api_request_generate_session_title.json")
	require.Equal(t, model.KindLLMRequest, e.Kind)
	require.Equal(t, strp("generate_session_title"), e.QuerySource)
	require.Nil(t, e.AgentID)
	require.Nil(t, e.PromptID, "the generate_session_title call happens outside any turn (live capture §3)")
}

// TestFromOTLPLogs_MissingSessionIDRejectedOthersKept covers the only
// rejection case (ticket P2-02): a record with no session.id is rejected,
// while the rest of the batch is still returned as Events.
func TestFromOTLPLogs_MissingSessionIDRejectedOthersKept(t *testing.T) {
	t.Parallel()
	data := loadFixture(t, "missing_session_id_batch.json")
	n := newTestNormalizer()
	events, rejections := n.FromOTLPLogs(data)

	require.Len(t, rejections, 1)
	require.Equal(t, "missing session.id", rejections[0].Reason)
	toolName, ok := rejections[0].Record["tool_name"]
	require.True(t, ok, "the rejection carries the full record for diagnostics")
	require.Equal(t, "Read", toolName)

	require.Len(t, events, 1, "the second record in the batch must still be returned")
	require.Equal(t, model.KindTurnStart, events[0].Kind)
	require.Equal(t, "0d7f3a8f-0ae3-4d5b-b256-70394e9e7a56", events[0].SessionID)
}

// TestFromOTLPLogs_ResourceRecordCollisionRecordWins covers SPEC §3.4's
// "merge resource/scope attributes with record attributes (record wins)":
// a resource attribute "flag" becomes "resource.flag" once prefixed, which
// collides with a record attribute literally named "resource.flag" — the
// record's value must win.
func TestFromOTLPLogs_ResourceRecordCollisionRecordWins(t *testing.T) {
	t.Parallel()
	e := normalizeOne(t, "resource_record_collision.json")
	v, ok := e.Attrs["resource.flag"]
	require.True(t, ok)
	require.Equal(t, "record-wins", v)
}

// TestFromOTLPLogs_ClockSkewFlagged covers SPEC §3.4: TimeUnixNano
// disagreeing with the event.timestamp attribute by more than 5s raises
// clock_skewed, and TimeUnixNano (not event.timestamp) is what is stored as
// ts.
func TestFromOTLPLogs_ClockSkewFlagged(t *testing.T) {
	t.Parallel()
	e := normalizeOne(t, "clock_skew.json")
	require.True(t, e.ClockSkewed)
	// clock_skew.json's event.timestamp attribute claims 10s before
	// TimeUnixNano; the stored ts must be the TimeUnixNano-derived one
	// (SPEC §3.4: "TimeUnixNano is preferred"), not the 10s-earlier
	// attribute value.
	wantTS := time.Date(2026, 8, 11, 21, 53, 21, 600000000, time.UTC)
	require.WithinDuration(t, wantTS, e.TS, time.Millisecond)
}

// TestFromOTLPLogs_NilInputIsSafe documents that a nil LogsData (never sent
// by a real OTLP client, but cheap to guard) yields empty, non-nil-panicking
// results rather than a crash.
func TestFromOTLPLogs_NilInputIsSafe(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer()
	events, rejections := n.FromOTLPLogs(nil)
	require.Empty(t, events)
	require.Empty(t, rejections)
}
