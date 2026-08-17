package normalize

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
)

// hookEventMessageDisplay is the one `hook_event_name` SPEC §1.5.2 says is
// "dropped by default": high volume, no analytic value, gated behind
// ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY.
const hookEventMessageDisplay = "MessageDisplay"

// hookDecisionSourceUnknown is the literal value written into the
// unconstrained `decision_source` text column for `PermissionDenied` (SPEC
// §1.5.2: "the hook does not state *who* denied; the OTel `tool_decision`
// does — do not guess"). It is a plain string, not a member of any Go enum —
// SPEC §0 forbids a Go type that could reject a decision_source value, and
// this package must not invent provenance the payload never asserted.
const hookDecisionSourceUnknown = "unknown"

// HookNormalizer holds the state FromHookPayload needs injected to be
// deterministic in tests, mirroring the pattern otel_logs.go's Normalizer
// established for ticket P2-02 (SPEC rule 8: inject the clock rather than
// race time.Now). It is a separate type from that Normalizer — not an
// added field on it — because otel_logs.go is P2-02's file (already
// committed, read-only for this ticket per the concurrent-agent file
// ownership split) and hooks.go must not touch it; two small injected-option
// structs cost nothing and keep the ownership boundary exact.
type HookNormalizer struct {
	// Now returns the server clock used as both IngestedAt and, per SPEC
	// §1.5.2 ("ts = now() at receipt … unless the payload carries a
	// timestamp"), the default event ts. A nil Now is treated as time.Now
	// defensively, matching otel_logs.go's Normalizer.
	Now func() time.Time

	// RetentionRaw is ARGUS_RETENTION_RAW_DAYS as a time.Duration, passed to
	// model.ClampTimestamp exactly as otel_logs.go's Normalizer does (SPEC
	// §1.2 applies to every source, not only otel_log).
	RetentionRaw time.Duration

	// AllowMessageDisplay gates the `MessageDisplay` hook event (SPEC
	// §1.5.2, ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY, default false). This
	// package must not import internal/config (depguard, doc.go), so the
	// flag arrives here as a plain bool the httpapi/ingest wiring layer
	// reads from config and injects — the same seam Now/RetentionRaw use.
	AllowMessageDisplay bool
}

// NewHookNormalizer builds a HookNormalizer with an injected clock,
// retention window, and MessageDisplay gate, so tests can freeze "now" and
// assert clamp/gating behaviour deterministically.
func NewHookNormalizer(now func() time.Time, retentionRaw time.Duration, allowMessageDisplay bool) *HookNormalizer {
	return &HookNormalizer{Now: now, RetentionRaw: retentionRaw, AllowMessageDisplay: allowMessageDisplay}
}

// FromHookPayload implements SPEC §1.5.2 and §3.5 for one decoded hooks
// webhook request body: a single JSON object, or a JSON array of objects
// (SPEC §3.5: "also accepts an array for batch replay by argus-sim").
//
// Mixed-array decision (ticket P2-03, documented per its instruction to
// state the choice and rationale): an array in which some elements are
// valid and at least one is missing `session_id` fails the *whole* call —
// FromHookPayload returns an error and zero events, not a partial result.
// This deliberately differs from FromOTLPLogs's per-record Rejection
// design. The OTLP surface is a batch of independently-sourced records
// where "some of this batch could not be attributed" is itself the normal,
// expected outcome (SPEC §3.4's partial_success). The hooks webhook is not:
// SPEC §3.5 says Claude Code sends exactly one payload per request and
// answers with a single `{"ok":true,"event":"<hook_event_name>"}` — there is
// no wire shape for "202 accepted 3 of 4". Because normalization runs
// in-request before enqueue (SPEC §3.6: "fails fast with a 400 and never
// occupies queue capacity"), returning an error here means the request is
// rejected visibly (400) and *nothing* from it is silently dropped into the
// queue — the "no valid event may be silently lost" AC is satisfied by
// never accepting the partial batch in the first place, not by salvaging
// part of it. `argus-sim`, the only caller that ever sends an array, can
// retry with corrected input; a caller that wants partial acceptance must
// send one request per hook event, which is what real Claude Code always
// does anyway.
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

// splitHookPayload implements SPEC §3.5's "single object or array" body
// shape. A body whose first non-whitespace byte is `[` is decoded as a JSON
// array of objects; anything else is treated as exactly one object, so a
// single malformed body still surfaces as a JSON decode error rather than an
// empty result.
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

// trimLeadingJSONSpace strips the JSON whitespace characters (RFC 8259 §2)
// so splitHookPayload can sniff the first significant byte without a full
// parse.
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

// buildHookEvent turns one decoded hook JSON object into a model.Event. The
// bool return is false only for a `MessageDisplay` payload dropped by the
// AllowMessageDisplay gate (SPEC §1.5.2) — that is a deliberate, configured
// drop, never an error, and never counts against "no valid event may be
// silently lost" since it was never a valid-to-keep event in the first
// place under the default config.
//
// The only error this returns is a missing/empty `session_id` (SPEC §3.5:
// "the handler validates session_id" — material for a 400). Every other
// field is read defensively via attrs.go's coercing accessors: a
// missing/null/wrong-typed field yields nil, never an error (SPEC §1.5.2:
// "hook payload field names beyond the common set are [unverified-safe]").
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
		Vendor:     "claude_code", // SPEC §1.5.2: "vendor = claude_code" — hooks are Claude Code's own transport, unlike OTLP's resource-derived vendor
		Source:     model.SourceHook,
		EventName:  hookEventName,
		// VendorSeq is deliberately left nil: hooks carry no counterpart to
		// OTel's event.sequence (SPEC §1.7 rule 2's hash-fallback dedup form
		// applies to every hook event, never the vendor_seq form).
		ClockSkewed: skewed,
		Attrs:       attrs,

		// permission_mode is a *common* payload field (SPEC §1.5.2), applied
		// to every hook event regardless of hook_event_name — including an
		// unrecognised one, per SPEC §0's passthrough rule.
		PermissionMode: String(attrs, "permission_mode"),

		// agent_id/agent_type are likewise common on *subagent* payloads
		// (SPEC §1.5.2: "subagent payloads add agent_id → agent_id,
		// agent_type → agent_type") — read unconditionally since their
		// presence, not the hook_event_name, is what signals a subagent
		// context. parent_agent_id has no such common-field status in the
		// SPEC prose; it is read only where the table names it explicitly
		// (SubagentStart, below).
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
		// Unreachable with any input FromHookPayload can produce: attrs is
		// decoded exclusively by encoding/json, whose own output types
		// (string/float64/bool/nil/[]any/map[string]any) always re-marshal.
		// Kept as a fail-safe fallback for the same reason otel_logs.go's
		// buildEvent keeps one (SPEC §1.7 rule 2 has "no exceptions": a
		// missing DedupKey would silently break idempotency instead of
		// degrading to a deterministic, session-scoped key).
		evt.DedupKey = "hook:" + *sessionID + ":unhashable:" + hookEventName
	}

	return evt, true, nil
}

// resolveHookTimestamp implements SPEC §1.5.2's "ts = now() at receipt …
// unless the payload carries a timestamp". No hook timestamp field name is
// verified by the live capture (it captured OTel, not hooks — SPEC §1.5.2's
// own [unverified-safe] caveat exists for exactly this reason), so this
// checks the plausible `timestamp` key defensively and falls back to
// receipt time on any absence or parse failure — never an error, matching
// every other hook field read in this file.
func resolveHookTimestamp(attrs map[string]any, ingestedAt time.Time) time.Time {
	if s := String(attrs, "timestamp"); s != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *s); err == nil {
			return parsed.UTC()
		}
	}
	return ingestedAt
}

// knownFileToolNames is the "known file tools" SPEC §1.5.2 names for
// `PreToolUse`'s `file_path` extraction: the built-in tools whose
// `tool_input` is documented (Claude Code docs, not the live capture, hence
// [unverified-safe]) to carry a `file_path` key. Glob's `path` argument
// names a search directory, not a single file, but SPEC §1.5.2 still asks
// for "file_path from tool input for known file tools" and lead note 5
// explicitly includes "Glob-ish" — so it is read into the same column as a
// best-effort, defensive fallback, never as a claim that the value is a
// file rather than a directory.
var knownFileToolNames = map[string]struct{}{
	"Read":         {},
	"Edit":         {},
	"Write":        {},
	"NotebookEdit": {},
	"Glob":         {},
}

// hookToolFilePath implements the file_path extraction PreToolUse's SPEC
// §1.5.2 row asks for. It reads defensively: an unrecognised tool_name, a
// missing tool_input, or a tool_input without either key all yield nil,
// never an error.
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
	// Glob's argument is named `path`, not `file_path` — see
	// knownFileToolNames's doc comment.
	return String(toolInput, "path")
}

// applyHookKindMapping implements the SPEC §1.5.2 table row for
// hookEventName, writing every column that row promotes directly onto evt
// and returning the row's Kind. Like otel_logs.go's applyKindMapping, this
// switches on the raw event-name *string*, not a model.Kind, so the
// `exhaustive` linter (scoped to switches over model.Kind, SPEC .golangci.yml
// deviation D-11) does not apply; `default` is required because
// hook_event_name is unconstrained vendor text (SPEC §0) that must always
// resolve to some Kind.
//
// Several rows promote a value that the SPEC table marks as feeding a
// `sessions` *projection* field rather than an `events` column
// (`SessionStart`'s `attrs.source`/`cwd`, `SessionEnd`'s `attrs.reason`,
// `CwdChanged`'s `attrs.cwd`). Those values need no extra code here: the
// full hook body is already in evt.Attrs (SPEC §1.3, "promotion is a copy,
// not a move"), and turning attrs into a sessions-row update is the store
// layer's job (ticket P2-06/P2-07), not this normalizer's. This function
// only ever sets fields that exist on model.Event — it never invents one.
func applyHookKindMapping(hookEventName string, attrs map[string]any, evt *model.Event) model.Kind {
	switch hookEventName {
	case "SessionStart":
		// attrs.source → sessions.start_type, cwd → sessions.cwd: both
		// projection-only (see doc comment above).
		return model.KindSessionStart

	case "SessionEnd":
		// attrs.reason → sessions.end_reason: projection-only.
		return model.KindSessionEnd

	case "Setup":
		return model.KindAgentSetup

	case "UserPromptSubmit":
		// Prompt text, if sent, stays attrs-only (SPEC §1.5.2) — no
		// model.Event field carries turn prompt text.
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
		// attrs.cwd → sessions.cwd update: projection-only.
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
		// Reached only when AllowMessageDisplay gated it open (the drop path
		// returns before this function is called). SPEC §1.5.2 assigns
		// MessageDisplay no Kind at all — unlike every other named row, its
		// entry is "*dropped by default*", not a table cell — so there is no
		// mapping to promote it to. KindUnknown is used deliberately rather
		// than inventing a Kind the SPEC never defined: EventName still
		// preserves "MessageDisplay" for the quality/unknown-kinds
		// inspector, exactly as it would for a genuinely unrecognised
		// hook_event_name.
		return model.KindUnknown

	default:
		return model.KindUnknown
	}
}
