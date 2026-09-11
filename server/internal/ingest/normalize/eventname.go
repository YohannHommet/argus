package normalize

import (
	"strings"
	"unicode/utf8"
)

// maxEventNameLen caps resolved event name to prevent unbounded dedup_key
// (audit finding m4). Record body fallback is arbitrary text with no limit;
// uncapped, it can exceed Postgres btree entry limits (SQLSTATE 54000).
const maxEventNameLen = 255

// capEventName removes newlines and truncates to maxEventNameLen runes.
// Must be cap, not hash: SPEC §1.7 rule 2 is a stability contract (capping
// only changes pathologically long names, hashing would change every existing key).
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

// vendorEventNamePrefix is the "claude_code." prefix Claude Code's body carries
// but its event.name attribute does not. ResolveEventName strips it unconditionally.
const vendorEventNamePrefix = "claude_code."

// ResolveEventName implements SPEC §1.5.1's event-name resolution order:
// LogRecord.EventName (if non-empty), else event.name attribute, else record body
// (if string). Then strips leading "claude_code.". OTel-log-specific, not shared contract.
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
	// audit finding m4: cap regardless of source — long event.name attr is as risky as long body.
	return capEventName(name)
}
