package normalize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// TestResolveEventName_CapsUnboundedBodyFallback is the audit finding m4's
// required repro: a 4 KB record body, with no LogRecord.EventName and no
// event.name attribute, so ResolveEventName falls all the way through to
// SPEC §1.5.1 step 3's body fallback. Before the fix, the resolved name
// carried the whole 4 KB into model.DedupKeyOTelLog's dedup_key (a btree
// key twice over, 002_events.sql:65); this asserts it is capped to
// maxEventNameLen runes.
func TestResolveEventName_CapsUnboundedBodyFallback(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", 4096)
	name := ResolveEventName("", map[string]any{}, body, true)
	require.Len(t, []rune(name), maxEventNameLen)
	require.Equal(t, strings.Repeat("x", maxEventNameLen), name)
}

// TestResolveEventName_CapsUnboundedEventNameAttr covers the audit note
// "a long event.name *attribute* triggers it too — the real gap is the
// unbounded key": the cap must apply regardless of which of the three
// SPEC §1.5.1 sources supplied the name, not only the body fallback.
func TestResolveEventName_CapsUnboundedEventNameAttr(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("y", 1000)
	name := ResolveEventName("", map[string]any{"event.name": longName}, "", false)
	require.Len(t, []rune(name), maxEventNameLen)
}

// TestResolveEventName_CapsUnboundedRecordEventName covers the third
// source, LogRecord.EventName itself.
func TestResolveEventName_CapsUnboundedRecordEventName(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("z", 1000)
	name := ResolveEventName(longName, map[string]any{}, "", false)
	require.Len(t, []rune(name), maxEventNameLen)
}

// TestResolveEventName_StripsNewlines asserts a dedup_key component never
// carries an embedded line break, regardless of which source supplied it.
func TestResolveEventName_StripsNewlines(t *testing.T) {
	t.Parallel()

	name := ResolveEventName("line one\nline two\r\nline three", map[string]any{}, "", false)
	require.NotContains(t, name, "\n")
	require.NotContains(t, name, "\r")
	require.Equal(t, "line oneline twoline three", name)
}

// TestResolveEventName_ShortNamesUnaffected is the non-regression check:
// every existing SPEC §1.5.1 mapping-table name (well under the cap) must
// come out byte-for-byte identical, since dedup_key is a stability
// contract (ticket instructions: "changing its construction changes dedup
// identity across a deploy").
func TestResolveEventName_ShortNamesUnaffected(t *testing.T) {
	t.Parallel()

	require.Equal(t, "api_request", ResolveEventName("claude_code.api_request", nil, "", false))
	require.Equal(t, "tool_result", ResolveEventName("", map[string]any{"event.name": "tool_result"}, "", false))
	require.Equal(t, "user_prompt", ResolveEventName("", nil, "claude_code.user_prompt", true))
}

// TestResolveEventName_CapDoesNotBreakDedupKeyHashing is a light
// integration check that the capped name still flows cleanly into
// model.DedupKeyOTelLog — the whole point of capping rather than leaving
// the key unbounded.
func TestResolveEventName_CapDoesNotBreakDedupKeyHashing(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", 4096)
	name := ResolveEventName("", map[string]any{}, body, true)
	key, err := model.DedupKeyOTelLog("sess-1", nil, name, map[string]any{"body": body})
	require.NoError(t, err)
	require.Less(t, len(key), 512, "dedup_key must stay well under a btree-safe size even for a pathological body")
}
