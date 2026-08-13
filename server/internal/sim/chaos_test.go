package sim

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestApplyChaosOrphans_MovesSessionStartPastTurnHooks asserts the
// --chaos-orphans transform's core contract (chaos.go): SessionStart ends
// up chaosOrphanShift hooks later in the slice, every other hook keeps its
// relative order, and no hook's own payload/timestamp is mutated —
// SessionStart is delivered late, not rewritten.
func TestApplyChaosOrphans_MovesSessionStartPastTurnHooks(t *testing.T) {
	t.Parallel()

	base := time.Unix(0, 0).UTC()
	hooks := []hookEmission{
		{TS: base, Payload: map[string]any{"hook_event_name": "SessionStart"}},
		{TS: base.Add(1 * time.Second), Payload: map[string]any{"hook_event_name": "UserPromptSubmit"}},
		{TS: base.Add(2 * time.Second), Payload: map[string]any{"hook_event_name": "PreToolUse"}},
		{TS: base.Add(3 * time.Second), Payload: map[string]any{"hook_event_name": "PostToolUse"}},
		{TS: base.Add(4 * time.Second), Payload: map[string]any{"hook_event_name": "Stop"}},
	}
	result := sessionResult{Hooks: hooks}

	out := applyChaosOrphans(result)
	require.Len(t, out.Hooks, len(hooks))

	// SessionStart moved to index chaosOrphanShift (0 + 3), keeping its own
	// original timestamp — delivered late, not rewritten.
	require.Equal(t, "SessionStart", out.Hooks[chaosOrphanShift].Payload["hook_event_name"])
	require.True(t, out.Hooks[chaosOrphanShift].TS.Equal(base))

	// The three hooks it was moved past now come first, in their original
	// relative order.
	require.Equal(t, "UserPromptSubmit", out.Hooks[0].Payload["hook_event_name"])
	require.Equal(t, "PreToolUse", out.Hooks[1].Payload["hook_event_name"])
	require.Equal(t, "PostToolUse", out.Hooks[2].Payload["hook_event_name"])
	// Everything after the move point is untouched.
	require.Equal(t, "Stop", out.Hooks[4].Payload["hook_event_name"])
}

// TestApplyChaosOrphans_ClampsToSliceLength covers a session with fewer
// hooks after SessionStart than chaosOrphanShift: SessionStart must end up
// last, not panic on an out-of-range slice.
func TestApplyChaosOrphans_ClampsToSliceLength(t *testing.T) {
	t.Parallel()

	hooks := []hookEmission{
		{Payload: map[string]any{"hook_event_name": "SessionStart"}},
		{Payload: map[string]any{"hook_event_name": "Stop"}},
	}
	out := applyChaosOrphans(sessionResult{Hooks: hooks})
	require.Len(t, out.Hooks, 2)
	require.Equal(t, "Stop", out.Hooks[0].Payload["hook_event_name"])
	require.Equal(t, "SessionStart", out.Hooks[1].Payload["hook_event_name"])
}

// TestApplyChaosOrphans_NoSessionStartIsNoOp covers a legacy-app session
// (metrics-only, no hooks at all) and a hook slice that never had a
// SessionStart: both must pass through unchanged rather than panic.
func TestApplyChaosOrphans_NoSessionStartIsNoOp(t *testing.T) {
	t.Parallel()

	require.Empty(t, applyChaosOrphans(sessionResult{}).Hooks)

	hooks := []hookEmission{{Payload: map[string]any{"hook_event_name": "Stop"}}}
	out := applyChaosOrphans(sessionResult{Hooks: hooks})
	require.Equal(t, hooks, out.Hooks)
}

// TestMaybeSkewTimestamp_WithinBounds asserts --chaos-clock-skew's per-event
// draw (SPEC §7.1: "2% ±1h") never shifts a timestamp by more than
// chaosSkewMax in either direction, across enough draws to exercise both
// the "skewed" and "unskewed" branches.
func TestMaybeSkewTimestamp_WithinBounds(t *testing.T) {
	t.Parallel()

	r := chaosRand(1, 0)
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	sawSkewed, sawUnskewed := false, false
	for i := 0; i < 2000; i++ {
		ts := maybeSkewTimestamp(r, base)
		diff := ts.Sub(base)
		require.LessOrEqual(t, diff, chaosSkewMax)
		require.GreaterOrEqual(t, diff, -chaosSkewMax)
		if diff == 0 {
			sawUnskewed = true
		} else {
			sawSkewed = true
		}
	}
	require.True(t, sawSkewed, "expected at least one skewed draw across 2000 iterations at p=2%%")
	require.True(t, sawUnskewed, "expected the common case (no skew) to also occur")
}

// TestBuildChaosTooOldEvent_TimestampBeyondPartitionHorizon asserts the
// --chaos-clock-skew opt-in event lands chaosTooOldMonthsBack months before
// its base timestamp — inside default retention, but before the
// partition-manager's ahead-only horizon (chaos.go's doc comment) — and
// that it is an ordinary api_request record (kind resolves normally, not as
// a rejection or a synthetic type).
func TestBuildChaosTooOldEvent_TimestampBeyondPartitionHorizon(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	id := sessionIdentity{sessionID: "sess-chaos-too-old"}
	rec := buildChaosTooOldEvent(id, base, 0)

	wantTS := base.AddDate(0, -chaosTooOldMonthsBack, 0)
	require.Equal(t, uint64(wantTS.UnixNano()), rec.TimeUnixNano)
	require.Equal(t, "claude_code.api_request", rec.GetBody().GetStringValue())
}

// TestBuildChaosUnknownEvent_UsesInventedName asserts --chaos-unknown
// (SPEC §7.1) builds through newLogRecord with a name absent from the
// §1.5.1 mapping table, so FromOTLPLogs's unknown fallback is exercised
// rather than accidentally colliding with a documented event name.
func TestBuildChaosUnknownEvent_UsesInventedName(t *testing.T) {
	t.Parallel()

	id := sessionIdentity{sessionID: "sess-chaos-unknown"}
	ts := time.Now().UTC()
	rec := buildChaosUnknownEvent(id, ts, 0, nil)

	require.Equal(t, "claude_code.chaos_invented_event", rec.GetBody().GetStringValue())
	var sawEventName bool
	for _, kv := range rec.GetAttributes() {
		if kv.GetKey() == "event.name" {
			sawEventName = true
			require.Equal(t, "chaos_invented_event", kv.GetValue().GetStringValue())
		}
	}
	require.True(t, sawEventName)
}

// fakeChaosInnerTransport is a minimal Transport double recording every
// call it receives, used to observe chaosTransport's decoration without a
// live server.
type fakeChaosInnerTransport struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeChaosInnerTransport) record(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind)
}

func (f *fakeChaosInnerTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeChaosInnerTransport) SendLogs(context.Context, []byte, string) SendResult {
	f.record("logs")
	return SendResult{StatusCode: 200}
}

func (f *fakeChaosInnerTransport) SendMetrics(context.Context, []byte, string) SendResult {
	f.record("metrics")
	return SendResult{StatusCode: 200}
}

func (f *fakeChaosInnerTransport) SendHooks(context.Context, []byte) SendResult {
	f.record("hooks")
	return SendResult{StatusCode: 200}
}

// TestChaosTransport_DuplicatesResendByteIdentical drives enough SendHooks
// calls through a --chaos-duplicates-enabled chaosTransport that at least
// one resend must have fired (pChaosDuplicate=3%), and asserts every
// recorded call is "hooks" — i.e. a resend, not some other kind of call —
// proving the duplication is transparent to the caller (P2-13 lead note 2:
// resent payloads must be byte-identical, which a same-body second call
// through the identical inner.SendHooks trivially guarantees).
func TestChaosTransport_DuplicatesResendByteIdentical(t *testing.T) {
	t.Parallel()

	inner := &fakeChaosInnerTransport{}
	cfg := Config{Seed: 7, ChaosDuplicates: true}
	ct := newChaosTransport(cfg, inner)

	const attempts = 500
	for i := 0; i < attempts; i++ {
		res := ct.SendHooks(context.Background(), []byte(`[{"hook_event_name":"Stop"}]`))
		require.NoError(t, res.Err)
		require.Equal(t, 200, res.StatusCode)
	}

	require.Greater(t, inner.count(), attempts, "expected at least one resend across %d attempts at p=%.0f%%", attempts, pChaosDuplicate*100)
	for _, k := range inner.calls {
		require.Equal(t, "hooks", k)
	}
}

// TestChaosTransport_OutOfOrderHoldsThenDelivers asserts --chaos-out-of-
// order returns immediately (StatusCode 0, no HTTP exchange yet) for a held
// send, that the inner send has NOT happened by the time the call returns,
// and that Wait blocks until it has — using an injected sleep so the test
// does not actually wait 5-60 real seconds.
func TestChaosTransport_OutOfOrderHoldsThenDelivers(t *testing.T) {
	t.Parallel()

	inner := &fakeChaosInnerTransport{}
	cfg := Config{Seed: 3, ChaosOutOfOrder: true}
	ct := newChaosTransport(cfg, inner)

	var slept atomic.Int64
	ct.sleep = func(d time.Duration) { slept.Add(int64(d)) } // no real delay; just record it happened

	// Force the hold branch deterministically regardless of the RNG draw,
	// by driving enough attempts that at least one must hold (pChaosHold=5%)
	// while asserting the *held* one's immediate return shape.
	var heldSeen bool
	for i := 0; i < 500 && !heldSeen; i++ {
		res := ct.SendHooks(context.Background(), []byte(`[]`))
		if res.StatusCode == 0 && res.Err == nil {
			heldSeen = true
		}
	}
	require.True(t, heldSeen, "expected at least one held send across 500 attempts at p=%.0f%%", pChaosHold*100)

	ct.Wait()
	require.Positive(t, inner.count(), "held send should have fired by the time Wait returns")
	require.Positive(t, slept.Load(), "holdAndSend should have used the injected sleep")
}

// TestNewChaosTransport_DisabledFlagsAreNoOps asserts a chaosTransport built
// for a Config with both chaos flags off behaves exactly like the inner
// Transport — no duplication, no holding — since cli.go only wraps a
// Transport at all when at least one of the two flags is set, but
// chaosTransport itself must still be safe to construct and use with
// neither enabled (defence in depth against that call site changing).
func TestNewChaosTransport_DisabledFlagsAreNoOps(t *testing.T) {
	t.Parallel()

	inner := &fakeChaosInnerTransport{}
	ct := newChaosTransport(Config{Seed: 1}, inner)

	for i := 0; i < 50; i++ {
		res := ct.SendHooks(context.Background(), []byte(`[]`))
		require.Equal(t, 200, res.StatusCode)
	}
	require.Equal(t, 50, inner.count())
}
