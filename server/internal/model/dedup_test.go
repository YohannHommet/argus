package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// bigRecord is deliberately large and nested so 100 re-marshals have a
// realistic chance of hitting map-iteration-order-dependent bugs if
// canonicalJSON ever stopped delegating to encoding/json's key sorting.
func bigRecord() map[string]any {
	return map[string]any{
		"session": map[string]any{
			"id":     "s-1",
			"vendor": "claude_code",
		},
		"event": map[string]any{
			"name":     "api_request",
			"sequence": 12,
		},
		"tokens": map[string]any{
			"input":          100,
			"output":         50,
			"cache_read":     10,
			"cache_creation": 5,
		},
		"nested": map[string]any{
			"a": map[string]any{"x": 1, "y": 2},
			"b": map[string]any{"z": 3},
		},
		"zebra": "z", "apple": "a", "mango": "m",
	}
}

// TestCanonicalJSON_StableAcrossIterationOrder is the P2-01 AC: canonical
// JSON hashing is stable across map iteration order (100 iterations, same
// hash).
func TestCanonicalJSON_StableAcrossIterationOrder(t *testing.T) {
	record := bigRecord()

	first, err := canonicalJSON(record)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		got, err := canonicalJSON(record)
		require.NoError(t, err)
		require.Equal(t, first, got, "iteration %d produced a different canonical form", i)
	}
}

// TestCanonicalJSON_StableAcrossInsertionOrder is the other half of the same
// AC: two maps built by inserting the same keys in different orders hash
// identically.
func TestCanonicalJSON_StableAcrossInsertionOrder(t *testing.T) {
	a := map[string]any{}
	a["zebra"] = "z"
	a["apple"] = "a"
	a["mango"] = "m"

	b := map[string]any{}
	b["mango"] = "m"
	b["apple"] = "a"
	b["zebra"] = "z"

	canonA, err := canonicalJSON(a)
	require.NoError(t, err)
	canonB, err := canonicalJSON(b)
	require.NoError(t, err)
	require.Equal(t, canonA, canonB)

	// And a JSON round-trip of the map produces the same canonical form,
	// proving the property survives a decode/encode cycle too.
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	canonDecoded, err := canonicalJSON(decoded)
	require.NoError(t, err)
	require.Equal(t, canonA, canonDecoded)
}

// TestDedupKeyOTelLog_WithAndWithoutVendorSeq is the P2-01 AC: the otel
// dedup key with and without vendor_seq produce the documented two forms
// and differ.
func TestDedupKeyOTelLog_WithAndWithoutVendorSeq(t *testing.T) {
	record := bigRecord()
	seq := int64(42)

	withSeq, err := DedupKeyOTelLog("sess-1", &seq, "api_request", record)
	require.NoError(t, err)
	require.Regexp(t, `^otel:sess-1:42:api_request:[0-9a-f]{32}$`, withSeq)

	withoutSeq, err := DedupKeyOTelLog("sess-1", nil, "api_request", record)
	require.NoError(t, err)
	require.Regexp(t, `^otel:sess-1:h:[0-9a-f]{32}$`, withoutSeq)

	require.NotEqual(t, withSeq, withoutSeq)

	// Same inputs, same key, both forms deterministic.
	withSeqAgain, err := DedupKeyOTelLog("sess-1", &seq, "api_request", record)
	require.NoError(t, err)
	require.Equal(t, withSeq, withSeqAgain)
}

func TestDedupKeyMetric(t *testing.T) {
	ts := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	attrs := map[string]any{"model": "claude-opus-5"}

	key, err := DedupKeyMetric("claude_code.cost.usage", ts, attrs)
	require.NoError(t, err)
	require.Regexp(t, `^metric:[0-9a-f]{32}$`, key)

	// Deterministic.
	key2, err := DedupKeyMetric("claude_code.cost.usage", ts, attrs)
	require.NoError(t, err)
	require.Equal(t, key, key2)

	// A different name changes the key.
	key3, err := DedupKeyMetric("claude_code.token.usage", ts, attrs)
	require.NoError(t, err)
	require.NotEqual(t, key, key3)
}

func TestDedupKeyHook(t *testing.T) {
	body := map[string]any{"tool_name": "Edit"}

	key, err := DedupKeyHook("PreToolUse", "sess-1", "prompt-1", body)
	require.NoError(t, err)
	require.Regexp(t, `^hook:[0-9a-f]{32}$`, key)

	key2, err := DedupKeyHook("PreToolUse", "sess-1", "prompt-1", body)
	require.NoError(t, err)
	require.Equal(t, key, key2)

	// A different prompt_id changes the key even with identical body.
	key3, err := DedupKeyHook("PreToolUse", "sess-1", "prompt-2", body)
	require.NoError(t, err)
	require.NotEqual(t, key, key3)
}
