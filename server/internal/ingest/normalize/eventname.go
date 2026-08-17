package normalize

import (
	"strings"
	"unicode/utf8"
)

// maxEventNameLen caps the resolved event name so it can never make
// `dedup_key` (model/dedup.go, which interpolates it verbatim) an
// unbounded btree key (audit finding m4). `LogRecord.EventName` and the
// `event.name` attribute are both short, vendor-controlled identifiers in
// every observed payload, but SPEC §1.5.1 step 3's fallback — the record
// *body* — is arbitrary vendor/user text with no length guarantee at all;
// a body over roughly 2.7 KB pushes `dedup_key` (and therefore the
// `ingest_dedup` PK and the `UNIQUE (ts, dedup_key)` index,
// 002_events.sql:65) past Postgres's btree index entry limit, raising
// SQLSTATE 54000 — a failure retry.go's classification does not recognize,
// so it defaults to transient and the whole batch is retried and dropped.
// 255 is comfortably below that limit for any of the three resolution
// sources while still being generous for a real event name.
const maxEventNameLen = 255

// capEventName drops newlines (a dedup_key is meant to be a single opaque
// token, not a display string with embedded line breaks) and truncates to
// maxEventNameLen runes, never splitting a multi-byte rune. This is
// deliberately a length *cap*, not a hash: SPEC §1.7 rule 2's dedup_key
// construction is a stability contract across a deploy, and hashing the
// name component would change every existing key derived from a
// (session_id, vendor_seq, event_name) triple that happens to already fit
// under the cap — capping only ever changes the pathologically long names
// this finding is about.
func capEventName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, name)

	if utf8.RuneCountInString(name) <= maxEventNameLen {
		return name
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n >= maxEventNameLen {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

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
	name = strings.TrimPrefix(name, vendorEventNamePrefix)
	// audit finding m4: cap regardless of which source won — a
	// pathologically long event.name *attribute* is exactly as dangerous
	// to dedup_key as a long body (see capEventName's doc comment).
	return capEventName(name)
}
