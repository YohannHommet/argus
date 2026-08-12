package sim

// Hook payload builders. Field names mirror
// internal/ingest/normalize/testdata/hooks/*.json exactly (the fixtures the
// real HookNormalizer.FromHookPayload is tested against, SPEC §1.5.2) so
// this package's output round-trips through the same normalizer with no
// unknown fields silently dropped. Every payload is a plain
// map[string]any: HookNormalizer decodes hook bodies as JSON objects, never
// as typed Go structs, and this package's fidelity rule (doc.go) is about
// which keys appear, not about a wire type.
//
// hookSessionStart/... functions are pure: given already-decided field
// values, they return the payload map with no I/O and no RNG draws of
// their own (session.go owns every distribution decision).

// hookCommon builds the three fields every hook payload carries
// (SPEC §1.5.2: "Common payload fields: session_id → session_id, prompt_id
// → prompt_id, hook_event_name → event_name"). promptID nil omits the key
// entirely, matching how session-lifecycle hooks (SessionStart/SessionEnd)
// have no prompt.
func hookCommon(sessionID string, promptID *string, hookEventName string) map[string]any {
	m := map[string]any{
		"session_id":      sessionID,
		"hook_event_name": hookEventName,
	}
	if promptID != nil {
		m["prompt_id"] = *promptID
	}
	return m
}

// hookSessionStart implements SPEC §1.5.2's SessionStart row
// (testdata/hooks/session_start.json: cwd, permission_mode, source,
// transcript_path).
func hookSessionStart(sessionID, cwd, permissionMode, startType, transcriptPath string) map[string]any {
	m := hookCommon(sessionID, nil, "SessionStart")
	m["cwd"] = cwd
	m["permission_mode"] = permissionMode
	m["source"] = startType
	m["transcript_path"] = transcriptPath
	return m
}

// hookSessionEnd implements SPEC §1.5.2's SessionEnd row
// (testdata/hooks/session_end.json: reason). SPEC §7.1 item 3: emitted with
// p=0.85 ("15% of sessions are abandoned with no end event").
func hookSessionEnd(sessionID, reason string) map[string]any {
	m := hookCommon(sessionID, nil, "SessionEnd")
	m["reason"] = reason
	return m
}

// hookUserPromptSubmit implements SPEC §7.1 item 2's "UserPromptSubmit
// hook … with the same prompt_id" as the paired user_prompt log event.
func hookUserPromptSubmit(sessionID, promptID string) map[string]any {
	return hookCommon(sessionID, &promptID, "UserPromptSubmit")
}

// hookStop implements the §1.5.2 Stop row (testdata/hooks/stop.json has no
// extra fields beyond the common set).
func hookStop(sessionID, promptID string) map[string]any {
	return hookCommon(sessionID, &promptID, "Stop")
}

// hookPreToolUse implements the §1.5.2 PreToolUse row
// (testdata/hooks/pre_tool_use.json: tool_name, tool_use_id, tool_input).
// includeToolUseID implements --tool-use-id-in-hooks (SPEC §7.1: "toggles
// whether hook payloads carry tool_use_id, exercising both heuristic and
// hook_only" [correlation]). agentID non-empty adds agent_id (SPEC §7.1:
// "Task/Agent calls … whose hook payloads carry agent_id/parent_agent_id").
func hookPreToolUse(sessionID, promptID, toolName, toolUseID string, includeToolUseID bool, filePath, agentID string) map[string]any {
	m := hookCommon(sessionID, &promptID, "PreToolUse")
	m["tool_name"] = toolName
	if includeToolUseID {
		m["tool_use_id"] = toolUseID
	}
	if filePath != "" {
		m["tool_input"] = map[string]any{"file_path": filePath}
	}
	if agentID != "" {
		m["agent_id"] = agentID
	}
	return m
}

// hookPostToolUse implements the §1.5.2 PostToolUse row
// (testdata/hooks/post_tool_use.json: tool_name, tool_use_id, agent_id).
func hookPostToolUse(sessionID, promptID, toolName, toolUseID string, includeToolUseID bool, agentID string) map[string]any {
	m := hookCommon(sessionID, &promptID, "PostToolUse")
	m["tool_name"] = toolName
	if includeToolUseID {
		m["tool_use_id"] = toolUseID
	}
	if agentID != "" {
		m["agent_id"] = agentID
	}
	return m
}

// hookPostToolUseFailure implements the §1.5.2 PostToolUseFailure row
// (testdata/hooks/post_tool_use_failure.json: tool_name, tool_use_id,
// error_type).
func hookPostToolUseFailure(sessionID, promptID, toolName, toolUseID, errType string, includeToolUseID bool) map[string]any {
	m := hookCommon(sessionID, &promptID, "PostToolUseFailure")
	m["tool_name"] = toolName
	if includeToolUseID {
		m["tool_use_id"] = toolUseID
	}
	m["error_type"] = errType
	return m
}

// hookPermissionRequest implements the §1.5.2 PermissionRequest row
// (testdata/hooks/permission_request.json: tool_name). SPEC §7.1 item 2:
// "occasional PermissionRequest before a user_* decision, with a human-
// latency gap … so wait_ms is realistic" — session.go stamps the gap into
// this event's own ts vs. the following tool_decision's ts; wait_ms itself
// is not a documented hook field (not present in any testdata/hooks
// fixture), so it is not fabricated onto this payload — the realism lives
// in the timestamp delta between the two events, which is exactly what
// SPEC says it should produce ("so wait_ms is realistic": a downstream
// projection derives wait_ms from that delta, not from an attribute here).
func hookPermissionRequest(sessionID, promptID, toolName string) map[string]any {
	m := hookCommon(sessionID, &promptID, "PermissionRequest")
	m["tool_name"] = toolName
	return m
}

// hookSubagentStart implements the §1.5.2 SubagentStart row
// (testdata/hooks/subagent_start.json: agent_id, agent_type,
// parent_agent_id). SPEC §7.1 item 2: "Task/Agent calls … emit
// SubagentStart, a nested mini-session … whose hook payloads carry
// agent_id/parent_agent_id". parentAgentID == "" omits the key (top-level,
// depth-1 subagent).
func hookSubagentStart(sessionID, promptID, agentID, agentType, parentAgentID string) map[string]any {
	m := hookCommon(sessionID, &promptID, "SubagentStart")
	m["agent_id"] = agentID
	m["agent_type"] = agentType
	if parentAgentID != "" {
		m["parent_agent_id"] = parentAgentID
	}
	return m
}

// hookFileChanged implements the §1.5.2 FileChanged row
// (testdata/hooks/file_changed.json: file_path). SPEC §7.1 item 2:
// "p=0.05 FileChanged burst".
func hookFileChanged(sessionID, promptID, filePath string) map[string]any {
	m := hookCommon(sessionID, &promptID, "FileChanged")
	m["file_path"] = filePath
	return m
}

// hookPreCompact / hookPostCompact implement the §1.5.2 PreCompact/
// PostCompact rows (testdata/hooks/pre_compact.json, post_compact.json:
// common fields only). SPEC §7.1 item 2: "p=0.02 PreCompact/PostCompact
// pair".
func hookPreCompact(sessionID, promptID string) map[string]any {
	return hookCommon(sessionID, &promptID, "PreCompact")
}

func hookPostCompact(sessionID, promptID string) map[string]any {
	return hookCommon(sessionID, &promptID, "PostCompact")
}

// hookSubagentStop implements the §1.5.2 SubagentStop row
// (testdata/hooks/subagent_stop.json: agent_id, success).
func hookSubagentStop(sessionID, promptID, agentID string, success bool) map[string]any {
	m := hookCommon(sessionID, &promptID, "SubagentStop")
	m["agent_id"] = agentID
	m["success"] = success
	return m
}
