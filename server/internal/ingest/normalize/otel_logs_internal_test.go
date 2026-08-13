package normalize

import (
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/stretchr/testify/require"
)

// TestResolveVendor covers SPEC §1.5.1's vendor resolution: the documented
// claude-code → claude_code mapping, the unknown-when-absent default, and
// the deliberate passthrough of a service.name Argus has never seen (vendor
// is agent-agnostic text, not a closed taxonomy — SPEC §0, §1.3).
func TestResolveVendor(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude_code", resolveVendor(map[string]any{"service.name": "claude-code"}))
	require.Equal(t, "unknown", resolveVendor(map[string]any{}))
	require.Equal(t, "unknown", resolveVendor(map[string]any{"service.name": ""}))
	require.Equal(t, "codex-cli", resolveVendor(map[string]any{"service.name": "codex-cli"}), "an unrecognized service.name passes through verbatim rather than being coerced to unknown")
}

// TestResolveCostUSD covers SPEC §1.5.1's cost preference: cost_usd_micros
// (divided by 1e6) wins over cost_usd when both are present; cost_usd is
// used only as a fallback; absence of both yields nil.
func TestResolveCostUSD(t *testing.T) {
	t.Parallel()

	require.InDelta(t, 0.001153, *resolveCostUSD(map[string]any{
		"cost_usd_micros": int64(1153),
		"cost_usd":        float64(0.0011), // deliberately different, to prove micros wins
	}), 1e-9)

	require.InDelta(t, 0.0042, *resolveCostUSD(map[string]any{"cost_usd": float64(0.0042)}), 1e-9)

	require.Nil(t, resolveCostUSD(map[string]any{}))
}

// TestResolveTimestamp covers SPEC §3.4's timestamp preference and skew
// detection directly, including the attr-only and neither-present fallback
// paths that the fixture-driven tests don't exercise on their own.
func TestResolveTimestamp(t *testing.T) {
	t.Parallel()

	t.Run("TimeUnixNano only", func(t *testing.T) {
		t.Parallel()
		rec := &logspb.LogRecord{TimeUnixNano: 1_700_000_000_000_000_000}
		ts, skewed := resolveTimestamp(rec, map[string]any{})
		require.False(t, skewed)
		require.Equal(t, time.Unix(0, 1_700_000_000_000_000_000).UTC(), ts)
	})

	t.Run("event.timestamp attr only", func(t *testing.T) {
		t.Parallel()
		rec := &logspb.LogRecord{}
		ts, skewed := resolveTimestamp(rec, map[string]any{"event.timestamp": "2026-08-11T21:51:56.419Z"})
		require.False(t, skewed)
		require.Equal(t, 2026, ts.Year())
	})

	t.Run("neither present yields zero time, no skew flag of its own", func(t *testing.T) {
		t.Parallel()
		rec := &logspb.LogRecord{}
		ts, skewed := resolveTimestamp(rec, map[string]any{})
		require.False(t, skewed)
		require.True(t, ts.IsZero())
	})

	t.Run("unparseable event.timestamp attr is ignored, not an error", func(t *testing.T) {
		t.Parallel()
		rec := &logspb.LogRecord{TimeUnixNano: 1_700_000_000_000_000_000}
		ts, skewed := resolveTimestamp(rec, map[string]any{"event.timestamp": "not-a-timestamp"})
		require.False(t, skewed)
		require.Equal(t, time.Unix(0, 1_700_000_000_000_000_000).UTC(), ts)
	})

	t.Run("agreement within threshold is not skewed", func(t *testing.T) {
		t.Parallel()
		nano := uint64(1_700_000_000_000_000_000)
		rec := &logspb.LogRecord{TimeUnixNano: nano}
		attrTS := time.Unix(0, int64(nano)).UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
		_, skewed := resolveTimestamp(rec, map[string]any{"event.timestamp": attrTS})
		require.False(t, skewed)
	})
}
