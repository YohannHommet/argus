package normalize

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// hookFixedNow mirrors otel_logs_test.go's fixedNow: a frozen clock so
// model.ClampTimestamp and receipt-time ts assignment are deterministic
// (ticket P2-02 rule 8, reused here per the ticket's "equally injectable and
// deterministic under test" instruction).
var hookFixedNow = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

const hookTestRetention = 30 * 24 * time.Hour

func newTestHookNormalizer(allowMessageDisplay bool) *HookNormalizer {
	return NewHookNormalizer(func() time.Time { return hookFixedNow }, hookTestRetention, allowMessageDisplay)
}

// loadHookFixture reads a raw testdata/hooks/<name>.json body byte-for-byte
// — hook payloads are plain JSON (no protojson envelope like the OTLP
// fixtures), so FromHookPayload consumes the file exactly as an HTTP handler
// would consume a request body.
func loadHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hooks", name)) //nolint:gosec // test-only: name is always a literal from this file's own test table
	require.NoError(t, err, "reading fixture %s", name)
	return b
}

// normalizeOneHook loads a single-object fixture, runs FromHookPayload, and
// asserts it produced exactly one event and no error — the common shape for
// every §1.5.2 mapping-row fixture.
func normalizeOneHook(t *testing.T, fixture string) model.Event {
	t.Helper()
	body := loadHookFixture(t, fixture)
	n := newTestHookNormalizer(false)
	events, err := n.FromHookPayload(body)
	require.NoError(t, err, "fixture %s", fixture)
	require.Len(t, events, 1, "fixture %s did not produce exactly one event", fixture)
	return events[0]
}

func hookStrp(s string) *string { return &s }

// TestFromHookPayload_MappingTableRows asserts one case per row of the SPEC
// §1.5.2 table, each checking every column that row promotes.
func TestFromHookPayload_MappingTableRows(t *testing.T) {
	t.Parallel()

	t.Run("SessionStart", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "session_start.json")
		require.Equal(t, model.KindSessionStart, e.Kind)
		require.Equal(t, "SessionStart", e.EventName)
		require.Equal(t, "sess-0001", e.SessionID)
		require.Equal(t, model.SourceHook, e.Source)
		require.Equal(t, "claude_code", e.Vendor)
		// attrs.source → sessions.start_type and cwd → sessions.cwd are the
		// store's projection job (SPEC §1.5.2, §1.6) — this normalizer's
		// only obligation is that the raw values are findable in Attrs.
		require.Equal(t, "startup", e.Attrs["source"])
		require.Equal(t, "/home/dev/project", e.Attrs["cwd"])
	})

	t.Run("SessionEnd", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "session_end.json")
		require.Equal(t, model.KindSessionEnd, e.Kind)
		// attrs.reason → sessions.end_reason: projection-only.
		require.Equal(t, "clean_exit", e.Attrs["reason"])
	})

	t.Run("Setup", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "setup.json")
		require.Equal(t, model.KindAgentSetup, e.Kind)
	})

	t.Run("UserPromptSubmit", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "user_prompt_submit.json")
		require.Equal(t, model.KindTurnStart, e.Kind)
		require.Equal(t, hookStrp("prompt-0001"), e.PromptID)
		// Prompt text stays attrs-only; no model.Event field carries it.
		require.Equal(t, "please refactor this function", e.Attrs["prompt"])
	})

	t.Run("Stop", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "stop.json")
		require.Equal(t, model.KindTurnEnd, e.Kind)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success)
	})

	t.Run("StopFailure", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "stop_failure.json")
		require.Equal(t, model.KindTurnEnd, e.Kind)
		require.NotNil(t, e.Success)
		require.False(t, *e.Success)
		require.Equal(t, hookStrp("timeout"), e.ErrorType)
	})

	t.Run("PreToolUse", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "pre_tool_use.json")
		require.Equal(t, model.KindToolPre, e.Kind)
		require.Equal(t, hookStrp("Edit"), e.ToolName)
		require.Equal(t, hookStrp("tu-0001"), e.ToolUseID)
		require.Equal(t, hookStrp("/home/dev/project/main.go"), e.FilePath)
	})

	t.Run("PreToolUse_Glob_uses_path_fallback", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "pre_tool_use_glob.json")
		require.Equal(t, model.KindToolPre, e.Kind)
		require.Equal(t, hookStrp("Glob"), e.ToolName)
		require.Nil(t, e.ToolUseID, "not present in this fixture")
		require.Equal(t, hookStrp("/home/dev/project"), e.FilePath)
	})

	t.Run("PreToolUse_unknown_tool_yields_no_file_path", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "pre_tool_use_unknown_tool.json")
		require.Equal(t, model.KindToolPre, e.Kind)
		require.Equal(t, hookStrp("Bash"), e.ToolName)
		require.Nil(t, e.FilePath, "Bash is not a known file tool")
	})

	t.Run("PostToolUse", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "post_tool_use.json")
		require.Equal(t, model.KindToolResult, e.Kind)
		require.Equal(t, hookStrp("Edit"), e.ToolName)
		require.Equal(t, hookStrp("tu-0001"), e.ToolUseID)
		require.Equal(t, hookStrp("agent-0001"), e.AgentID)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success, "PostToolUse fixes success=true (SPEC §1.5.2), it is not read from the payload")
	})

	t.Run("PostToolUseFailure", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "post_tool_use_failure.json")
		require.Equal(t, model.KindToolResult, e.Kind)
		require.NotNil(t, e.Success)
		require.False(t, *e.Success)
		require.Equal(t, hookStrp("permission_denied"), e.ErrorType)
	})

	t.Run("PostToolBatch", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "post_tool_batch.json")
		require.Equal(t, model.KindToolBatch, e.Kind)
	})

	t.Run("PermissionRequest", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "permission_request.json")
		require.Equal(t, model.KindToolPermissionRequest, e.Kind)
		require.Equal(t, hookStrp("Bash"), e.ToolName)
		require.Equal(t, hookStrp("pending"), e.Decision)
	})

	t.Run("PermissionDenied_never_invents_provenance", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "permission_denied.json")
		require.Equal(t, model.KindToolDecision, e.Kind)
		require.Equal(t, hookStrp("Bash"), e.ToolName)
		require.Equal(t, hookStrp("reject"), e.Decision)
		// The fixture's own "source": "user_reject" must be IGNORED for
		// DecisionSource — SPEC §1.5.2 is explicit that PermissionDenied
		// never states who denied, so decision_source is hardcoded
		// "unknown" regardless of what the payload contains under any key.
		require.Equal(t, hookStrp("unknown"), e.DecisionSource,
			"must not infer decision_source from any payload field, even one named plausibly")
	})

	t.Run("SubagentStart", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "subagent_start.json")
		require.Equal(t, model.KindSubagentStart, e.Kind)
		require.Equal(t, hookStrp("agent-0001"), e.AgentID)
		require.Equal(t, hookStrp("explore"), e.AgentType)
		require.Equal(t, hookStrp("agent-0000"), e.ParentAgentID)
	})

	t.Run("SubagentStop", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "subagent_stop.json")
		require.Equal(t, model.KindSubagentStop, e.Kind)
		require.Equal(t, hookStrp("agent-0001"), e.AgentID)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success)
	})

	t.Run("TaskCreated", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "task_created.json")
		require.Equal(t, model.KindTaskCreated, e.Kind)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success)
		require.Equal(t, "task-0001", e.Attrs["task_id"], "task_id is projection-only, no model.Event column")
	})

	t.Run("TaskCompleted", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "task_completed.json")
		require.Equal(t, model.KindTaskCompleted, e.Kind)
		require.NotNil(t, e.Success)
		require.True(t, *e.Success)
	})

	t.Run("TeammateIdle", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "teammate_idle.json")
		require.Equal(t, model.KindAgentIdle, e.Kind)
	})

	t.Run("FileChanged", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "file_changed.json")
		require.Equal(t, model.KindFSFileChanged, e.Kind)
		require.Equal(t, hookStrp("/home/dev/project/main.go"), e.FilePath)
	})

	t.Run("CwdChanged", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "cwd_changed.json")
		require.Equal(t, model.KindWorkspaceCWDChanged, e.Kind)
		// attrs.cwd → sessions.cwd update: projection-only.
		require.Equal(t, "/home/dev/other-project", e.Attrs["cwd"])
	})

	t.Run("DirectoryAdded", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "directory_added.json")
		require.Equal(t, model.KindWorkspaceDirectoryAdded, e.Kind)
		require.Equal(t, hookStrp("/home/dev/another-project"), e.FilePath)
	})

	t.Run("ConfigChange", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "config_change.json")
		require.Equal(t, model.KindWorkspaceConfigChanged, e.Kind)
	})

	t.Run("InstructionsLoaded", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "instructions_loaded.json")
		require.Equal(t, model.KindWorkspaceInstructionsLoaded, e.Kind)
	})

	t.Run("WorktreeCreate", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "worktree_create.json")
		require.Equal(t, model.KindWorkspaceWorktreeCreated, e.Kind)
		require.Equal(t, hookStrp("/home/dev/project-worktrees/feature-x"), e.FilePath)
	})

	t.Run("WorktreeRemove", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "worktree_remove.json")
		require.Equal(t, model.KindWorkspaceWorktreeRemoved, e.Kind)
		require.Equal(t, hookStrp("/home/dev/project-worktrees/feature-x"), e.FilePath)
	})

	t.Run("PreCompact", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "pre_compact.json")
		require.Equal(t, model.KindContextCompactStart, e.Kind)
	})

	t.Run("PostCompact", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "post_compact.json")
		require.Equal(t, model.KindContextCompactEnd, e.Kind)
	})

	t.Run("UserPromptExpansion", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "user_prompt_expansion.json")
		require.Equal(t, model.KindTurnPromptExpanded, e.Kind)
	})

	t.Run("Elicitation", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "elicitation.json")
		require.Equal(t, model.KindMCPElicitation, e.Kind)
	})

	t.Run("ElicitationResult", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "elicitation_result.json")
		require.Equal(t, model.KindMCPElicitationResult, e.Kind)
	})

	t.Run("Notification", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "notification.json")
		require.Equal(t, model.KindAgentNotification, e.Kind)
	})

	t.Run("unknown_hook_event_name", func(t *testing.T) {
		t.Parallel()
		e := normalizeOneHook(t, "unknown_event.json")
		require.Equal(t, model.KindUnknown, e.Kind)
		require.Equal(t, "SomethingFutureVersionAdds", e.EventName, "event_name is preserved even when unrecognised")
		require.Equal(t, "bar", e.Attrs["foo"], "full body still lands in attrs for an unknown kind")
	})
}

// TestFromHookPayload_MinimalPayloadNormalizesWithoutError is the AC "a
// payload with only session_id + hook_event_name normalizes without error".
func TestFromHookPayload_MinimalPayloadNormalizesWithoutError(t *testing.T) {
	t.Parallel()
	e := normalizeOneHook(t, "minimal.json")
	require.Equal(t, "sess-0001", e.SessionID)
	require.Equal(t, model.KindAgentNotification, e.Kind)
	require.Nil(t, e.PromptID)
	require.Nil(t, e.ToolName)
	require.Nil(t, e.AgentID)
}

// TestFromHookPayload_MissingSessionIDIsAnError is the AC "missing
// session_id → error (400 material)" — unlike FromOTLPLogs, which turns this
// into a Rejection, the hooks webhook is one synchronous request per SPEC
// §3.5 and answers 400.
func TestFromHookPayload_MissingSessionIDIsAnError(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	body := loadHookFixture(t, "missing_session_id.json")
	events, err := n.FromHookPayload(body)
	require.Error(t, err)
	require.Empty(t, events)
}

// TestFromHookPayload_EmptySessionIDIsAnError asserts an empty string
// session_id is treated the same as an absent one — "" is not a valid
// session identity to key a stored row on.
func TestFromHookPayload_EmptySessionIDIsAnError(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	body := loadHookFixture(t, "empty_session_id.json")
	events, err := n.FromHookPayload(body)
	require.Error(t, err)
	require.Empty(t, events)
}

// TestFromHookPayload_MalformedJSONIsAnError covers the other documented
// error case: malformed JSON.
func TestFromHookPayload_MalformedJSONIsAnError(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	events, err := n.FromHookPayload([]byte(`{"session_id": "sess-0001",`))
	require.Error(t, err)
	require.Empty(t, events)
}

// TestFromHookPayload_MessageDisplay is the AC "MessageDisplay returns zero
// events by default and one when enabled".
func TestFromHookPayload_MessageDisplay(t *testing.T) {
	t.Parallel()

	t.Run("dropped_by_default", func(t *testing.T) {
		t.Parallel()
		n := newTestHookNormalizer(false)
		body := loadHookFixture(t, "message_display.json")
		events, err := n.FromHookPayload(body)
		require.NoError(t, err)
		require.Empty(t, events)
	})

	t.Run("kept_when_enabled", func(t *testing.T) {
		t.Parallel()
		n := newTestHookNormalizer(true)
		body := loadHookFixture(t, "message_display.json")
		events, err := n.FromHookPayload(body)
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.Equal(t, "MessageDisplay", events[0].EventName)
		// SPEC §1.5.2 assigns MessageDisplay no Kind at all (its table cell
		// is "*dropped by default*", not a mapping) — KindUnknown is this
		// normalizer's documented choice rather than an invented Kind.
		require.Equal(t, model.KindUnknown, events[0].Kind)
	})
}

// TestFromHookPayload_ArrayBody is the AC "an array body yields N events".
func TestFromHookPayload_ArrayBody(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	body := loadHookFixture(t, "array_batch.json")
	events, err := n.FromHookPayload(body)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, "sess-0001", events[0].SessionID)
	require.Equal(t, "sess-0002", events[1].SessionID)
	require.Equal(t, "sess-0003", events[2].SessionID)
}

// TestFromHookPayload_ArrayBody_MixedValidityFailsWhole exercises this
// ticket's documented mixed-array decision: one invalid element (missing
// session_id) fails the whole call, so no valid sibling event is returned
// either — nothing is silently half-accepted.
func TestFromHookPayload_ArrayBody_MixedValidityFailsWhole(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	body := loadHookFixture(t, "array_batch_mixed_invalid.json")
	events, err := n.FromHookPayload(body)
	require.Error(t, err)
	require.Empty(t, events, "a rejected batch must not silently keep the valid element")
}

// TestFromHookPayload_UnrecognisedPermissionModePassesThroughVerbatim is the
// AC "a payload with an unrecognised permission_mode string passes it
// through verbatim" — SPEC §0 forbids any Go type/validation that could
// reject a vendor-supplied permission_mode value.
func TestFromHookPayload_UnrecognisedPermissionModePassesThroughVerbatim(t *testing.T) {
	t.Parallel()
	e := normalizeOneHook(t, "permission_mode_passthrough.json")
	require.Equal(t, hookStrp("yolo-experimental-mode-v9"), e.PermissionMode)
}

// TestFromHookPayload_FullBodyAlwaysInAttrs asserts SPEC §1.3's "promotion
// is a copy, not a move" for a hook event whose kind promotes several
// columns: every source field must still be present in Attrs verbatim.
func TestFromHookPayload_FullBodyAlwaysInAttrs(t *testing.T) {
	t.Parallel()
	e := normalizeOneHook(t, "pre_tool_use.json")
	require.Equal(t, "sess-0001", e.Attrs["session_id"])
	require.Equal(t, "PreToolUse", e.Attrs["hook_event_name"])
	require.Equal(t, "Edit", e.Attrs["tool_name"])
	require.Equal(t, "tu-0001", e.Attrs["tool_use_id"])
	toolInput, ok := e.Attrs["tool_input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "/home/dev/project/main.go", toolInput["file_path"])
}

// TestFromHookPayload_CommonFields asserts the SPEC §1.5.2 "common payload
// fields" paragraph: session_id, prompt_id, hook_event_name → event_name,
// vendor/source, and receipt-time ts.
func TestFromHookPayload_CommonFields(t *testing.T) {
	t.Parallel()
	e := normalizeOneHook(t, "user_prompt_submit.json")
	require.Equal(t, "claude_code", e.Vendor)
	require.Equal(t, model.SourceHook, e.Source)
	require.Equal(t, hookFixedNow, e.TS, "ts = receipt time when the payload carries no timestamp")
	require.Equal(t, hookFixedNow, e.IngestedAt)
	require.False(t, e.ClockSkewed)
	require.NotEmpty(t, e.DedupKey)
}

// TestFromHookPayload_DedupKeyUsesLedgerNotTimestamp asserts SPEC §1.7 rule
// 2's premise for hooks: the dedup key is stable across two deliveries of
// the same payload even though receipt-time ts necessarily differs between
// them (a fixed clock can't by itself prove this, so this test calls
// FromHookPayload with two different injected "now" values on the same
// body and asserts the key doesn't change).
func TestFromHookPayload_DedupKeyUsesLedgerNotTimestamp(t *testing.T) {
	t.Parallel()
	body := loadHookFixture(t, "stop.json")

	n1 := NewHookNormalizer(func() time.Time { return hookFixedNow }, hookTestRetention, false)
	n2 := NewHookNormalizer(func() time.Time { return hookFixedNow.Add(5 * time.Second) }, hookTestRetention, false)

	e1, err := n1.FromHookPayload(body)
	require.NoError(t, err)
	e2, err := n2.FromHookPayload(body)
	require.NoError(t, err)

	require.Equal(t, e1[0].DedupKey, e2[0].DedupKey, "receipt-time ts must not leak into the dedup key")
	require.NotEqual(t, e1[0].TS, e2[0].TS, "sanity: the two receipts really did get different ts")
}

// TestFromHookPayload_ClampsOutOfWindowTimestamp exercises SPEC §1.2's clamp
// via a hook payload that carries an explicit out-of-window "timestamp".
func TestFromHookPayload_ClampsOutOfWindowTimestamp(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	body := []byte(`{"session_id":"sess-0001","hook_event_name":"Notification","timestamp":"2000-01-01T00:00:00Z"}`)
	events, err := n.FromHookPayload(body)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].ClockSkewed)
	require.Equal(t, hookFixedNow, events[0].TS)
}

// TestFromHookPayload_HonoursPayloadTimestampWithinWindow asserts the "…
// unless the payload carries a timestamp" half of SPEC §1.5.2's ts rule: an
// in-window explicit timestamp is used verbatim rather than replaced by
// receipt time.
func TestFromHookPayload_HonoursPayloadTimestampWithinWindow(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	explicit := hookFixedNow.Add(-2 * time.Minute)
	body := []byte(`{"session_id":"sess-0001","hook_event_name":"Notification","timestamp":"` + explicit.Format(time.RFC3339Nano) + `"}`)
	events, err := n.FromHookPayload(body)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.False(t, events[0].ClockSkewed)
	require.True(t, explicit.Equal(events[0].TS))
}

// TestFromHookPayload_NilNowDefensesToTimeNow mirrors otel_logs.go's
// Normalizer.Now nil-safety: a zero-value HookNormalizer must not panic.
func TestFromHookPayload_NilNowDefensesToTimeNow(t *testing.T) {
	t.Parallel()
	n := &HookNormalizer{}
	body := loadHookFixture(t, "minimal.json")
	events, err := n.FromHookPayload(body)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.False(t, events[0].IngestedAt.IsZero())
}
