// Package model holds Argus's canonical domain types (SPEC §3.1): Event record,
// the four closed taxonomies (Kind/Source/Correlation/Status, SPEC §0), codecs,
// and read-side projection shapes. It is the leaf of the dependency graph (depguard-enforced).
package model

// Kind is Argus's normalized event taxonomy (SPEC §1.4), closed but not total:
// unrecognized event_names map to KindUnknown, never dropped or rejected (SPEC §1.4).
type Kind string

// Kind constants mirror SPEC §1.4 exactly. No KindMetricSample: would be permanently dead code (SPEC §1.4, §1.8).
const (
	KindSessionStart Kind = "session.start"
	KindSessionEnd   Kind = "session.end"

	KindTurnStart          Kind = "turn.start"
	KindTurnEnd            Kind = "turn.end"
	KindTurnPromptExpanded Kind = "turn.prompt_expanded"

	KindLLMRequest       Kind = "llm.request"
	KindLLMError         Kind = "llm.error"
	KindLLMRefusal       Kind = "llm.refusal"
	KindLLMRequestBody   Kind = "llm.request_body"
	KindLLMResponseBody  Kind = "llm.response_body"
	KindAssistantMessage Kind = "assistant.message"

	KindToolPre               Kind = "tool.pre"
	KindToolDecision          Kind = "tool.decision"
	KindToolPermissionRequest Kind = "tool.permission_request"
	KindToolResult            Kind = "tool.result"
	KindToolBatch             Kind = "tool.batch"

	KindSubagentStart Kind = "subagent.start"
	KindSubagentStop  Kind = "subagent.stop"
	KindTaskCreated   Kind = "task.created"
	KindTaskCompleted Kind = "task.completed"

	KindPermissionModeChanged Kind = "permission.mode_changed"

	KindHookRegistered     Kind = "hook.registered"
	KindHookExecutionStart Kind = "hook.execution_start"
	KindHookExecutionEnd   Kind = "hook.execution_end"

	KindFSFileChanged Kind = "fs.file_changed"

	KindWorkspaceCWDChanged         Kind = "workspace.cwd_changed"
	KindWorkspaceDirectoryAdded     Kind = "workspace.directory_added"
	KindWorkspaceConfigChanged      Kind = "workspace.config_changed"
	KindWorkspaceInstructionsLoaded Kind = "workspace.instructions_loaded"
	KindWorkspaceWorktreeCreated    Kind = "workspace.worktree_created"
	KindWorkspaceWorktreeRemoved    Kind = "workspace.worktree_removed"

	KindContextCompactStart Kind = "context.compact_start"
	KindContextCompactEnd   Kind = "context.compact_end"

	KindMCPConnection        Kind = "mcp.connection"
	KindMCPElicitation       Kind = "mcp.elicitation"
	KindMCPElicitationResult Kind = "mcp.elicitation_result"

	KindAgentAuth          Kind = "agent.auth"
	KindAgentSetup         Kind = "agent.setup"
	KindAgentPlugin        Kind = "agent.plugin"
	KindAgentInternalError Kind = "agent.internal_error"
	KindAgentNotification  Kind = "agent.notification"
	KindAgentIdle          Kind = "agent.idle"

	// KindUnknown is the fallback for any `event_name` the normalizer does
	// not recognize (SPEC §1.4). Never dropped.
	KindUnknown Kind = "unknown"
)

// AllKinds returns every defined Kind, including KindUnknown, for tests and enumeration.
func AllKinds() []Kind {
	return []Kind{
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
}

var validKinds = func() map[Kind]struct{} {
	m := make(map[Kind]struct{}, len(AllKinds()))
	for _, k := range AllKinds() {
		m[k] = struct{}{}
	}
	return m
}()

// Valid reports whether k is a defined Kind; not a rejection mechanism (SPEC §0).
func (k Kind) Valid() bool {
	_, ok := validKinds[k]
	return ok
}

// Group is the coarse category a Kind belongs to (SPEC §1.4), with an exhaustive switch (D-11).
type Group string

// Group constants match the SPEC §1.4 table's Group column verbatim.
const (
	GroupSession    Group = "session"
	GroupTurn       Group = "turn"
	GroupLLM        Group = "llm"
	GroupTool       Group = "tool"
	GroupAgentic    Group = "agentic"
	GroupPermission Group = "permission"
	GroupHooks      Group = "hooks"
	GroupFS         Group = "fs"
	GroupWorkspace  Group = "workspace"
	GroupContext    Group = "context"
	GroupMCP        Group = "mcp"
	GroupAgent      Group = "agent"
	GroupFallback   Group = "fallback"
)

// Group reports the SPEC §1.4 category k belongs to.
func (k Kind) Group() Group {
	switch k {
	case KindSessionStart, KindSessionEnd:
		return GroupSession
	case KindTurnStart, KindTurnEnd, KindTurnPromptExpanded:
		return GroupTurn
	case KindLLMRequest, KindLLMError, KindLLMRefusal, KindLLMRequestBody, KindLLMResponseBody, KindAssistantMessage:
		return GroupLLM
	case KindToolPre, KindToolDecision, KindToolPermissionRequest, KindToolResult, KindToolBatch:
		return GroupTool
	case KindSubagentStart, KindSubagentStop, KindTaskCreated, KindTaskCompleted:
		return GroupAgentic
	case KindPermissionModeChanged:
		return GroupPermission
	case KindHookRegistered, KindHookExecutionStart, KindHookExecutionEnd:
		return GroupHooks
	case KindFSFileChanged:
		return GroupFS
	case KindWorkspaceCWDChanged, KindWorkspaceDirectoryAdded, KindWorkspaceConfigChanged,
		KindWorkspaceInstructionsLoaded, KindWorkspaceWorktreeCreated, KindWorkspaceWorktreeRemoved:
		return GroupWorkspace
	case KindContextCompactStart, KindContextCompactEnd:
		return GroupContext
	case KindMCPConnection, KindMCPElicitation, KindMCPElicitationResult:
		return GroupMCP
	case KindAgentAuth, KindAgentSetup, KindAgentPlugin, KindAgentInternalError, KindAgentNotification, KindAgentIdle:
		return GroupAgent
	case KindUnknown:
		return GroupFallback
	}
	// Unreachable; exhaustive linter (D-11) requires all cases before build.
	return GroupFallback
}
