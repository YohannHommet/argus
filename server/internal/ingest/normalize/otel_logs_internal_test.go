package normalize

import (
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
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

// TestApplyKindMapping_CostSourceOnlyWhenCostKnown pins D-30's normalize-side
// fix (docs/review/phase-4-gauntlet.md, candidate D-30): an api_request
// event must never claim cost_source="reported" when cost_usd is NULL. The
// pre-fix code stamped costSource := "reported" unconditionally, so a
// --cost-mode=omit event (resolveCostUSD returns nil) landed as
// {cost_usd: NULL, cost_source: "reported"} — a text column asserting a
// reported cost that does not exist (SPEC §1.3 types cost_source `text
// null`). upsert_session.go/upsert_turn.go's estimation branch keys off
// e.CostUSD, not e.CostSource (belt and braces — see their doc comments),
// but a wrong cost_source is still a lie on its own and must never be
// written, regardless of what any projection folder does with it.
func TestApplyKindMapping_CostSourceOnlyWhenCostKnown(t *testing.T) {
	t.Parallel()

	t.Run("cost_usd present -> cost_source is reported", func(t *testing.T) {
		t.Parallel()
		evt := model.Event{}
		kind := applyKindMapping("api_request", map[string]any{"cost_usd": float64(0.0042)}, &evt)
		require.Equal(t, model.KindLLMRequest, kind)
		require.NotNil(t, evt.CostUSD)
		require.Equal(t, strp("reported"), evt.CostSource)
	})

	t.Run("no cost attr at all -> cost_source is nil, not reported", func(t *testing.T) {
		t.Parallel()
		evt := model.Event{}
		kind := applyKindMapping("api_request", map[string]any{}, &evt)
		require.Equal(t, model.KindLLMRequest, kind)
		require.Nil(t, evt.CostUSD, "resolveCostUSD must return nil with neither cost_usd nor cost_usd_micros present")
		require.Nil(t, evt.CostSource, "cost_source must not claim \"reported\" when cost_usd is NULL (SPEC §1.3, D-30)")
	})
}
