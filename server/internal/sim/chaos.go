package sim

// Chaos modes (P2-13, SPEC §7.1). See doc.go's "chaos hooks" for the
// architectural seams: duplicates/out-of-order via Transport wrapper,
// orphans via post-generation slice transform, clock-skew/unknown via
// per-event RNG draws. All five flags are independently switchable and off
// by default.

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Chaos probabilities and magnitudes per SPEC §7.1: duplicates (resend 3%),
// out-of-order (hold 5% for 5-60s), clock-skew (2% ±1h, plus beyond-retention).
const (
	pChaosDuplicate = 0.03
	pChaosHold      = 0.05
	chaosHoldFloor  = 5 * time.Second
	chaosHoldCeil   = 60 * time.Second
	pChaosSkew      = 0.02
	chaosSkewMax    = time.Hour

	// chaosOrphanShift is how many of a session's own turn-hooks its
	// delivered-late SessionStart is moved past (SPEC §7.1:
	// "turn events before SessionStart"). 3 is enough to guarantee at least
	// one full PreToolUse/PostToolUse pair — and therefore a real
	// stub-on-reference session — lands before SessionStart, without
	// requiring every session to have that many hooks (applyChaosOrphans
	// clamps to the slice length when it does not).
	chaosOrphanShift = 3

	// chaosTooOldMonthsBack is how many calendar months back the beyond-retention
	// event is timestamped: different partition than "now", within retention window
	// (SPEC §1.2's clamp leaves it alone). P3-12's backward partition creation
	// means the test injects the fault on storage side (drops the month's partition).
	// Generator's job: emit an in-retention event in a different month.
	chaosTooOldMonthsBack = 2
)

// chaosRand derives a chaos-only RNG stream for one session, independent of
// that session's own content-generation stream (rng.go's sessionRand): a
// distinct high bits fold ("CHOS" ASCII folded into the seed) keeps chaos
// draws from ever advancing the same *rand.Rand a clean run's tool mix,
// token counts, or turn count would consume, so turning a chaos flag on
// never changes what a clean run with the same --seed would otherwise have
// produced — chaos flags inject faults, they do not redefine content.
func chaosRand(seed uint64, sessionOrdinal int) *rand.Rand {
	const chosSalt = 0x43484f53                                         // "CHOS"
	return rand.New(rand.NewPCG(seed^chosSalt, uint64(sessionOrdinal))) //nolint:gosec // sessionOrdinal is always >=0 by construction (loop counter)
}

// applyChaosOrphans implements --chaos-orphans (SPEC §7.1): moves SessionStart
// past next chaosOrphanShift hooks without changing its timestamp, simulating
// late delivery. Early events stub-create the session (rule 1); SessionStart
// arrival triggers dirty-marking per SPEC §2.4 (project/cwd change rule).
func applyChaosOrphans(result sessionResult) sessionResult {
	startIdx := -1
	for i, h := range result.Hooks {
		if h.Payload["hook_event_name"] == "SessionStart" {
			startIdx = i
			break
		}
	}
	if startIdx < 0 || startIdx >= len(result.Hooks)-1 {
		// No SessionStart in this session's hooks (e.g. a legacy-app,
		// metrics-only session), or nothing after it to reorder past —
		// leave the slice untouched rather than manufacture a no-op move.
		return result
	}

	moveTo := startIdx + chaosOrphanShift
	if moveTo >= len(result.Hooks) {
		moveTo = len(result.Hooks) - 1
	}

	reordered := make([]hookEmission, 0, len(result.Hooks))
	reordered = append(reordered, result.Hooks[:startIdx]...)
	reordered = append(reordered, result.Hooks[startIdx+1:moveTo+1]...)
	reordered = append(reordered, result.Hooks[startIdx])
	reordered = append(reordered, result.Hooks[moveTo+1:]...)
	result.Hooks = reordered
	return result
}

// maybeSkewTimestamp implements --chaos-clock-skew's per-event draw (SPEC
// §7.1: "2% ±1h"): with probability pChaosSkew it returns ts shifted by a
// uniform random offset in [-chaosSkewMax, +chaosSkewMax], else ts
// unchanged. Called from sessionBuilder.now() for every event once the flag
// is on, so — like every other §7.1 distribution — it is a per-event coin
// flip, not a per-session one.
func maybeSkewTimestamp(r *rand.Rand, ts time.Time) time.Time {
	if !bernoulli(r, pChaosSkew) {
		return ts
	}
	offset := time.Duration(uniformRange(r, -int(chaosSkewMax), int(chaosSkewMax)))
	return ts.Add(offset)
}

// buildChaosUnknownEvent implements --chaos-unknown (SPEC §7.1: "invented
// event.name -> kind='unknown'"): an ordinary OTel log record built through
// the exact same newLogRecord constructor every other event in this package
// uses (otel_log_events.go), with an event.name the §1.5.1 mapping table
// does not list. FromOTLPLogs's documented fallback stores it as
// kind='unknown' with event_name preserved and attrs intact — never a
// rejection (SPEC §1.4) — so this is data flowing through the taxonomy's
// escape hatch, not a malformed payload.
func buildChaosUnknownEvent(id sessionIdentity, ts time.Time, seq int64, promptID *string) *logspb.LogRecord {
	return newLogRecord(id, ts, seq, "chaos_invented_event", promptID)
}

// buildChaosTooOldEvent implements --chaos-clock-skew's beyond-retention
// event (SPEC §7.1): an api_request timestamped chaosTooOldMonthsBack before
// now. SPEC §1.2's clamp keeps it in-retention unless beyond-retention,
// reaching rule 3 only if its partition is missing (injected by test).
func buildChaosTooOldEvent(id sessionIdentity, ts time.Time, seq int64) *logspb.LogRecord {
	tooOld := ts.AddDate(0, -chaosTooOldMonthsBack, 0)
	return buildAPIRequest(id, tooOld, seq, nil, apiRequestFields{
		model:        "claude-sonnet-4-5",
		inputTokens:  100,
		outputTokens: 50,
		durationMS:   500,
		querySource:  "sdk",
		includeCost:  true,
		costMicros:   1000,
		requestID:    "req_chaos_too_old_" + id.sessionID,
	})
}

// chaosTransport decorates Transport with --chaos-duplicates (resend ~3% of
// sends) and --chaos-out-of-order (hold ~5% for random delay before sending).
// Duplicates are synchronous; held sends are async (goroutines) tracked by wg.
// Wait blocks until all held sends have fired.
type chaosTransport struct {
	Transport
	cfg   Config
	mu    sync.Mutex
	r     *rand.Rand
	wg    sync.WaitGroup
	sleep func(time.Duration)
}

// newChaosTransport wraps inner with chaos flags. RNG is seeded from cfg.Seed
// like chaosRand (per-session), so chaos decisions are reproducible.
func newChaosTransport(cfg Config, inner Transport) *chaosTransport {
	const chosSalt = 0x43484f53 // "CHOS", matching chaosRand's fold
	return &chaosTransport{
		Transport: inner,
		cfg:       cfg,
		r:         rand.New(rand.NewPCG(cfg.Seed^chosSalt, 0)), //nolint:gosec // fixed second stream index; this RNG is process-lifetime, not per-session
		sleep:     time.Sleep,
	}
}

// draw reports whether a chaos event of probability p fires, and — for
// --chaos-out-of-order — the hold duration to use if it does. Both come
// from the same mutex-guarded stream since chaosTransport is shared across
// every session's sends (unlike chaosRand's per-session streams).
func (t *chaosTransport) draw(p float64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return bernoulli(t.r, p)
}

func (t *chaosTransport) holdDuration() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	span := int64(chaosHoldCeil - chaosHoldFloor)
	return chaosHoldFloor + time.Duration(t.r.Int64N(span))
}

// SendLogs implements Transport, decorated per the type doc.
func (t *chaosTransport) SendLogs(ctx context.Context, body []byte, contentType string) SendResult {
	if t.cfg.ChaosOutOfOrder && t.draw(pChaosHold) {
		t.holdAndSend(func() { t.Transport.SendLogs(context.WithoutCancel(ctx), body, contentType) })
		return SendResult{}
	}
	res := t.Transport.SendLogs(ctx, body, contentType)
	if t.cfg.ChaosDuplicates && res.Err == nil && t.draw(pChaosDuplicate) {
		t.Transport.SendLogs(ctx, body, contentType)
	}
	return res
}

// SendMetrics implements Transport, decorated per the type doc.
func (t *chaosTransport) SendMetrics(ctx context.Context, body []byte, contentType string) SendResult {
	if t.cfg.ChaosOutOfOrder && t.draw(pChaosHold) {
		t.holdAndSend(func() { t.Transport.SendMetrics(context.WithoutCancel(ctx), body, contentType) })
		return SendResult{}
	}
	res := t.Transport.SendMetrics(ctx, body, contentType)
	if t.cfg.ChaosDuplicates && res.Err == nil && t.draw(pChaosDuplicate) {
		t.Transport.SendMetrics(ctx, body, contentType)
	}
	return res
}

// SendHooks implements Transport, decorated per the type doc. Hooks are the
// AC's named case (P2-13 lead note 2): the hook dedup key deliberately
// excludes ts (SPEC §1.7 rule 2), so a byte-identical resent hook payload
// is the only kind of duplicate whose suppression proves the ingest_dedup
// ledger — not a ts-bearing unique constraint — is doing the work.
func (t *chaosTransport) SendHooks(ctx context.Context, body []byte) SendResult {
	if t.cfg.ChaosOutOfOrder && t.draw(pChaosHold) {
		t.holdAndSend(func() { t.Transport.SendHooks(context.WithoutCancel(ctx), body) })
		return SendResult{}
	}
	res := t.Transport.SendHooks(ctx, body)
	if t.cfg.ChaosDuplicates && res.Err == nil && t.draw(pChaosDuplicate) {
		t.Transport.SendHooks(ctx, body)
	}
	return res
}

// holdAndSend runs send on its own goroutine after a random
// [chaosHoldFloor, chaosHoldCeil] real delay, tracked by wg so Wait can block
// until every held send has actually fired.
func (t *chaosTransport) holdAndSend(send func()) {
	t.wg.Add(1)
	delay := t.holdDuration()
	go func() {
		defer t.wg.Done()
		t.sleep(delay)
		send()
	}()
}

// Wait blocks until every --chaos-out-of-order held send has fired. A
// no-op transport that never held anything returns immediately.
func (t *chaosTransport) Wait() {
	t.wg.Wait()
}
