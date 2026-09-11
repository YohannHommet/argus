package normalize

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
)

// hookEventMessageDisplay is dropped by default (high volume, no value), gated behind env var.
const hookEventMessageDisplay = "MessageDisplay"

// hookDecisionSourceUnknown: SPEC §1.5.2 says hook does not state *who* denied.
// Plain string (not enum) per SPEC §0: must not reject any decision_source value.
const hookDecisionSourceUnknown = "unknown"

// HookNormalizer holds injected state for deterministic tests (SPEC rule 8:
// inject clock, not race time.Now). Separate from otel_logs.go's Normalizer
// due to file ownership split; two small structs keep the boundary exact.
type HookNormalizer struct {
	// Now is the server clock; nil → time.Now. Default event ts per SPEC §1.5.2.
	Now func() time.Time

	// RetentionRaw is ARGUS_RETENTION_RAW_DAYS as a time.Duration for ClampTimestamp.
	RetentionRaw time.Duration

	// AllowMessageDisplay gates MessageDisplay hook events (SPEC §1.5.2, env var).
	// Injected here since package must not import internal/config (depguard).
	AllowMessageDisplay bool
}

// NewHookNormalizer builds a HookNormalizer with an injected clock,
// retention window, and MessageDisplay gate, so tests can freeze "now" and
// assert clamp/gating behaviour deterministically.
func NewHookNormalizer(now func() time.Time, retentionRaw time.Duration, allowMessageDisplay bool) *HookNormalizer {
	return &HookNormalizer{Now: now, RetentionRaw: retentionRaw, AllowMessageDisplay: allowMessageDisplay}
}

// FromHookPayload implements SPEC §1.5.2 and §3.5 for one hook webhook body
// (single JSON object or array for batch replay). Deliberately differs from
// FromOTLPLogs: an array with ANY missing session_id fails the *whole* call
// (no partial results). SPEC §3.5 specifies exactly one payload per request;
// normalization runs in-request before enqueue, so error means 400 + zero events
// silently dropped. `argus-sim` can retry; real Claude Code sends one-per-request.
func (n *HookNormalizer) FromHookPayload(body []byte) ([]model.Event, error) {
	nowFn := n.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	rawElements, err := splitHookPayload(body)
	if err != nil {
		return nil, err
	}

	events := make([]model.Event, 0, len(rawElements))
	for i, raw := range rawElements {
		var attrs map[string]any
		if err := json.Unmarshal(raw, &attrs); err != nil {
			return nil, fmt.Errorf("normalize: decode hook payload element %d: %w", i, err)
		}
		// Audit finding M5: sanitize before this map is stored as either
		// evt.Attrs or hashed into the dedup key (otlpattrs.go's
		// sanitizeHookAttrs doc comment).
		attrs = sanitizeHookAttrs(attrs)

		evt, keep, err := n.buildHookEvent(attrs, nowFn())
		if err != nil {
			return nil, fmt.Errorf("normalize: hook payload element %d: %w", i, err)
		}
		if keep {
			events = append(events, evt)
		}
	}

	return events, nil
}

// splitHookPayload implements SPEC §3.5: "single object or array".
// First non-whitespace byte `[` → JSON array; else one object.
func splitHookPayload(body []byte) ([]json.RawMessage, error) {
	trimmed := trimLeadingJSONSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var elements []json.RawMessage
		if err := json.Unmarshal(body, &elements); err != nil {
			return nil, fmt.Errorf("normalize: decode hook payload array: %w", err)
		}
		return elements, nil
	}
	return []json.RawMessage{body}, nil
}

// trimLeadingJSONSpace strips JSON whitespace (RFC 8259 §2) for first-byte sniff.
func trimLeadingJSONSpace(body []byte) []byte {
	i := 0
	for i < len(body) {
		switch body[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return body[i:]
		}
	}
	return body[i:]
}

// buildHookEvent decodes one hook JSON object. Bool is false only for MessageDisplay
// when gated (deliberate drop, not counted against "no silent loss" since not valid-to-keep).
// Only error: missing/empty session_id. Other fields read defensively, nil on absent/wrong-type.
func (n *HookNormalizer) buildHookEvent(attrs map[string]any, ingestedAt time.Time) (model.Event, bool, error) {
	sessionID := String(attrs, "session_id")
	if sessionID == nil || *sessionID == "" {
		return model.Event{}, false, errors.New("normalize: hook payload missing session_id")
	}

	hookEventName := ""
	if s := String(attrs, "hook_event_name"); s != nil {
		hookEventName = *s
	}

	if hookEventName == hookEventMessageDisplay && !n.AllowMessageDisplay {
		return model.Event{}, false, nil
	}

	promptID := String(attrs, "prompt_id")

	rawTS := resolveHookTimestamp(attrs, ingestedAt)
	clampedTS, skewed := model.ClampTimestamp(rawTS, ingestedAt, n.RetentionRaw)

	evt := model.Event{
		TS:         clampedTS,
		IngestedAt: ingestedAt,
		SessionID:  *sessionID,
		PromptID:   promptID,
		Vendor:     "claude_code", // SPEC §1.5.2: hooks are Claude Code's own transport
		Source:     model.SourceHook,
		EventName:  hookEventName,
		// VendorSeq nil: hooks have no counterpart to OTel event.sequence (SPEC §1.7 rule 2).
		ClockSkewed: skewed,
		Attrs:       attrs,

		// permission_mode is common, applied to every hook event.
		PermissionMode: String(attrs, "permission_mode"),

		// agent_id/agent_type are common on subagent payloads, read unconditionally.
		// parent_agent_id is read only for SubagentStart (below).
		AgentID:   String(attrs, "agent_id"),
		AgentType: String(attrs, "agent_type"),
	}

	evt.Kind = applyHookKindMapping(hookEventName, attrs, &evt)

	promptIDForDedup := ""
	if promptID != nil {
		promptIDForDedup = *promptID
	}
	if dedupKey, err := model.DedupKeyHook(hookEventName, *sessionID, promptIDForDedup, attrs); err == nil {
		evt.DedupKey = dedupKey
	} else {
		// Unreachable (attrs from encoding/json always re-marshals). Fail-safe per SPEC §1.7 rule 2.
		evt.DedupKey = "hook:" + *sessionID + ":unhashable:" + hookEventName
	}

	return evt, true, nil
}

// resolveHookTimestamp implements SPEC §1.5.2: "ts = now() at receipt unless
// payload carries timestamp". No hook field name verified by live capture; check
// `timestamp` defensively, fall back to receipt on absence/parse failure.
func resolveHookTimestamp(attrs map[string]any, ingestedAt time.Time) time.Time {
	if s := String(attrs, "timestamp"); s != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *s); err == nil {
			return parsed.UTC()
		}
	}
	return ingestedAt
}

// knownFileToolNames is SPEC §1.5.2 "known file tools" for PreToolUse's file_path.
// Glob's `path` is read defensively as fallback (best-effort, not a file claim).
var knownFileToolNames = map[string]struct{}{
	"Read":         {},
	"Edit":         {},
	"Write":        {},
	"NotebookEdit": {},
	"Glob":         {},
}

// hookToolFilePath extracts file_path for PreToolUse (SPEC §1.5.2).
// Reads defensively: unrecognized tool_name, missing tool_input, or missing key all → nil.
func hookToolFilePath(attrs map[string]any) *string {
	toolName := String(attrs, "tool_name")
	if toolName == nil {
		return nil
	}
	if _, known := knownFileToolNames[*toolName]; !known {
		return nil
	}
	toolInput, ok := Map(attrs, "tool_input")
	if !ok {
		return nil
	}
	if fp := String(toolInput, "file_path"); fp != nil {
		return fp
	}
	// Glob's argument is `path`, not `file_path`.
	return String(toolInput, "path")
}

// applyHookKindMapping implements SPEC §1.5.2 row → Kind. Switches on raw
// event-name string (not model.Kind), so exhaustive lint doesn't apply; default
// required (hook_event_name is unconstrained vendor text per SPEC §0).
// Some rows promote sessions-projection fields; they stay in evt.Attrs per SPEC §1.3
// (promotion is copy not move); store layer's job to build sessions rows.
func applyHookKindMapping(hookEventName string, attrs map[string]any, evt *model.Event) model.Kind {
	switch hookEventName {
	case "SessionStart":
		// attrs.source/cwd → sessions projection, not events (see doc comment above).
		return model.KindSessionStart

	case "SessionEnd":
		// attrs.reason → sessions.end_reason (projection-only).
		return model.KindSessionEnd

	case "Setup":
		return model.KindAgentSetup

	case "UserPromptSubmit":
		// Prompt text stays attrs-only (no Event field carries it per SPEC §1.5.2).
		return model.KindTurnStart

	case "Stop":
		success := true
		evt.Success = &success
		return model.KindTurnEnd

	case "StopFailure":
		failure := false
		evt.Success = &failure
		evt.ErrorType = String(attrs, "error_type")
		return model.KindTurnEnd

	case "PreToolUse":
		evt.ToolName = String(attrs, "tool_name")
		evt.ToolUseID = String(attrs, "tool_use_id") // [unverified-safe]
		evt.FilePath = hookToolFilePath(attrs)
		return model.KindToolPre

	case "PostToolUse":
		evt.ToolName = String(attrs, "tool_name")
		evt.ToolUseID = String(attrs, "tool_use_id")
		success := true
		evt.Success = &success
		return model.KindToolResult

	case "PostToolUseFailure":
		evt.ToolName = String(attrs, "tool_name")
		evt.ToolUseID = String(attrs, "tool_use_id")
		failure := false
		evt.Success = &failure
		evt.ErrorType = String(attrs, "error_type")
		return model.KindToolResult

	case "PostToolBatch":
		return model.KindToolBatch

	case "PermissionRequest":
		evt.ToolName = String(attrs, "tool_name")
		decision := "pending"
		evt.Decision = &decision
		return model.KindToolPermissionRequest

	case "PermissionDenied":
		evt.ToolName = String(attrs, "tool_name")
		decision := "reject"
		evt.Decision = &decision
		decisionSource := hookDecisionSourceUnknown
		evt.DecisionSource = &decisionSource
		return model.KindToolDecision

	case "SubagentStart":
		evt.ParentAgentID = String(attrs, "parent_agent_id")
		return model.KindSubagentStart

	case "SubagentStop":
		evt.Success = Bool(attrs, "success")
		return model.KindSubagentStop

	case "TaskCreated":
		// attrs.task_id: projection-only, already in Attrs.
		evt.Success = Bool(attrs, "success")
		return model.KindTaskCreated

	case "TaskCompleted":
		evt.Success = Bool(attrs, "success")
		return model.KindTaskCompleted

	case "TeammateIdle":
		return model.KindAgentIdle

	case "FileChanged":
		evt.FilePath = String(attrs, "file_path")
		return model.KindFSFileChanged

	case "CwdChanged":
		// attrs.cwd → sessions.cwd (projection-only).
		return model.KindWorkspaceCWDChanged

	case "DirectoryAdded":
		evt.FilePath = String(attrs, "file_path")
		return model.KindWorkspaceDirectoryAdded

	case "ConfigChange":
		return model.KindWorkspaceConfigChanged

	case "InstructionsLoaded":
		return model.KindWorkspaceInstructionsLoaded

	case "WorktreeCreate":
		evt.FilePath = String(attrs, "file_path")
		return model.KindWorkspaceWorktreeCreated

	case "WorktreeRemove":
		evt.FilePath = String(attrs, "file_path")
		return model.KindWorkspaceWorktreeRemoved

	case "PreCompact":
		return model.KindContextCompactStart

	case "PostCompact":
		return model.KindContextCompactEnd

	case "UserPromptExpansion":
		return model.KindTurnPromptExpanded

	case "Elicitation":
		return model.KindMCPElicitation

	case "ElicitationResult":
		return model.KindMCPElicitationResult

	case "Notification":
		return model.KindAgentNotification

	case hookEventMessageDisplay:
		// Reached only when AllowMessageDisplay gated it open. SPEC §1.5.2 assigns
		// no Kind; use KindUnknown deliberately (EventName preserves "MessageDisplay").
		return model.KindUnknown

	default:
		return model.KindUnknown
	}
}
