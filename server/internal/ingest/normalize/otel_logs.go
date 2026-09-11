package normalize

import (
	"fmt"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/YohannHommet/argus/server/internal/model"
)

// resourceAttrPrefix reserves namespace for resource attrs (SPEC §3.4: "resource.").
// Scope/record unprefixed; record wins on collision.
const resourceAttrPrefix = "resource."

// clockSkewThreshold is SPEC §3.4's "disagreement > 5 s" bound.
const clockSkewThreshold = 5 * time.Second

// Normalizer holds injected state for deterministic tests (SPEC rule 8 ticket P2-02).
// Fields here instead of package-level to avoid internal/config import (depguard) and
// allow tests to freeze the clock.
type Normalizer struct {
	// Now returns server clock (IngestedAt and "now" per SPEC §1.2). Nil → time.Now.
	Now func() time.Time

	// RetentionRaw is ARGUS_RETENTION_RAW_DAYS, lower bound of ClampTimestamp window.
	RetentionRaw time.Duration
}

// NewNormalizer builds a Normalizer with an injected clock and retention
// window, so tests can freeze "now" and assert clamp/skew behaviour
// deterministically instead of racing the wall clock.
func NewNormalizer(now func() time.Time, retentionRaw time.Duration) *Normalizer {
	return &Normalizer{Now: now, RetentionRaw: retentionRaw}
}

// FromOTLPLogs implements SPEC §1.5.1 and §3.4: walk ResourceLogs/ScopeLogs/LogRecord,
// merge attrs (record wins, resource prefixed), resolve event name, apply per-event-name
// mapping, assign dedup_key and clamped/skew-flagged ts. Never errors (partial_success
// design); only rejection: missing session.id (no row key). Rejects entire batch.
func (n *Normalizer) FromOTLPLogs(data *logspb.LogsData) ([]model.Event, []Rejection) {
	var events []model.Event
	var rejections []Rejection

	if data == nil {
		return events, rejections
	}

	nowFn := n.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	for _, rl := range data.GetResourceLogs() {
		resourceAttrs := otlpAttrsToMap(rl.GetResource().GetAttributes())
		vendor := resolveVendor(resourceAttrs)

		for _, sl := range rl.GetScopeLogs() {
			scopeAttrs := otlpAttrsToMap(sl.GetScope().GetAttributes())

			for _, rec := range sl.GetLogRecords() {
				recordAttrs := otlpAttrsToMap(rec.GetAttributes())

				merged := make(map[string]any, len(resourceAttrs)+len(scopeAttrs)+len(recordAttrs)+1)
				for k, v := range resourceAttrs {
					merged[resourceAttrPrefix+k] = v
				}
				for k, v := range scopeAttrs {
					merged[k] = v
				}
				for k, v := range recordAttrs {
					merged[k] = v // record wins on collision
				}

				bodyStr, bodyIsString := otlpBodyString(rec.GetBody())
				if bodyIsString {
					// Body part of source record even after event-name resolution (SPEC §1.3).
					merged["body"] = bodyStr
				}

				eventName := ResolveEventName(rec.GetEventName(), merged, bodyStr, bodyIsString)

				// audit finding m12: a session.id present only on the
				// resource (not scope/record) is still findable in merged
				// under its "resource." prefix (resourceAttrPrefix, above)
				// — fall back to it before rejecting the record for good.
				sessionID := String(merged, "session.id")
				if sessionID == nil || *sessionID == "" {
					sessionID = String(merged, resourceAttrPrefix+"session.id")
				}
				if sessionID == nil || *sessionID == "" {
					rejections = append(rejections, Rejection{
						Reason: "missing session.id",
						Record: merged,
						Count:  1, // one LogRecord
					})
					continue
				}

				evt, err := n.buildEvent(*sessionID, vendor, eventName, merged, rec, nowFn())
				if err != nil {
					// Provably unreachable now that every string/float64
					// attribute is sanitized at decode time (M5,
					// otlpattrs.go's sanitizeAttrString/sanitizeAttrFloat
					// doc comment): canonicalJSON can no longer fail on
					// this map. Surfaced as a Rejection rather than a
					// silently-degraded dedup key so a future regression
					// in that invariant fails loud (visible in
					// partial_success) instead of writing an event whose
					// idempotency the store's own re-marshal would break
					// anyway.
					rejections = append(rejections, Rejection{
						Reason: fmt.Sprintf("dedup key: %v", err),
						Record: merged,
						Count:  1, // one LogRecord
					})
					continue
				}
				events = append(events, evt)
			}
		}
	}

	return events, rejections
}

// resolveVendor implements SPEC §1.5.1's "vendor is set from the record's
// service.name resource attribute (claude-code → claude_code), defaulting
// to unknown". A service.name Argus has never seen is passed through
// verbatim rather than forced to "unknown": vendor is documented
// agent-agnostic text (SPEC §1.3), not one of the four closed taxonomies
// SPEC §0 permits to reject a value, and mapping every unrecognized name to
// "unknown" would erase a real (if new) vendor's identity rather than merely
// declining to special-case it.
func resolveVendor(resourceAttrs map[string]any) string {
	name := String(resourceAttrs, "service.name")
	if name == nil || *name == "" {
		return "unknown"
	}
	if *name == "claude-code" {
		return "claude_code"
	}
	return *name
}

// buildEvent assembles one model.Event from a resolved session/vendor/
// event-name and the fully merged attrs map, applying the §1.5.1 kind
// mapping, the §3.4 timestamp resolution + clamp, and the §1.7 rule 2 dedup
// key. The only error it returns is model.DedupKeyOTelLog's — see the call
// site's doc comment for why that is provably unreachable post-M5 and is
// nonetheless surfaced rather than papered over.
func (n *Normalizer) buildEvent(sessionID, vendor, eventName string, attrs map[string]any, rec *logspb.LogRecord, ingestedAt time.Time) (model.Event, error) {
	promptID := String(attrs, "prompt.id")
	vendorSeq := Int64(attrs, "event.sequence")

	rawTS, disagreementSkewed := resolveTimestamp(rec, attrs)
	clampedTS, clampSkewed := model.ClampTimestamp(rawTS, ingestedAt, n.RetentionRaw)

	evt := model.Event{
		TS:          clampedTS,
		IngestedAt:  ingestedAt,
		SessionID:   sessionID,
		PromptID:    promptID,
		Vendor:      vendor,
		Source:      model.SourceOTelLog,
		EventName:   eventName,
		VendorSeq:   vendorSeq,
		ClockSkewed: clampSkewed || disagreementSkewed,
		Attrs:       attrs,
		// AgentID/ParentAgentID/AgentType are deliberately never populated
		// here: no OTel log event carries agent attribution (SPEC §1.9,
		// live-capture §3). Those columns exist only for hook payloads.
	}

	evt.Kind = applyKindMapping(eventName, attrs, &evt)

	dedupKey, err := model.DedupKeyOTelLog(sessionID, vendorSeq, eventName, attrs)
	if err != nil {
		// (M5) unreachable post-sanitization; surfaced as Rejection not fallback.
		return model.Event{}, err
	}
	evt.DedupKey = dedupKey

	return evt, nil
}

// resolveTimestamp implements SPEC §3.4: prefer TimeUnixNano over
// event.timestamp string; disagreement > 5 s raises clock_skewed flag.
// Zero time falls back to ClampTimestamp for fail-safe clamping.
func resolveTimestamp(rec *logspb.LogRecord, attrs map[string]any) (ts time.Time, tsSkew bool) {
	var fromNano time.Time
	haveNano := rec.GetTimeUnixNano() != 0
	if haveNano {
		fromNano = time.Unix(0, int64(rec.GetTimeUnixNano())).UTC() //nolint:gosec // uint64 wire timestamp never approaches int64 overflow within any plausible event time
	}

	var fromAttr time.Time
	haveAttr := false
	if s := String(attrs, "event.timestamp"); s != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *s); err == nil {
			fromAttr = parsed.UTC()
			haveAttr = true
		}
	}

	switch {
	case haveNano && haveAttr:
		diff := fromNano.Sub(fromAttr)
		if diff < 0 {
			diff = -diff
		}
		return fromNano, diff > clockSkewThreshold
	case haveNano:
		return fromNano, false
	case haveAttr:
		return fromAttr, false
	default:
		return time.Time{}, false
	}
}

// applyKindMapping implements the SPEC §1.5.1 table row for eventName,
// writing every column that row promotes directly onto evt and returning
// the row's Kind. This switches on the *event-name string*, not a
// model.Kind, so golangci-lint's `exhaustive` check (which SPEC's
// .golangci.yml deviation D-11 scopes to switches over model.Kind) does not
// apply here; the `default` case is nonetheless required because
// event_name is unconstrained vendor text (SPEC §0) and must always resolve
// to some Kind, never a compile-time-enumerable one.
func applyKindMapping(eventName string, attrs map[string]any, evt *model.Event) model.Kind {
	switch eventName {
	case "user_prompt":
		return model.KindTurnStart

	case "assistant_response":
		evt.MessageUUID = String(attrs, "message.uuid")
		return model.KindAssistantMessage

	case "api_request":
		evt.Model = String(attrs, "model")
		evt.InputTokens = Int64(attrs, "input_tokens")
		evt.OutputTokens = Int64(attrs, "output_tokens")
		evt.CacheReadTokens = Int64(attrs, "cache_read_tokens")
		evt.CacheCreationTokens = Int64(attrs, "cache_creation_tokens")
		evt.DurationMS = int64PtrToIntPtr(Int64(attrs, "duration_ms"))
		evt.CostUSD = resolveCostUSD(attrs)
		// (D-30) cost_source="reported" only when cost_usd is known.
		if evt.CostUSD != nil {
			costSource := "reported"
			evt.CostSource = &costSource
		}
		evt.RequestID = String(attrs, "request_id")
		evt.QuerySource = String(attrs, "query_source") // unconstrained text passthrough — SPEC §0, §1.9
		success := true
		evt.Success = &success
		return model.KindLLMRequest

	case "api_error":
		evt.Model = String(attrs, "model")
		evt.DurationMS = int64PtrToIntPtr(Int64(attrs, "duration_ms"))
		if et := String(attrs, "error_type"); et != nil {
			evt.ErrorType = et
		} else {
			evt.ErrorType = StringLike(attrs, "status_code")
		}
		evt.RequestID = String(attrs, "request_id")
		evt.QuerySource = String(attrs, "query_source")
		failure := false
		evt.Success = &failure
		return model.KindLLMError

	case "api_refusal":
		evt.Model = String(attrs, "model")
		evt.ErrorType = String(attrs, "category")
		failure := false
		evt.Success = &failure
		return model.KindLLMRefusal

	case "api_request_body":
		return model.KindLLMRequestBody

	case "api_response_body":
		return model.KindLLMResponseBody

	case "tool_result":
		evt.ToolName = String(attrs, "tool_name")
		evt.ToolUseID = String(attrs, "tool_use_id")
		evt.Success = Bool(attrs, "success")
		evt.DurationMS = int64PtrToIntPtr(Int64(attrs, "duration_ms"))
		evt.ErrorType = String(attrs, "error_type") // attrs.error is kept too (verbatim, in Attrs) — capture shows both keys
		evt.DecisionSource = String(attrs, "decision_source")
		if toolParams, ok := Map(attrs, "tool_parameters"); ok {
			evt.FilePath = String(toolParams, "file_path")
			evt.AgentType = String(toolParams, "subagent_type")
		}
		return model.KindToolResult

	case "tool_decision":
		evt.ToolName = String(attrs, "tool_name")
		evt.ToolUseID = String(attrs, "tool_use_id") // M10, live-capture-verified: tool_decision carries tool_use_id
		evt.Decision = String(attrs, "decision")
		evt.DecisionSource = String(attrs, "source")
		evt.ToolSource = String(attrs, "tool_source")
		return model.KindToolDecision

	case "permission_mode_changed":
		evt.PermissionMode = String(attrs, "to_mode") // from_mode stays in attrs only
		return model.KindPermissionModeChanged

	case "hook_registered":
		return model.KindHookRegistered

	case "hook_execution_start":
		return model.KindHookExecutionStart

	case "hook_execution_complete":
		evt.DurationMS = int64PtrToIntPtr(Int64(attrs, "total_duration_ms"))
		nonBlocking := Int64(attrs, "num_non_blocking_error")
		cancelled := Int64(attrs, "num_cancelled")
		if nonBlocking != nil && cancelled != nil {
			ok := *nonBlocking == 0 && *cancelled == 0
			evt.Success = &ok
		}
		return model.KindHookExecutionEnd

	case "auth":
		evt.Success = Bool(attrs, "success")
		return model.KindAgentAuth

	case "mcp_server_connection":
		evt.Success = Bool(attrs, "success")
		return model.KindMCPConnection

	case "internal_error":
		evt.ErrorType = String(attrs, "error_type")
		failure := false
		evt.Success = &failure
		return model.KindAgentInternalError

	case "plugin_installed", "plugin_loaded":
		return model.KindAgentPlugin

	default:
		return model.KindUnknown
	}
}

// resolveCostUSD implements SPEC §1.5.1's "cost_usd_micros / 1e6 → cost_usd
// (preferred over cost_usd — more precision; fall back to cost_usd)".
func resolveCostUSD(attrs map[string]any) *float64 {
	if micros := Int64(attrs, "cost_usd_micros"); micros != nil {
		usd := float64(*micros) / 1e6
		return &usd
	}
	return Float64(attrs, "cost_usd")
}

// int64PtrToIntPtr narrows an *int64 attribute value to the *int
// model.Event.DurationMS expects. Duration values never approach the
// int32/int64 range boundary on any platform Argus targets, so a plain cast
// is sufficient.
func int64PtrToIntPtr(v *int64) *int {
	if v == nil {
		return nil
	}
	i := int(*v)
	return &i
}
