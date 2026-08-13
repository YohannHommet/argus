package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKind_AllConstantsInAllKinds is the P2-01 AC: every Kind constant is in
// AllKinds() and JSON round-trips.
func TestKind_AllConstantsInAllKinds(t *testing.T) {
	kinds := []Kind{
		KindSessionStart, KindSessionEnd,
		KindTurnStart, KindTurnEnd, KindTurnPromptExpanded,
		KindLLMRequest, KindLLMError, KindLLMRefusal, KindLLMRequestBody, KindLLMResponseBody, KindAssistantMessage,
		KindToolPre, KindToolDecision, KindToolPermissionRequest, KindToolResult, KindToolBatch,
		KindSubagentStart, KindSubagentStop, KindTaskCreated, KindTaskCompleted,
		KindPermissionModeChanged,
		KindHookRegistered, KindHookExecutionStart, KindHookExecutionEnd,
		KindFSFileChanged,
		KindWorkspaceCWDChanged, KindWorkspaceDirectoryAdded, KindWorkspaceConfigChanged,
		KindWorkspaceInstructionsLoaded, KindWorkspaceWorktreeCreated, KindWorkspaceWorktreeRemoved,
		KindContextCompactStart, KindContextCompactEnd,
		KindMCPConnection, KindMCPElicitation, KindMCPElicitationResult,
		KindAgentAuth, KindAgentSetup, KindAgentPlugin, KindAgentInternalError, KindAgentNotification, KindAgentIdle,
		KindUnknown,
	}

	all := AllKinds()
	allSet := make(map[Kind]struct{}, len(all))
	for _, k := range all {
		allSet[k] = struct{}{}
	}

	for _, k := range kinds {
		t.Run(string(k), func(t *testing.T) {
			_, ok := allSet[k]
			require.True(t, ok, "%s must be in AllKinds()", k)
			require.True(t, k.Valid())

			// JSON round-trip: Kind is a defined string type, so it must
			// marshal/unmarshal as its underlying string value with no
			// custom codec surprises.
			b, err := json.Marshal(k)
			require.NoError(t, err)

			var got Kind
			require.NoError(t, json.Unmarshal(b, &got))
			require.Equal(t, k, got)
		})
	}

	require.Len(t, all, len(kinds), "AllKinds() must contain exactly the documented constants, no more, no fewer")
}

// TestKind_NoMetricSample is the explicit P2-01 AC: SPEC §1.4 forbids a
// metric.sample Kind because no metric is ever mirrored into events.
func TestKind_NoMetricSample(t *testing.T) {
	for _, k := range AllKinds() {
		require.NotEqual(t, Kind("metric.sample"), k)
	}
	require.False(t, Kind("metric.sample").Valid())
}

// TestKind_Valid_RejectsUnknownString ensures Valid is a real membership
// check, not a tautology.
func TestKind_Valid_RejectsUnknownString(t *testing.T) {
	require.False(t, Kind("not_a_real_kind").Valid())
	require.False(t, Kind("").Valid())
}

// TestKind_Group is an exhaustive switch over Kind (see Group's
// implementation) — this test exercises every branch so a Kind added
// without updating Group shows up here even before golangci-lint runs.
func TestKind_Group(t *testing.T) {
	tests := []struct {
		kind Kind
		want Group
	}{
		{KindSessionStart, GroupSession},
		{KindSessionEnd, GroupSession},
		{KindTurnStart, GroupTurn},
		{KindTurnEnd, GroupTurn},
		{KindTurnPromptExpanded, GroupTurn},
		{KindLLMRequest, GroupLLM},
		{KindLLMError, GroupLLM},
		{KindLLMRefusal, GroupLLM},
		{KindLLMRequestBody, GroupLLM},
		{KindLLMResponseBody, GroupLLM},
		{KindAssistantMessage, GroupLLM},
		{KindToolPre, GroupTool},
		{KindToolDecision, GroupTool},
		{KindToolPermissionRequest, GroupTool},
		{KindToolResult, GroupTool},
		{KindToolBatch, GroupTool},
		{KindSubagentStart, GroupAgentic},
		{KindSubagentStop, GroupAgentic},
		{KindTaskCreated, GroupAgentic},
		{KindTaskCompleted, GroupAgentic},
		{KindPermissionModeChanged, GroupPermission},
		{KindHookRegistered, GroupHooks},
		{KindHookExecutionStart, GroupHooks},
		{KindHookExecutionEnd, GroupHooks},
		{KindFSFileChanged, GroupFS},
		{KindWorkspaceCWDChanged, GroupWorkspace},
		{KindWorkspaceDirectoryAdded, GroupWorkspace},
		{KindWorkspaceConfigChanged, GroupWorkspace},
		{KindWorkspaceInstructionsLoaded, GroupWorkspace},
		{KindWorkspaceWorktreeCreated, GroupWorkspace},
		{KindWorkspaceWorktreeRemoved, GroupWorkspace},
		{KindContextCompactStart, GroupContext},
		{KindContextCompactEnd, GroupContext},
		{KindMCPConnection, GroupMCP},
		{KindMCPElicitation, GroupMCP},
		{KindMCPElicitationResult, GroupMCP},
		{KindAgentAuth, GroupAgent},
		{KindAgentSetup, GroupAgent},
		{KindAgentPlugin, GroupAgent},
		{KindAgentInternalError, GroupAgent},
		{KindAgentNotification, GroupAgent},
		{KindAgentIdle, GroupAgent},
		{KindUnknown, GroupFallback},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			require.Equal(t, tt.want, tt.kind.Group())
		})
	}
}
