package normalize

import "strings"

// vendorEventNamePrefix is the prefix Claude Code's `body` field carries
// (`"claude_code.api_request"`) that its `event.name` *attribute* does not
// (`"api_request"`) — live-capture finding 4.1
// (docs/research/live-capture-2026-08-11.md). ResolveEventName strips it
// unconditionally so `events.event_name` always holds the unprefixed form
// regardless of which of the three sources supplied it.
const vendorEventNamePrefix = "claude_code."

// ResolveEventName implements SPEC §1.5.1's event-name resolution order for
// one OTel log record:
//
//  1. LogRecord.EventName (the OTLP 1.x field) if non-empty, else
//  2. the `event.name` attribute if present, else
//  3. the record body, when it is a string.
//
// then strips a leading "claude_code." from whichever source won. recordEventName
// is the OTLP LogRecord.EventName field value (pass "" when unset — OTLP
// itself uses the empty string as absence for this field). attrs is the
// merged resource+scope+record attribute map. bodyStr/bodyIsString carry the
// record body's decoded string form and whether the body was a string at
// all (a non-string body, e.g. a kvlist, is not a valid name source per
// SPEC §1.5.1 step 3 and is skipped).
//
// This function is OTel-log-specific and is not part of the shared
// attrs.go/rejection.go contract other normalizers reuse (package doc
// comment): hook payloads carry their event name directly in
// `hook_event_name`, with no resolution ambiguity.
func ResolveEventName(recordEventName string, attrs map[string]any, bodyStr string, bodyIsString bool) string {
	name := recordEventName
	if name == "" {
		if v := String(attrs, "event.name"); v != nil {
			name = *v
		} else if bodyIsString {
			name = bodyStr
		}
	}
	return strings.TrimPrefix(name, vendorEventNamePrefix)
}
