package sim

// Chaos modes (P2-13, SPEC §7.1's five --chaos-* flags). doc.go's "chaos
// hooks" note lists the seams P2-12 deliberately left in place; this file is
// what actually walks through them:
//
//   - --chaos-duplicates / --chaos-out-of-order decorate the Transport
//     interface (chosen over decorating runner.go's send loop directly,
//     which the doc comment describes but which would require editing
//     runner.go for every chaos concern — wrapping Transport gets the same
//     "every encoded payload passes through exactly one seam" property with
//     zero change to runner.go: cli.go swaps the Transport before handing
//     it to NewRunner).
//   - --chaos-orphans is the post-generation slice transform on a session's
//     own Hooks emissions that doc.go names; generateSession (session.go)
//     applies it once a session's full emission set exists.
//   - --chaos-clock-skew and --chaos-unknown are extra draws taken inside
//     sessionBuilder alongside every other §7.1 distribution, using their
//     own RNG stream (chaosRand) so enabling them never perturbs the
//     ordinary event content or ordering a clean run with the same --seed
//     would have produced.
//
// All five flags are independently switchable (Config's doc comment) and
// off by default.

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Chaos probabilities and magnitudes, transcribed verbatim from SPEC §7.1's
// chaos paragraph: "--chaos-duplicates (resend 3%) … --chaos-out-of-order
// (hold 5% for 5-60s) … --chaos-clock-skew (2% ±1h, plus an opt-in
// beyond-retention event)".
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

	// chaosTooOldMonthsBack is how many calendar months before "now" the
	// --chaos-clock-skew opt-in beyond-retention event is timestamped. See
	// buildChaosTooOldEvent's doc comment for why this — not a timestamp
	// that trips the §1.2 clamp — is the reachable path to
	// argus_ingest_too_old_total: 2 months is comfortably inside the
	// default 90-day ARGUS_RETENTION_RAW_DAYS window (so the clamp leaves
	// it untouched) and comfortably outside the "current month + 2 ahead"
	// range internal/app.New and the hourly PartitionJob ensure — Argus
	// never creates a partition *behind* the current month, only ahead of
	// it (SPEC §2.4's partition-manager job), so a legitimately-in-window
	// event from two months ago has no partition to land in.
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

// applyChaosOrphans implements --chaos-orphans (SPEC §7.1: "turn events
// before SessionStart -> stub-on-reference and the late-project rollup
// re-mark"). It is a pure post-generation transform on the already-fully-
// generated per-session Hooks slice (doc.go's chaos-hooks note): it moves
// the SessionStart hook payload past this session's next chaosOrphanShift
// hooks, without changing SessionStart's own timestamp — it genuinely
// happened first, it is merely *delivered* late, exactly like a real slow
// hook subprocess would.
//
// Every event ingested before the delayed SessionStart lands stub-creates
// the session via rule 1 (status='unknown', started_at NULL) with
// project="" (only SessionStart's cwd feeds the project projection,
// upsert_session.go). When SessionStart finally lands it both fills
// started_at (healing the stub) and changes project from "" to a real
// value, which is exactly the SPEC §2.4 second dirty-marking rule's
// trigger: "when a session's project or cwd changes … the session upsert
// marks every hour bucket from first_seen_at to last_event_at dirty".
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

// buildChaosTooOldEvent implements --chaos-clock-skew's opt-in
// beyond-retention event (SPEC §7.1). It is an ordinary api_request record
// (same builder, same attribute set every other api_request in this package
// emits) whose event.timestamp/TimeUnixNano is set chaosTooOldMonthsBack
// calendar months before ts.
//
// This — not a timestamp that trips the §1.2 clamp — is the reachable path
// to argus_ingest_too_old_total (P2-13's live-run finding): the clamp
// rewrites any ts outside [now-retention, now+1h] to ingested_at *before*
// storage ever sees it, so a timestamp chosen to be "beyond retention" can
// never itself reach a missing partition — it gets normalized to "now" and
// lands in a partition that certainly exists. What reaches rule 3
// (§1.7: "no DEFAULT partition") is an event that is genuinely inside the
// retention window (so the clamp leaves it alone) but in a calendar month
// the partition manager has not created, because §2.4's partition-manager
// job only ever creates partitions for the current month and up to two
// months *ahead* — never behind it.
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

// chaosTransport decorates a Transport with --chaos-duplicates (resend
// ~pChaosDuplicate of sends, byte-identical) and --chaos-out-of-order (hold
// ~pChaosHold of sends for a random real delay in [chaosHoldFloor,
// chaosHoldCeil] before delivering them). Both act on the already-encoded
// wire payload, after generation and immediately around the real
// Transport.Send* call — doc.go's "runner.go's send loop is the single
// place every encoded payload passes through before Transport.Send"; since
// runner.go always calls through the Transport interface and never a
// concrete type, wrapping that interface here is that single seam, with no
// change to runner.go itself.
//
// Duplicate resends are synchronous (folded into the same Send* call the
// caller made) so they are guaranteed to have landed by the time that call
// returns. Held sends are asynchronous (a goroutine tracked by wg) since
// their entire point is to arrive *after* the caller has moved on; Wait
// blocks until every held send has actually fired, for a caller (cli.go)
// that needs the run's true end state before reporting or returning.
type chaosTransport struct {
	Transport
	cfg   Config
	mu    sync.Mutex
	r     *rand.Rand
	wg    sync.WaitGroup
	sleep func(time.Duration)
}

// newChaosTransport wraps inner with --chaos-duplicates/--chaos-out-of-order
// per cfg. Its RNG stream is seeded from cfg.Seed the same way chaosRand
// derives per-session streams, so a given --seed's chaos decisions are
// themselves reproducible even though the *content* being duplicated/held
// is a live HTTP exchange and therefore not byte-for-byte deterministic in
// wall-clock terms.
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
