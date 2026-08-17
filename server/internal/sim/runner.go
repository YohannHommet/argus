package sim

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Runner wires a Config to a Transport and accumulates a Report — the only
// file in this package where generation (session.go) and delivery
// (transport.go) meet (doc.go's generator/transport split).
type Runner struct {
	Cfg       Config
	Transport Transport
	Report    *Report
}

// NewRunner builds a Runner with a fresh Report.
func NewRunner(cfg Config, transport Transport) *Runner {
	return &Runner{Cfg: cfg, Transport: transport, Report: NewReport()}
}

// Run dispatches to RunDemo or RunLoad by Cfg.Mode.
func (rn *Runner) Run(ctx context.Context) error {
	switch rn.Cfg.Mode {
	case ModeLoad:
		return rn.RunLoad(ctx)
	case ModeDemo:
		return rn.RunDemo(ctx)
	default:
		// cli.go rejects any Mode other than ModeDemo/ModeLoad before a
		// Runner is ever constructed, so this branch is unreachable in
		// practice; it exists only because Mode is backed by a plain
		// string (SPEC §0: no Go enum can reject a value) and the
		// `exhaustive` linter still requires every named case to appear.
		return rn.RunDemo(ctx)
	}
}

// RunDemo implements SPEC §7.2's "--mode=demo … historical timestamps so
// analytics has shape": Cfg.Sessions sessions, generated and sent
// sequentially (never concurrently — lead note 2/AC1: --out's
// byte-identical-output guarantee, and FileTransport's per-session
// directory bookkeeping, both depend on sequential generation; SPEC §7.2
// says concurrency is a live-load-testing knob, not a demo-fidelity one),
// spread across the backfill window so earlier sessions carry older
// timestamps.
func (rn *Runner) RunDemo(ctx context.Context) error {
	origin, err := ResolveClockOrigin(rn.Cfg.ClockOriginRaw, rn.Cfg.Out != "", rn.Cfg.Deterministic, time.Now, rn.Cfg.Backfill)
	if err != nil {
		return fmt.Errorf("sim: resolve clock origin: %w", err)
	}
	clock := NewClock(origin)
	ft, isFile := rn.Transport.(*FileTransport)

	sessions := rn.Cfg.Sessions
	if sessions <= 0 {
		sessions = demoDefaultSessions
	}

	assignment := demoProjectAssignment(rn.Cfg.Seed, sessions)

	for ordinal := 0; ordinal < sessions; ordinal++ {
		startOffset := backfillOffset(ordinal, sessions, rn.Cfg.Backfill)
		project := assignment[ordinal]
		result := generateSession(rn.Cfg, clock, ordinal, startOffset, project)

		if isFile {
			if err := ft.BeginSession(ordinal); err != nil {
				return fmt.Errorf("sim: begin session %d output dir: %w", ordinal, err)
			}
		}
		if err := rn.sendSession(ctx, result, rn.Cfg.FlushImmediately); err != nil {
			return err
		}
		rn.Report.RecordSession()
	}

	rn.Report.Finish()
	return nil
}

// backfillOffset spreads session ordinal 0..sessions-1 evenly across
// [0, backfill] (SPEC §7.2: "--mode=demo … historical timestamps so
// analytics has shape"), so a --sessions=25 --backfill=14d run's oldest
// session starts at origin and its newest starts 14 days later.
func backfillOffset(ordinal, sessions int, backfill time.Duration) time.Duration {
	if sessions <= 1 || backfill <= 0 {
		return 0
	}
	return time.Duration(float64(ordinal) / float64(sessions-1) * float64(backfill))
}

// demoProjectAssignment decides which project each of a demo run's
// `sessions` ordinals gets: a balanced multiset of the fixed §7.1 project
// set, permuted by a seed-derived RNG, with ordinal 0 guaranteed
// logs-capable.
//
// It replaces the previous `projects[ordinal%len(projects)]` round robin
// (ticket W15, "argusd sim --sessions=N undercount"). That cycle put
// legacy-app — the one project SPEC §7.1 makes metrics-only, so its
// sessions correctly produce no `sessions` row — at a *fixed* residue
// (index 4 of 5) depending only on ordinal, never on sessions or seed. So
// whenever --sessions was a multiple of len(projects), ordinal sessions-1
// (the run's last session, whose content is otherwise no thinner than any
// other's — verified: this package's demo path has no time-based emission
// cutoff, so the ledger's `backfillOffset` theory for the symptom does not
// hold) landed on that residue 100% of the time, for every seed.
//
// Why a permuted balanced multiset rather than an independent per-ordinal
// draw: an i.i.d. draw fixes the positional bias but replaces it with
// binomial variance in the *count* of metrics-only sessions, and that count
// is what decides how many `sessions` rows a demo produces. Measured over
// 2000 seeds at the previous 25-session default, 34% of seeds yielded fewer
// than 20 session rows and the worst yielded 15 — against a Phase-4 exit
// criterion that requires the session list to show at least 20 from a demo
// run. Raising the default could not close it: even at 36 sessions the tail
// still dipped to 18. A balanced allocation keeps exactly the share §7.1
// asks for (one project in five is metrics-only, so a run of N yields
// ceil(4N/5) session rows) with no variance at all, while the permutation
// keeps *which* ordinals get it seed-dependent. The demo's data volume
// becomes a property of N alone, which is what makes it safe to assert on.
//
// Ordinal 0 is additionally pinned logs-capable. Two things depend on the
// run's first session being a real logs session: session.go anchors
// --chaos-clock-skew's beyond-retention event on `logsOnly &&
// sessionOrdinal == 0` — "emitted once for the whole run" — so an ordinal 0
// holding the metrics-only project would silently emit no repro at all for
// that seed, with nothing failing to say so; and a demo whose very first
// session contains no events reads as a broken install to whoever is
// watching it arrive.
//
// Determinism: the permutation is driven by sessionRand(seed, 0), a
// dedicated *rand.Rand instance, so it never perturbs the draw sequence
// generateSession makes for any ordinal (two independently-constructed
// rand.Rand values seeded identically advance independently). A single-
// session run keeps projects[0], so the committed golden fixtures are
// unaffected.
func demoProjectAssignment(seed uint64, sessions int) []string {
	if sessions <= 0 {
		return nil
	}

	// Balanced base: exactly the multiset the round robin produced, which is
	// where the "one in five is metrics-only" share comes from.
	assignment := make([]string, sessions)
	for i := range assignment {
		assignment[i] = projects[i%len(projects)]
	}

	r := sessionRand(seed, 0)
	r.Shuffle(len(assignment), func(i, j int) {
		assignment[i], assignment[j] = assignment[j], assignment[i]
	})

	if assignment[0] == legacyAppProject {
		// Swap with the first logs-capable ordinal, preserving the multiset
		// (and therefore the share) exactly.
		for i := 1; i < len(assignment); i++ {
			if assignment[i] != legacyAppProject {
				assignment[0], assignment[i] = assignment[i], assignment[0]
				break
			}
		}
	}

	return assignment
}

// RunLoad implements SPEC §7.2's "--mode=load (--rate=<events/s>
// --concurrency=N --duration=…), live timestamps, for backpressure
// testing". Cfg.Concurrency workers each generate sessions back-to-back
// (ordinals drawn from a shared atomic counter, so two workers never
// collide, and — per rng.go's design goal — each session's *content* is
// still exactly the same as a single-threaded run with the same seed would
// produce for that ordinal); a single shared rate limiter paces every
// individual event send (not batch send: load mode always sends one event
// per request, see sendSession's immediate=true call below) so aggregate
// throughput approximates Cfg.Rate regardless of how many workers are
// contributing to it.
func (rn *Runner) RunLoad(ctx context.Context) error {
	origin, err := ResolveClockOrigin(rn.Cfg.ClockOriginRaw, rn.Cfg.Out != "", rn.Cfg.Deterministic, time.Now, 0)
	if err != nil {
		return fmt.Errorf("sim: resolve clock origin: %w", err)
	}
	clock := NewClock(origin)

	// deadlineCtx bounds every individual event send (via limiter.Wait
	// below), not just the between-sessions loop condition: a session can
	// contain hundreds of events (§7.1's up-to-20-turns, up-to-12-tool-
	// calls-per-turn recipe), and at a modest --rate that alone can take
	// several seconds to drain — checking the deadline only between
	// sessions would let --duration=10s overrun by however long the last
	// session's send takes, which is exactly the "real timing assertion"
	// lead note 4 warns must not be quietly wrong.
	deadlineCtx, cancel := context.WithTimeout(ctx, rn.Cfg.Duration)
	defer cancel()

	limiter := newRateLimiter(rn.Cfg.Rate)
	concurrency := rn.Cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	var ordinalCounter atomic.Int64
	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-deadlineCtx.Done():
					return
				default:
				}
				ordinal := int(ordinalCounter.Add(1) - 1)
				project := projects[ordinal%len(projects)]
				startOffset := time.Since(origin)
				result := generateSession(rn.Cfg, clock, ordinal, startOffset, project)

				err := rn.sendSessionRateLimited(deadlineCtx, result, limiter)
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					return // expected: --duration elapsed mid-session, not a failure
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				rn.Report.RecordSession()
			}
		}()
	}
	wg.Wait()
	rn.Report.Finish()
	return firstErr
}

// sendSession encodes and sends every batch in result (SPEC §7.2's
// logs-5s/metrics-60s/hooks-5s batching, or one-event-per-batch when
// immediate is true).
func (rn *Runner) sendSession(ctx context.Context, result sessionResult, immediate bool) error {
	logBatches := batchByInterval(result.Logs, func(e logEmission) time.Time { return e.TS }, logFlushInterval, immediate)
	for _, batch := range logBatches {
		records := make([]*logspb.LogRecord, len(batch))
		for i, e := range batch {
			records[i] = e.Rec
		}
		body, contentType, err := encodeLogs(rn.Cfg.OTLPProtocol, wrapLogs(result.Identity, records))
		if err != nil {
			return err
		}
		res := rn.Transport.SendLogs(ctx, body, contentType)
		rn.Report.RecordSend("logs", len(batch), res)
	}

	hookBatches := batchByInterval(result.Hooks, func(e hookEmission) time.Time { return e.TS }, hookFlushInterval, immediate)
	for _, batch := range hookBatches {
		payloads := make([]map[string]any, len(batch))
		for i, e := range batch {
			payloads[i] = e.Payload
		}
		body, err := EncodeHookBatch(payloads)
		if err != nil {
			return err
		}
		res := rn.Transport.SendHooks(ctx, body)
		rn.Report.RecordSend("hooks", len(batch), res)
	}

	metricBatches := batchByInterval(result.Metrics, func(e metricEmission) time.Time { return e.TS }, metricFlushInterval, immediate)
	for _, batch := range metricBatches {
		points := make([]*metricspb.Metric, len(batch))
		for i, e := range batch {
			points[i] = e.M
		}
		body, contentType, err := encodeMetrics(rn.Cfg.OTLPProtocol, wrapMetrics(result.Identity, points))
		if err != nil {
			return err
		}
		res := rn.Transport.SendMetrics(ctx, body, contentType)
		rn.Report.RecordSend("metrics", len(batch), res)
	}

	return nil
}

// sendSessionRateLimited is RunLoad's send path: always one event per
// batch (immediate=true) so the shared rateLimiter's per-send wait
// corresponds to a per-event rate, matching Cfg.Rate's "events/s" unit.
func (rn *Runner) sendSessionRateLimited(ctx context.Context, result sessionResult, limiter *rateLimiter) error {
	for _, e := range result.Logs {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}
		body, contentType, err := encodeLogs(rn.Cfg.OTLPProtocol, wrapLogs(result.Identity, []*logspb.LogRecord{e.Rec}))
		if err != nil {
			return err
		}
		res := rn.Transport.SendLogs(ctx, body, contentType)
		rn.Report.RecordSend("logs", 1, res)
	}
	for _, e := range result.Hooks {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}
		body, err := EncodeHookBatch([]map[string]any{e.Payload})
		if err != nil {
			return err
		}
		res := rn.Transport.SendHooks(ctx, body)
		rn.Report.RecordSend("hooks", 1, res)
	}
	for _, e := range result.Metrics {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}
		body, contentType, err := encodeMetrics(rn.Cfg.OTLPProtocol, wrapMetrics(result.Identity, []*metricspb.Metric{e.M}))
		if err != nil {
			return err
		}
		res := rn.Transport.SendMetrics(ctx, body, contentType)
		rn.Report.RecordSend("metrics", 1, res)
	}
	return nil
}

// encodeLogs/encodeMetrics pick protobuf vs JSON encoding by
// Cfg.OTLPProtocol (SPEC §7.2's --otlp-protocol flag) and return the
// matching Content-Type.
func encodeLogs(protocol OTLPProtocol, data *logspb.LogsData) ([]byte, string, error) {
	if protocol == OTLPProtocolJSON {
		b, err := EncodeLogsJSON(data)
		return b, contentTypeJSON, err
	}
	b, err := EncodeLogsProtobuf(data)
	return b, contentTypeProtobuf, err
}

func encodeMetrics(protocol OTLPProtocol, data *metricspb.MetricsData) ([]byte, string, error) {
	if protocol == OTLPProtocolJSON {
		b, err := EncodeMetricsJSON(data)
		return b, contentTypeJSON, err
	}
	b, err := EncodeMetricsProtobuf(data)
	return b, contentTypeProtobuf, err
}

// rateLimiter paces calls to Wait so that, across every goroutine sharing
// one instance, calls happen no more often than 1/rate seconds apart on
// average — a minimal shared pacing primitive standing in for
// golang.org/x/time/rate, which is not a dependency of this module
// (go.mod's pinned dependency set, per this ticket's "do not run go get"
// constraint).
type rateLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

// newRateLimiter builds a limiter for the given events/s target. rate<=0
// disables pacing entirely (Wait always returns immediately) — SPEC's load
// mode always passes an explicit --rate, but a zero value degrading to "as
// fast as possible" rather than dividing by zero is the safer default.
func newRateLimiter(rate float64) *rateLimiter {
	if rate <= 0 {
		return &rateLimiter{interval: 0}
	}
	return &rateLimiter{interval: time.Duration(float64(time.Second) / rate), next: time.Now()}
}

// Wait blocks until this call's turn under the target rate, or ctx is
// cancelled.
func (rl *rateLimiter) Wait(ctx context.Context) error {
	if rl.interval <= 0 {
		return nil
	}
	rl.mu.Lock()
	now := time.Now()
	if rl.next.Before(now) {
		rl.next = now
	}
	wait := rl.next.Sub(now)
	rl.next = rl.next.Add(rl.interval)
	rl.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
