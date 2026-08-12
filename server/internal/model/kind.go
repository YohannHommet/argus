// Package model holds Argus's canonical domain types (SPEC §3.1): the
// append-only Event record, its closed Kind/Source/Correlation/Status
// taxonomies (SPEC §0 — the *only* closed vocabularies in the system), the
// dedup-key and event-ref codecs, the clock clamp, and the read-side
// projection shapes the store and HTTP layers pass around. This package
// depends on nothing but stdlib (depguard-enforced, SPEC §3.1): it is the
// leaf of the dependency graph so every other package can share these types
// without a cycle.
package model

// Kind is Argus's own normalized event taxonomy (SPEC §1.4). It is a closed
// set — one of the four vocabularies SPEC §0 permits to be closed (`kind`,
// `source`, `correlation`, `status`) — but closed does not mean total: any
// `event_name` the normalizer does not recognize maps to KindUnknown rather
// than being dropped or rejected, so the taxonomy can never reject an input,
// only decline to interpret it (SPEC §1.4: "never dropped").
type Kind string

// Kind constants mirror the SPEC §1.4 table exactly, one per row. There is
// deliberately no `KindMetricSample`: OTLP metric data points are never
// mirrored into `events` (SPEC §1.4, §1.8), so a permanently-dead switch
// branch would fail the `exhaustive` linter for nothing.
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

// AllKinds returns every defined Kind, including KindUnknown. Used by tests
// to assert every constant round-trips and is covered by Valid, and usable
// by callers (e.g. facets) that need to enumerate the taxonomy.
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

// Valid reports whether k is one of the defined Kind constants. It is a
// membership check, not a rejection mechanism at the ingest boundary — the
// normalizer always has KindUnknown available, so Valid is for internal
// assertions (tests, defensive checks) rather than input validation that
// could refuse an event (SPEC §0).
func (k Kind) Valid() bool {
	_, ok := validKinds[k]
	return ok
}

// Group is the SPEC §1.4 table's "Group" column: the coarse category a Kind
// belongs to, used for faceting and UI grouping without hand-maintaining a
// second table. The switch below is exhaustive over Kind (golangci-lint's
// `exhaustive` linter is configured to check exactly this type, SPEC's
// deviation D-11 in .golangci.yml) so a new Kind constant added without a
// matching case here fails CI immediately.
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
	// Unreachable while every Kind constant has a case above; the
	// `exhaustive` linter (SPEC .golangci.yml deviation D-11) fails the
	// build if a new Kind is added without a matching case, which is the
	// point of not having a `default` here.
	return GroupFallback
}
