package sim

import (
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// formatEventTimestamp renders ts in the exact shape the live capture's
// `event.timestamp` attribute uses: ISO 8601 / RFC3339 with millisecond
// precision and a literal "Z" (research doc: "event.timestamp ISO 8601";
// fixture testdata/otel/api_request_sdk.json: "2026-08-11T21:53:02.761Z").
func formatEventTimestamp(ts time.Time) string {
	return ts.UTC().Format("2006-01-02T15:04:05.000Z")
}

// newLogRecord assembles one OTLP LogRecord with the common attribute
// envelope every event.name in the live capture carries (research doc §2's
// observed key list) plus the fidelity rule's required pair: the prefixed
// `body` ("claude_code.<name>") and the unprefixed `event.name` attribute
// (live capture finding 4.1, SPEC §1.5.1 step 1-3). It deliberately never
// sets the structured OTLP 1.x LogRecord.EventName field: the capture's raw
// fixtures (testdata/otel/*.json) show every record resolving its name via
// body+attribute only, never via that field, so leaving it unset is the
// literal capture shape, not merely a compatible one.
func newLogRecord(id sessionIdentity, ts time.Time, seq int64, unprefixedName string, promptID *string, extra ...*commonpb.KeyValue) *logspb.LogRecord {
	attrs := id.commonRecordAttrs()
	attrs = append(attrs,
		kvString("event.name", unprefixedName),
		kvString("event.timestamp", formatEventTimestamp(ts)),
		kvInt("event.sequence", seq),
	)
	if promptID != nil {
		attrs = append(attrs, kvString("prompt.id", *promptID))
	}
	attrs = append(attrs, extra...)

	return &logspb.LogRecord{
		TimeUnixNano: uint64(ts.UnixNano()),
		Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude_code." + unprefixedName}},
		Attributes:   attrs,
	}
}

// buildUserPrompt implements SPEC §7.1 item 2's "user_prompt log event
// with the same prompt_id [as UserPromptSubmit], the log event carrying
// prompt_length + message.uuid" and the §1.5.1 mapping row. promptLength
// and messageUUID attribute keys are live-capture-verified (research doc
// 4.4: "user_prompt carries prompt_length and message.uuid").
func buildUserPrompt(id sessionIdentity, ts time.Time, seq int64, promptID string, promptLength int, messageUUID string) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "user_prompt", &promptID,
		kvInt("prompt_length", int64(promptLength)),
		kvString("message.uuid", messageUUID),
	)
}

// buildAssistantResponse implements the §1.5.1 `assistant_response` row:
// `message.uuid` is the only promoted field, live-capture-verified
// (otel_logs_test.go asserts it from the real fixture this package's
// output must round-trip identically to).
func buildAssistantResponse(id sessionIdentity, ts time.Time, seq int64, promptID string, messageUUID string) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "assistant_response", &promptID,
		kvString("message.uuid", messageUUID),
	)
}

// apiRequestFields is the full set of values SPEC §7.1 item 2's api_request
// generation recipe draws. querySource == "" means "omit the attribute"
// (25% of the mixed distribution is "absent", not the literal empty
// string — live capture §3 never observed an empty-string query_source).
type apiRequestFields struct {
	model                                                     string
	inputTokens, outputTokens, cacheReadTokens, cacheCreation int64
	durationMS                                                int64
	querySource                                               string
	includeCost                                               bool
	costMicros                                                int64
	requestID                                                 string
}

// buildAPIRequest implements SPEC §7.1 item 2's api_request recipe and the
// §1.5.1 mapping row. Every attribute key here — model, input_tokens,
// output_tokens, cache_read_tokens, cache_creation_tokens, cost_usd,
// cost_usd_micros, duration_ms, request_id, query_source — is
// live-capture-verified on api_request (research doc §3's observed key
// list; fixture api_request_sdk.json). --cost-mode=omit drops cost_usd /
// cost_usd_micros entirely (SPEC: "exercise the estimated-cost path") —
// includeCost=false implements that.
//
// No agent_id/parent_agent_id is ever added here (fidelity rule, SPEC
// §1.9): apiRequestFields has no such field, and the fixed set of kv pairs
// below is exhaustive over what this function emits.
func buildAPIRequest(id sessionIdentity, ts time.Time, seq int64, promptID *string, f apiRequestFields) *logspb.LogRecord {
	extra := []*commonpb.KeyValue{
		kvString("model", f.model),
		kvInt("input_tokens", f.inputTokens),
		kvInt("output_tokens", f.outputTokens),
		kvInt("cache_read_tokens", f.cacheReadTokens),
		kvInt("cache_creation_tokens", f.cacheCreation),
		kvInt("duration_ms", f.durationMS),
		kvString("request_id", f.requestID),
	}
	if f.includeCost {
		extra = append(extra,
			kvDouble("cost_usd", float64(f.costMicros)/1e6),
			kvInt("cost_usd_micros", f.costMicros),
		)
	}
	if f.querySource != "" {
		extra = append(extra, kvString("query_source", f.querySource))
	}
	return newLogRecord(id, ts, seq, "api_request", promptID, extra...)
}

// buildAPIError implements the §1.5.1 `api_error` row (`model`,
// `duration_ms`, `error_type`/`status_code`, `request_id`, `query_source`),
// live-capture-verified via testdata/otel/api_error.json (same attribute
// set FromOTLPLogs's api_error test asserts against).
func buildAPIError(id sessionIdentity, ts time.Time, seq int64, promptID *string, model string, durationMS int64, statusCode int, requestID, querySource string) *logspb.LogRecord {
	extra := []*commonpb.KeyValue{
		kvString("model", model),
		kvInt("duration_ms", durationMS),
		kvInt("status_code", int64(statusCode)),
		kvString("request_id", requestID),
	}
	if querySource != "" {
		extra = append(extra, kvString("query_source", querySource))
	}
	return newLogRecord(id, ts, seq, "api_error", promptID, extra...)
}

// buildAPIRefusal implements the §1.5.1 `api_refusal` row (`model`,
// `error_type = attrs.category`).
func buildAPIRefusal(id sessionIdentity, ts time.Time, seq int64, promptID *string, model, category string) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "api_refusal", promptID,
		kvString("model", model),
		kvString("category", category),
	)
}

// toolResultFields is the SPEC §7.1 tool_result recipe's payload
// (`duration_ms`, `tool_input_size_bytes`, `tool_result_size_bytes`,
// `error`/`error_type` on failure, `tool_parameters.file_path` /
// `subagent_type` under --tool-details) plus the size fields the P2-02
// mapping row reads out of attrs (SPEC §1.5.1).
type toolResultFields struct {
	toolName, toolUseID             string
	success                         bool
	durationMS                      int64
	inputSizeBytes, resultSizeBytes int64
	errType                         string // "" when success
	decisionSource                  string
	filePath, subagentType          string // "" when --tool-details=0 or not applicable
}

// buildToolResult implements the §1.5.1 `tool_result` row. `tool_use_id` is
// always present (live capture: every tool_decision/tool_result carries it
// — §1.6's "Guarantee (verified, no longer conditional)"). error_type and
// error are both set on failure, matching live capture 4.3 ("Note error
// *and* error_type"). tool_parameters.file_path / subagent_type are only
// added when the caller passes non-empty values, i.e. only under
// --tool-details=1 and only for tools that carry them (SPEC §1.5.1:
// "require OTEL_LOG_TOOL_DETAILS=1"). No agent_id is ever emitted here —
// the OTel side never carries subagent attribution (SPEC §1.9, live
// capture §3).
func buildToolResult(id sessionIdentity, ts time.Time, seq int64, promptID *string, f toolResultFields) *logspb.LogRecord {
	extra := []*commonpb.KeyValue{
		kvString("tool_name", f.toolName),
		kvString("tool_use_id", f.toolUseID),
		kvBool("success", f.success),
		kvInt("duration_ms", f.durationMS),
		kvInt("tool_input_size_bytes", f.inputSizeBytes),
		kvInt("tool_result_size_bytes", f.resultSizeBytes),
	}
	if f.decisionSource != "" {
		extra = append(extra, kvString("decision_source", f.decisionSource))
	}
	if !f.success {
		extra = append(extra,
			kvString("error", "simulated tool failure"),
			kvString("error_type", f.errType),
		)
	}
	if f.filePath != "" {
		extra = append(extra, kvString("tool_parameters.file_path", f.filePath))
	}
	if f.subagentType != "" {
		extra = append(extra, kvString("tool_parameters.subagent_type", f.subagentType))
	}
	return newLogRecord(id, ts, seq, "tool_result", promptID, extra...)
}

// buildToolDecision implements the §1.5.1 `tool_decision` row and SPEC
// §7.1's "PreToolUse hook → tool_decision (with tool_use_id, per the live
// capture)". includeToolUseID defaults true (--tool-use-id-in-decision,
// live-capture-verified per §1.5.1's "tool_decision carries tool_use_id —
// verified, not assumed" note) but stays a parameter so the degraded path
// (P2-12's --tool-use-id-in-decision=false) is testable.
func buildToolDecision(id sessionIdentity, ts time.Time, seq int64, promptID *string, toolName, toolUseID, decision, decisionSource, toolSource string, includeToolUseID bool) *logspb.LogRecord {
	extra := []*commonpb.KeyValue{
		kvString("decision", decision),
		kvString("source", decisionSource),
		kvString("tool_name", toolName),
	}
	if includeToolUseID {
		extra = append(extra, kvString("tool_use_id", toolUseID))
	}
	extra = append(extra, kvString("tool_source", toolSource))
	return newLogRecord(id, ts, seq, "tool_decision", promptID, extra...)
}

// buildPermissionModeChanged implements the §1.5.1
// `permission_mode_changed` row (`attrs.to_mode`/`attrs.from_mode`),
// live-capture-verified via testdata/otel/permission_mode_changed.json.
func buildPermissionModeChanged(id sessionIdentity, ts time.Time, seq int64, fromMode, toMode string) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "permission_mode_changed", nil,
		kvString("from_mode", fromMode),
		kvString("to_mode", toMode),
	)
}

// buildHookRegistered implements the §1.5.1 `hook_registered` row. Every
// attribute key is live-capture finding 4.2's exact list for this event
// ("hook_event, hook_matcher, hook_type, hook_source, plugin.name" —
// plugin.name/plugin_id_hash/safe_mode also listed; plugin.name is omitted
// here since argus-sim never simulates a plugin).
func buildHookRegistered(id sessionIdentity, ts time.Time, seq int64, hookEvent, hookMatcher, hookType, hookSource string) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "hook_registered", nil,
		kvString("hook_event", hookEvent),
		kvString("hook_type", hookType),
		kvString("hook_source", hookSource),
		kvString("hook_matcher", hookMatcher),
		kvString("safe_mode", "false"),
	)
}

// buildHookExecutionStart implements the §1.5.1 `hook_execution_start` row
// (live capture finding 4.2: "hook_event, hook_name, hook_source,
// num_hooks, managed_only, safe_mode, prompt.id").
func buildHookExecutionStart(id sessionIdentity, ts time.Time, seq int64, promptID *string, hookEvent, hookName, hookSource string, numHooks int) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "hook_execution_start", promptID,
		kvString("hook_event", hookEvent),
		kvString("hook_name", hookName),
		kvString("hook_source", hookSource),
		kvInt("num_hooks", int64(numHooks)),
		kvBool("managed_only", false),
		kvString("safe_mode", "false"),
	)
}

// buildHookExecutionComplete implements the §1.5.1 `hook_execution_complete`
// row: `total_duration_ms` (SPEC §7.1: "drawn lognormal μ=ln(8) σ=0.9 — so
// the hook-latency panel has data"), plus num_success/num_blocking/
// num_cancelled/num_non_blocking_error (live capture finding 4.2), which
// the §1.5.1 mapping row reads to derive `success`.
func buildHookExecutionComplete(id sessionIdentity, ts time.Time, seq int64, promptID *string, hookEvent, hookName, hookSource string, numHooks int, totalDurationMS float64, numSuccess int) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "hook_execution_complete", promptID,
		kvString("hook_event", hookEvent),
		kvString("hook_name", hookName),
		kvString("hook_source", hookSource),
		kvInt("num_hooks", int64(numHooks)),
		kvBool("managed_only", false),
		kvString("safe_mode", "false"),
		kvDouble("total_duration_ms", totalDurationMS),
		kvInt("num_success", int64(numSuccess)),
		kvInt("num_blocking", 0),
		kvInt("num_cancelled", 0),
		kvInt("num_non_blocking_error", 0),
	)
}
