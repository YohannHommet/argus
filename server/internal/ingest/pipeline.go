// Package ingest implements Argus's ingestion pipeline (docs/SPEC.md §3.6):
// a bounded, non-blocking batch queue drained by N workers that accumulate
// events into store.Writer.WriteBatch calls, retrying transient failures
// per class and never blocking the request goroutine that called Enqueue.
//
// depguard (SPEC §3.1): ingest may import internal/store and internal/model
// (plus stdlib and prometheus) and nothing else — never internal/httpapi or
// internal/query. That is deliberate: the receivers (P2-10 OTLP, P2-11
// hooks) depend on ingest's Enqueue* API, never the other way around, so
// this package cannot know or care how its input arrived over HTTP.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// ErrQueueFull is returned by EnqueueEvents/EnqueueMetrics when that lane's
// queue is at capacity (SPEC §3.6: "Full queue = load shed with a correct
// HTTP status, always"). Callers map it to a 503 + Retry-After for OTLP
// (SPEC §3.4) or a 429 for hooks (SPEC §3.5). Enqueue never blocks to avoid
// this error, and the queue keeps rejecting with it after Close begins
// draining (there is no separate "closed" error: a closing pipeline behaves
// exactly like a full one from a caller's point of view).
var ErrQueueFull = errors.New("ingest: queue full")

// ErrDrainDeadlineExceeded is returned by Close when ctx expires before
// every worker finished flushing its buffered batches (SPEC §3.8: "Exit 0
// only if the drain completed; 1 if events were dropped"). internal/app's
// shutdown sequence treats a non-nil Close error as the signal to exit
// non-zero.
var ErrDrainDeadlineExceeded = errors.New("ingest: drain deadline exceeded")

// saturationThreshold is the fraction of queue capacity GET /readyz
// considers "saturated" (SPEC §3.8's third readiness condition). 0.9 rather
// than 1.0: a queue that is merely full already sheds load correctly via
// ErrQueueFull, so waiting for literal 100% before failing readiness would
// let a load balancer keep sending traffic right up to the edge of loss.
// 0.9 gives a 10% margin to fail out of rotation before that happens, while
// staying high enough that normal batch-to-batch depth variance under
// healthy load never flaps readiness.
const saturationThreshold = 0.9

// PipelineConfig mirrors the ARGUS_INGEST_* keys in internal/config (SPEC
// §3.7); internal/app maps config.Config's fields onto this struct 1:1 so
// ingest never imports internal/config directly (same rationale as
// postgres.WithRollupSessionRemarkMax: the config seam belongs to app).
// Zero-value fields are filled with SPEC §3.6's defaults by applyDefaults,
// so tests can set only the fields they care about.
type PipelineConfig struct {
	QueueCap       int           // ARGUS_INGEST_QUEUE, batches per lane
	Workers        int           // ARGUS_INGEST_WORKERS, event lane only (see runMetricWorker doc)
	BatchSize      int           // ARGUS_INGEST_BATCH_SIZE
	FlushInterval  time.Duration // ARGUS_INGEST_FLUSH
	RetryConflict  int           // ARGUS_INGEST_RETRY_CONFLICT
	RetryTransient int           // ARGUS_INGEST_RETRY_TRANSIENT

	// WriteTimeout bounds a single store.Writer.WriteBatch/WriteMetrics
	// attempt (audit finding M6's ingest half; the companion lock_timeout
	// half lives in store/postgres/pool.go, owned by a different ticket).
	// Without it every attempt ran on p.ctx — a plain WithCancel(Background)
	// with no deadline — so a statement blocked on a lock parked a worker
	// indefinitely and retry classification never ran, because the call
	// never returned. retryLoop wraps each attempt in
	// context.WithTimeout(p.ctx, WriteTimeout); DeadlineExceeded is already
	// classified transient by ClassifyError, so a timed-out attempt retries
	// exactly like any other transient failure. ARGUS_INGEST_WRITE_TIMEOUT
	// and wiring cfg.IngestWriteTimeout onto this field are a separate
	// ticket's job (internal/config, internal/app) — this field's only
	// contract is "zero value means applyDefaults' 30s".
	WriteTimeout time.Duration
}

func (c *PipelineConfig) applyDefaults() {
	if c.QueueCap <= 0 {
		c.QueueCap = 1024
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 500
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 250 * time.Millisecond
	}
	if c.RetryConflict <= 0 {
		c.RetryConflict = 8
	}
	if c.RetryTransient <= 0 {
		c.RetryTransient = 3
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 30 * time.Second
	}
}

// Publisher is the seam Phase 4/5's SSE hub fills in (SPEC §5.3): Publish is
// called once per successful flush, after the write transaction has
// committed, with only the events store.Writer actually persisted — never
// deduped ones, never on a dropped batch. The pipeline ships NoopPublisher
// as the default so P2-09 has no forward dependency on the hub.
//
// # Contract Publish must uphold (audit finding m7-minor)
//
// The pipeline hands every persisted flush to Publish through a small
// buffered goroutine (handoffPublish/runPublishWorker), not inline on the
// flushing worker — so the guarantees below are two-sided: what the
// pipeline promises the hub, and what Publish must promise the pipeline in
// return.
//
//   - Ordering: within one flush's []model.Event, order is preserved
//     end-to-end (Publish sees the same order matchPersisted produced,
//     which is (ts, seq) order — see its doc). Across flushes, calls are
//     handed off in the order flushes complete, but multiple event workers
//     flush concurrently (SPEC §3.6: "parallel workers interleave"), so
//     Publish must not assume cross-flush ordering, only within-flush
//     ordering.
//   - Non-blocking, mandatory: Publish must return quickly and must never
//     block on a slow subscriber. The pipeline's handoff channel is
//     bounded; if it is full, the pipeline drops the publish job (logged,
//     accounted) rather than let a slow hub become ingest back-pressure.
//     But that only protects the *handoff* — once a job is handed to the
//     publish goroutine, a Publish call that itself blocks forever stalls
//     every publish behind it and leaks that one goroutine permanently
//     (Close cannot force a foreign call to return). The hub owns its own
//     internal fan-out/timeout to its subscribers; it must never make that
//     the pipeline's problem.
//   - Panic safety: the pipeline recovers a panic from each Publish call
//     and logs it — a hub bug can drop a batch of published events, it
//     cannot kill the pipeline's worker or crash the process.
//   - Lifetime relative to Close(): Publish may still be called after
//     Close(ctx) has returned (Close does not wait for the publish
//     goroutine to drain), because Close's job is draining the write path,
//     not the stream. A hub must tolerate being called after it considers
//     itself shut down, or must simply outlive the pipeline in practice
//     (Phase 5's expected wiring: the hub owns a longer lifetime than any
//     one Pipeline).
type Publisher interface {
	Publish(events []model.Event)
}

// NoopPublisher discards every event. It is Pipeline's default Publisher
// until a real one is wired in (Phase 4).
type NoopPublisher struct{}

// Publish implements Publisher by doing nothing.
func (NoopPublisher) Publish([]model.Event) {}

// SleepFunc lets Close's caller-facing deadline and the retry backoffs be
// tested without waiting on a real clock: pipeline_test.go injects a
// SleepFunc that still honours ctx cancellation (so the drain-deadline test
// remains meaningful) but never actually sleeps for the computed duration.
type SleepFunc func(ctx context.Context, d time.Duration) error

// defaultSleep is the production SleepFunc: a cancellable real-time sleep.
func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// options collects Option values before Pipeline construction, so
// unexported fields never need to be exposed just for New's sake.
type options struct {
	registerer prometheus.Registerer
	logger     *slog.Logger
	publisher  Publisher
	sleep      SleepFunc
}

// Option configures optional Pipeline dependencies. The zero value of every
// option is production-safe: New defaults registerer to
// prometheus.DefaultRegisterer, logger to slog.Default(), publisher to
// NoopPublisher, and sleep to defaultSleep.
type Option func(*options)

// WithRegisterer overrides the Prometheus registerer NewMetrics registers
// against. Tests that construct more than one Pipeline in the same process
// must supply a fresh prometheus.NewRegistry() each time (lead decision #5)
// — the package default, prometheus.DefaultRegisterer, is a process-global
// and panics on a duplicate metric name.
func WithRegisterer(r prometheus.Registerer) Option {
	return func(o *options) { o.registerer = r }
}

// WithLogger overrides the *slog.Logger every drop/retry/permanent-failure
// path logs to (lead decision #4: nothing is silently dropped).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithPublisher overrides the Publisher seam (default NoopPublisher).
func WithPublisher(p Publisher) Option {
	return func(o *options) { o.publisher = p }
}

// WithSleep overrides the retry-backoff/deadline sleep function. Tests use
// this to make retries instant while still respecting context
// cancellation, per SPEC's "tests must not sleep for real backoff
// durations" requirement.
func WithSleep(f SleepFunc) Option {
	return func(o *options) { o.sleep = f }
}

// Pipeline is the SPEC §3.6 ingestion pipeline: two lanes (events, metric
// samples), each a bounded channel of pre-formed batches drained by worker
// goroutines that accumulate into store.Writer calls. Construct with New,
// which starts the workers immediately; call Close to drain and stop them.
type Pipeline struct {
	cfg       PipelineConfig
	store     store.Writer
	metrics   *Metrics
	logger    *slog.Logger
	publisher Publisher
	sleep     SleepFunc

	events    chan []model.Event
	metricsCh chan []model.MetricSample

	// publishCh is the m7-minor hand-off queue between a flush and
	// Publisher.Publish: flushEvents no longer calls Publish inline on the
	// worker goroutine (see Publisher's doc for the contract this
	// implements). Bounded at cfg.QueueCap, same shock-absorber reasoning
	// as the events/metricsCh lanes; a full channel drops the job (logged,
	// accounted) rather than block the flushing worker.
	publishCh chan []model.Event

	// ctx/cancel scope every store.Writer call a worker makes. cancel is
	// invoked exactly once, by Close, only if the drain deadline is
	// exceeded — this is what lets a worker blocked inside a
	// context-respecting store call (e.g. a real pgx query, or a test fake
	// that blocks on <-ctx.Done()) unblock and exit instead of leaking,
	// even though Close itself has already returned an error by then.
	ctx    context.Context
	cancel context.CancelFunc

	stopCh  chan struct{}
	closing atomic.Bool
	wg      sync.WaitGroup

	// closeMu is m5's fix for the enqueue/drain race: EnqueueEvents/
	// EnqueueMetrics check p.closing and then send on the buffered channel
	// as two steps that used to be unsynchronised, so a producer that read
	// closing==false right before Close ran could still land its batch on
	// the channel *after* every worker had already exited its final drain
	// loop — silently lost (never written, never counted), with a nil
	// error returned to the caller and Close having already returned nil.
	// Enqueue* holds closeMu for reading across its whole check+send;
	// Close acquires it for writing (blocking until any in-flight Enqueue
	// finishes its send) before closing stopCh, so a send that commits
	// always does so strictly before the final drain begins, and any
	// Enqueue that starts after stopCh closes always observes
	// closing==true.
	closeMu sync.RWMutex

	// testAfterClosingCheck, when non-nil, runs inside EnqueueEvents/
	// EnqueueMetrics immediately after the closing check, while still
	// holding closeMu for reading — solely so pipeline_internal_test.go
	// (m5's regression test) can force the check-then-send interleaving
	// deterministically instead of relying on a real race to reproduce.
	// Production code never sets it.
	testAfterClosingCheck func()
}

// New constructs a Pipeline over w and starts its worker goroutines
// immediately (SPEC §3.6's diagram has no separate "armed but not running"
// state). Call Close when done to drain and stop them.
func New(w store.Writer, cfg PipelineConfig, opts ...Option) *Pipeline {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.registerer == nil {
		o.registerer = prometheus.DefaultRegisterer
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	if o.publisher == nil {
		o.publisher = NoopPublisher{}
	}
	if o.sleep == nil {
		o.sleep = defaultSleep
	}

	cfg.applyDefaults()

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{
		cfg:       cfg,
		store:     w,
		metrics:   NewMetrics(o.registerer),
		logger:    o.logger,
		publisher: o.publisher,
		sleep:     o.sleep,
		events:    make(chan []model.Event, cfg.QueueCap),
		metricsCh: make(chan []model.MetricSample, cfg.QueueCap),
		publishCh: make(chan []model.Event, cfg.QueueCap),
		ctx:       ctx,
		cancel:    cancel,
		stopCh:    make(chan struct{}),
	}
	p.start()
	return p
}

// Metrics exposes the Prometheus collectors this Pipeline registered, for
// tests (prometheus/client_golang/prometheus/testutil) and any future
// admin surface. It is the same *Metrics instance NewMetrics returned.
func (p *Pipeline) Metrics() *Metrics { return p.metrics }

// QueueSaturated reports whether either lane has crossed saturationThreshold
// of its configured capacity. GET /readyz (internal/httpapi, via the
// QueueSaturationChecker port) fails readiness while this is true, per SPEC
// §3.8's third readiness condition.
func (p *Pipeline) QueueSaturated() bool {
	return laneSaturated(len(p.events), cap(p.events)) || laneSaturated(len(p.metricsCh), cap(p.metricsCh))
}

func laneSaturated(depth, capacity int) bool {
	if capacity <= 0 {
		return false
	}
	return float64(depth)/float64(capacity) >= saturationThreshold
}

// QueueDepth reports how many batches are currently buffered across both
// lanes (SPEC §5.1's stats frame `queue_depth` field) — the exact same
// len(p.events)+len(p.metricsCh) lanes QueueSaturated reads, so a caller
// building a stream.Snapshot (internal/app) never has to duplicate that
// arithmetic or drift from it.
func (p *Pipeline) QueueDepth() int {
	return len(p.events) + len(p.metricsCh)
}

// EnqueueEvents hands a request's worth of normalized events to the event
// lane, without blocking (SPEC §3.6). An empty batch is a no-op success —
// normalize.go's job is to never hand ingest zero events for a non-empty
// request, but a defensive check here costs nothing. Returns ErrQueueFull
// if the lane is at capacity or the pipeline is draining.
func (p *Pipeline) EnqueueEvents(batch []model.Event) error {
	if len(batch) == 0 {
		return nil
	}
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()
	if p.closing.Load() {
		p.dropEvents(batch, "pipeline closing")
		return ErrQueueFull
	}
	if p.testAfterClosingCheck != nil {
		p.testAfterClosingCheck()
	}
	select {
	case p.events <- batch:
		p.metrics.QueueDepth.WithLabelValues("event").Set(float64(len(p.events)))
		return nil
	default:
		p.dropEvents(batch, "queue full")
		return ErrQueueFull
	}
}

// EnqueueMetrics is EnqueueEvents' counterpart for the metric-sample lane
// (lead decision #2): SPEC §3.6's diagram only draws the event lane, but
// store.Writer.WriteMetrics exists because OTLP metric data points need
// somewhere to go too (SPEC §1.8/§2.3). A second channel, rather than a
// tagged-union batch on the single event channel, because the two payload
// types (model.Event vs model.MetricSample) have nothing in common a shared
// wire type would simplify — tagging would only add a runtime type switch
// everywhere the event lane doesn't need one, for a lane whose volume and
// operational profile (OTLP metric data points, always source=otel_metric)
// is simple enough not to need its own worker pool sized by
// ARGUS_INGEST_WORKERS (see runMetricWorker's single goroutine).
func (p *Pipeline) EnqueueMetrics(batch []model.MetricSample) error {
	if len(batch) == 0 {
		return nil
	}
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()
	if p.closing.Load() {
		p.dropMetrics(batch, "pipeline closing")
		return ErrQueueFull
	}
	if p.testAfterClosingCheck != nil {
		p.testAfterClosingCheck()
	}
	select {
	case p.metricsCh <- batch:
		p.metrics.QueueDepth.WithLabelValues("metric").Set(float64(len(p.metricsCh)))
		return nil
	default:
		p.dropMetrics(batch, "queue full")
		return ErrQueueFull
	}
}

// Close stops accepting new batches and blocks until every worker has
// flushed whatever it already had buffered, or until ctx is done —
// whichever comes first (SPEC §3.8 step 3). It is idempotent: a second call
// returns nil immediately without re-draining. A non-nil return means the
// deadline was hit before the drain completed, which is exactly the signal
// internal/app.shutdown uses to make the process exit non-zero (SPEC §3.8:
// "exit 0 only if the drain completed; 1 if events were dropped").
func (p *Pipeline) Close(ctx context.Context) error {
	if !p.closing.CompareAndSwap(false, true) {
		return nil
	}
	// m5: block until any Enqueue* that already passed the closing check
	// finishes landing its send, so stopCh never closes mid-send — see
	// closeMu's doc on Pipeline and awaitEnqueueBarrier's doc.
	p.awaitEnqueueBarrier()
	close(p.stopCh)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		// Safe here and only here: p.wg covers every goroutine that can
		// call handoffPublish (the event/metric workers), so once it's
		// done no send to publishCh can still be in flight — closing it
		// cannot race a send, regardless of whether Close itself already
		// returned via the ctx.Done() branch below. runPublishWorker's
		// range loop drains whatever is already buffered, then exits: no
		// goroutine leak on the common path, and no leak on the
		// deadline-exceeded path either, once the cancelled workers
		// actually unwind (m7-minor).
		close(p.publishCh)
		close(done)
	}()

	select {
	case <-done:
		p.cancel()
		return nil
	case <-ctx.Done():
		// Cancelling here is what lets a worker stuck inside a
		// context-respecting store call unblock and exit after Close has
		// already returned — see the ctx/cancel doc on Pipeline.
		p.cancel()
		return fmt.Errorf("%w: %w", ErrDrainDeadlineExceeded, ctx.Err())
	}
}

// awaitEnqueueBarrier blocks until every EnqueueEvents/EnqueueMetrics call
// that already passed its closing check has finished landing its send (m5):
// Enqueue* holds closeMu for reading across its whole check+send, so
// acquiring it for writing here returns only once none of them are
// in-flight, at which point stopCh is safe to close — any Enqueue* that
// starts afterwards will see p.closing already true.
func (p *Pipeline) awaitEnqueueBarrier() {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
}

func (p *Pipeline) start() {
	p.wg.Add(p.cfg.Workers)
	for i := 0; i < p.cfg.Workers; i++ {
		go p.runEventWorker()
	}
	// A single goroutine, not p.cfg.Workers of them: the metric lane has no
	// SPEC-mandated worker count and OTLP metric volume in practice is a
	// small fraction of log-event volume, so one accumulator is the
	// simplest thing that is still honestly correct (lead decision #2).
	p.wg.Add(1)
	go p.runMetricWorker()

	// Not added to p.wg (m7-minor): it must outlive the event/metric
	// workers, since Close's drain-done goroutine closes publishCh only
	// after p.wg.Wait() returns — adding this goroutine to the same
	// WaitGroup would deadlock that close against itself. It terminates on
	// its own once publishCh is closed and drained.
	go p.runPublishWorker()
}

// runEventWorker accumulates batches off the shared event channel until
// ARGUS_INGEST_BATCH_SIZE or ARGUS_INGEST_FLUSH, then calls flushEvents.
// Multiple workers read the same channel, so batches from concurrent
// producers interleave across workers by design (SPEC §3.6's "seq is
// assigned by Postgres, so parallel workers interleave" note) — nothing
// here tries to give one worker exclusive ownership of ordering.
func (p *Pipeline) runEventWorker() {
	defer p.wg.Done()

	ctx := p.ctx
	buf := make([]model.Event, 0, p.cfg.BatchSize)
	timer := time.NewTimer(p.cfg.FlushInterval)
	defer timer.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		p.flushEvents(ctx, buf)
		buf = buf[:0]
	}

	for {
		select {
		case batch := <-p.events:
			buf = append(buf, batch...)
			p.metrics.QueueDepth.WithLabelValues("event").Set(float64(len(p.events)))
			if len(buf) >= p.cfg.BatchSize {
				flush()
				resetTimer(timer, p.cfg.FlushInterval)
			}
		case <-timer.C:
			flush()
			timer.Reset(p.cfg.FlushInterval)
		case <-p.stopCh:
			// EnqueueEvents already refuses new sends once closing is set,
			// so draining whatever is already sitting in the channel here
			// terminates: it cannot grow after this point. M7: chunk on
			// BatchSize exactly like the steady-state case above — without
			// this, a full QueueCap backlog coalesces into one unbounded
			// WriteBatch transaction, so a single failure or an overrun
			// drain deadline loses the *entire* backlog instead of just the
			// last partial batch.
			for {
				select {
				case batch := <-p.events:
					buf = append(buf, batch...)
					if len(buf) >= p.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// runMetricWorker is runEventWorker's counterpart for the metric-sample
// lane (see EnqueueMetrics' doc for why it is a single goroutine).
func (p *Pipeline) runMetricWorker() {
	defer p.wg.Done()

	ctx := p.ctx
	buf := make([]model.MetricSample, 0, p.cfg.BatchSize)
	timer := time.NewTimer(p.cfg.FlushInterval)
	defer timer.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		p.flushMetrics(ctx, buf)
		buf = buf[:0]
	}

	for {
		select {
		case batch := <-p.metricsCh:
			buf = append(buf, batch...)
			p.metrics.QueueDepth.WithLabelValues("metric").Set(float64(len(p.metricsCh)))
			if len(buf) >= p.cfg.BatchSize {
				flush()
				resetTimer(timer, p.cfg.FlushInterval)
			}
		case <-timer.C:
			flush()
			timer.Reset(p.cfg.FlushInterval)
		case <-p.stopCh:
			// M7: same BatchSize chunking as runEventWorker's drain loop —
			// see its doc for why an unchunked drain risks the whole
			// backlog on one failed write.
			for {
				select {
				case batch := <-p.metricsCh:
					buf = append(buf, batch...)
					if len(buf) >= p.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// resetTimer drains a possibly-already-fired timer before Reset, per the
// standard library's documented time.Timer.Reset caveat.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// runPublishWorker is the m7-minor hand-off consumer: it is the only
// goroutine that ever calls Publisher.Publish, so a slow or misbehaving hub
// affects publishing latency, never the flushing workers that feed
// publishCh. It exits once publishCh is closed and drained (see Close's
// doc on when that happens).
func (p *Pipeline) runPublishWorker() {
	for events := range p.publishCh {
		p.publishOne(events)
	}
}

// publishOne calls Publisher.Publish with a recover guarding the call
// (m7-minor's panic-safety half of the contract): a hub bug can lose the
// batch of events it panicked on, it cannot take down the publish
// goroutine (which would otherwise silently stop all future publishing for
// the life of the process) or the process itself.
func (p *Pipeline) publishOne(events []model.Event) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("ingest: publisher panicked, dropping this batch from the stream",
				"count", len(events), "panic", r)
		}
	}()
	p.publisher.Publish(events)
}

// handoffPublish hands persisted events to the publish goroutine without
// blocking the flushing worker (m7-minor's non-blocking half of the
// contract): a full publishCh means the hub (or the goroutine feeding it)
// is behind, and the fix for that is dropping this batch from the stream,
// logged and accounted, never stalling ingest — a hub outage must stay a
// hub outage, not become 503s on the write path.
func (p *Pipeline) handoffPublish(events []model.Event) {
	select {
	case p.publishCh <- events:
	default:
		// Not p.metrics.Dropped: that counter's documented meaning is
		// "never made it to storage" (metrics.go), and these events are
		// already committed — they only missed the stream. Accounting for
		// this drop is the log line itself (count, first event's session);
		// adding a dedicated Prometheus series for it is metrics.go's call,
		// outside this ticket's file set.
		first := events[0]
		p.logger.Warn("ingest: publish handoff full, dropping batch from stream",
			"count", len(events), "session_id", first.SessionID)
	}
}

// flushEvents writes one accumulated event batch through the retry loop and
// records every metric SPEC §3.6 names for a successful write, or the drop
// counters if the retry budget was exhausted.
func (p *Pipeline) flushEvents(ctx context.Context, batch []model.Event) {
	descriptor := eventBatchDescriptor(batch)
	start := time.Now()
	res, ok := p.retryLoop(ctx, descriptor, len(batch), func(attemptCtx context.Context) (store.BatchResult, error) {
		return p.store.WriteBatch(attemptCtx, batch)
	})
	if !ok {
		p.dropEvents(batch, "write failed permanently or exhausted its retry budget")
		return
	}

	p.metrics.WriteDuration.Observe(time.Since(start).Seconds())
	p.metrics.BatchSize.Observe(float64(len(batch)))
	p.metrics.Deduped.Add(float64(res.Deduped))
	if res.TooOld > 0 {
		p.metrics.TooOld.Add(float64(res.TooOld))
		p.logger.Warn("ingest: batch contained too-old events, rejected", "count", res.TooOld)
	}

	bySource := make(map[model.Source]int, 2)
	for _, e := range batch {
		bySource[e.Source]++
	}
	for src, n := range bySource {
		p.metrics.Events.WithLabelValues(string(src)).Add(float64(n))
	}
	for _, e := range batch {
		if !e.IngestedAt.IsZero() {
			p.metrics.Lag.Observe(e.IngestedAt.Sub(e.TS).Seconds())
		}
	}

	if persisted := matchPersisted(batch, res.EventRefs); len(persisted) > 0 {
		p.handoffPublish(persisted)
	}
}

// eventBatchDescriptor summarizes batch for a drop/retry log line (m8 fix).
// The obvious choice, batch[0].ID, is always "": no normalizer mints
// Event.ID, it comes from the events table's uuidv7() column default
// (events.go's package doc), so logging it produced first_id="" in exactly
// the line an operator needs after a whole-batch loss. This logs
// identifiers the batch actually carries instead — the first event's
// session/dedup_key/event_name, plus the ts range across the whole batch —
// rather than minting ids in a normalizer, which is outside this ticket's
// file set.
func eventBatchDescriptor(batch []model.Event) string {
	if len(batch) == 0 {
		return "empty batch"
	}
	minTS, maxTS := batch[0].TS, batch[0].TS
	for _, e := range batch[1:] {
		if e.TS.Before(minTS) {
			minTS = e.TS
		}
		if e.TS.After(maxTS) {
			maxTS = e.TS
		}
	}
	first := batch[0]
	return fmt.Sprintf("session_id=%s dedup_key=%s event_name=%s ts=[%s,%s]",
		first.SessionID, first.DedupKey, first.EventName,
		minTS.Format(time.RFC3339Nano), maxTS.Format(time.RFC3339Nano))
}

// flushMetrics is flushEvents' counterpart for the metric-sample lane. It
// never calls Publisher.Publish: SPEC §5.3's stream hub publishes
// model.Event only, and metric samples have no SSE representation in v1.
func (p *Pipeline) flushMetrics(ctx context.Context, batch []model.MetricSample) {
	descriptor := ""
	if len(batch) > 0 {
		descriptor = "name=" + batch[0].Name
	}
	start := time.Now()
	res, ok := p.retryLoop(ctx, descriptor, len(batch), func(attemptCtx context.Context) (store.BatchResult, error) {
		return p.store.WriteMetrics(attemptCtx, batch)
	})
	if !ok {
		p.dropMetrics(batch, "write failed permanently or exhausted its retry budget")
		return
	}

	p.metrics.WriteDuration.Observe(time.Since(start).Seconds())
	p.metrics.BatchSize.Observe(float64(len(batch)))
	p.metrics.Deduped.Add(float64(res.Deduped))
	if res.TooOld > 0 {
		p.metrics.TooOld.Add(float64(res.TooOld))
		p.logger.Warn("ingest: batch contained too-old metric samples, rejected", "count", res.TooOld)
	}
	p.metrics.Events.WithLabelValues(string(model.SourceOTelMetric)).Add(float64(len(batch)))
	for _, s := range batch {
		if !s.IngestedAt.IsZero() {
			p.metrics.Lag.Observe(s.IngestedAt.Sub(s.TS).Seconds())
		}
	}
}

// retryLoop is the shared mechanics behind flushEvents/flushMetrics: call
// write, classify any error, and either retry (sleeping the class's
// backoff schedule), drop, or succeed. "budget N" means N total attempts
// including the first, matching SPEC §3.6's "up to N attempts" phrasing —
// e.g. with the default ARGUS_INGEST_RETRY_CONFLICT=8, a conflict error on
// every attempt but the 8th means exactly 8 calls to write, the 8th one
// succeeding, no data lost.
func (p *Pipeline) retryLoop(
	ctx context.Context,
	descriptor string,
	count int,
	write func(ctx context.Context) (store.BatchResult, error),
) (store.BatchResult, bool) {
	var conflictAttempts, transientAttempts int
	for {
		// M6: each attempt gets its own deadline off ctx (the worker's copy
		// of p.ctx, passed in by flushEvents/flushMetrics) rather than
		// running on it directly, which never times out on its own — see
		// Pipeline.ctx's doc. Without this, a write blocked on a lock parked
		// the worker forever and retry classification never ran, because
		// write() never returned. context.DeadlineExceeded is already
		// classified ClassTransient below, so a timed-out attempt retries
		// exactly like any other transient failure.
		attemptCtx, cancel := context.WithTimeout(ctx, p.cfg.WriteTimeout)
		res, err := write(attemptCtx)
		cancel()
		if err == nil {
			return res, true
		}

		switch ClassifyError(err) {
		case ClassPermanent:
			p.logger.Error("ingest: permanent write error, dropping batch",
				"batch", descriptor, "count", count, "error", err)
			p.metrics.WriteFailed.WithLabelValues(ClassPermanent.String()).Inc()
			return store.BatchResult{}, false

		case ClassConflict:
			conflictAttempts++
			if conflictAttempts >= p.cfg.RetryConflict {
				p.logger.Error("ingest: conflict retry budget exhausted, dropping batch",
					"batch", descriptor, "count", count, "attempts", conflictAttempts, "error", err)
				p.metrics.WriteFailed.WithLabelValues(ClassConflict.String()).Inc()
				return store.BatchResult{}, false
			}
			p.metrics.Retries.WithLabelValues(ClassConflict.String()).Inc()
			if serr := p.sleep(ctx, conflictBackoff(conflictAttempts)); serr != nil {
				p.logger.Error("ingest: retry backoff interrupted, dropping batch",
					"batch", descriptor, "count", count, "error", serr)
				p.metrics.WriteFailed.WithLabelValues(ClassConflict.String()).Inc()
				return store.BatchResult{}, false
			}

		case ClassTransient:
			transientAttempts++
			if transientAttempts >= p.cfg.RetryTransient {
				p.logger.Error("ingest: transient retry budget exhausted, dropping batch",
					"batch", descriptor, "count", count, "attempts", transientAttempts, "error", err)
				p.metrics.WriteFailed.WithLabelValues(ClassTransient.String()).Inc()
				return store.BatchResult{}, false
			}
			p.metrics.Retries.WithLabelValues(ClassTransient.String()).Inc()
			if serr := p.sleep(ctx, transientBackoff(transientAttempts)); serr != nil {
				p.logger.Error("ingest: retry backoff interrupted, dropping batch",
					"batch", descriptor, "count", count, "error", serr)
				p.metrics.WriteFailed.WithLabelValues(ClassTransient.String()).Inc()
				return store.BatchResult{}, false
			}

		// ClassNone is unreachable here: it classifies a nil error, and this
		// switch only runs when err != nil. Listed explicitly rather than
		// folded into default so the exhaustive linter keeps checking this
		// switch if RetryClass ever gains a member.
		case ClassNone:
			return store.BatchResult{}, false

		default:
			return store.BatchResult{}, false
		}
	}
}

// matchPersisted maps WriteBatch's BatchResult.EventRefs back onto the
// subset of the submitted batch those refs correspond to, for
// Publisher.Publish (SPEC §5.3: "only the events that were actually
// persisted").
//
// M1 fix: this used to do one monotonic forward walk matching each ref to
// the next batch event with an equal TS, which is only correct if batch is
// in non-decreasing TS order — it never is, since batch is the worker's
// arrival-order accumulation buffer (appended to as EnqueueEvents calls
// land, pipeline.go's runEventWorker), while WriteBatch sorts its own copy
// before writing and returns EventRefs sorted by (TS, Seq)
// (store/postgres/write.go). Fed an out-of-order batch, the old walk either
// skipped events it should have published or paired a ref with the wrong
// event's Seq. DedupKey is stable across that reordering (it is copied
// verbatim from the submitted event into the persisted row, never derived
// from position), so refs are now matched by DedupKey instead — an identity
// that survives the write, independent of what order either side is in.
func matchPersisted(batch []model.Event, refs []model.EventRef) []model.Event {
	if len(refs) == 0 {
		return nil
	}
	byKey := make(map[string]model.Event, len(batch))
	for _, e := range batch {
		// First occurrence wins: a batch can carry the same DedupKey twice
		// (the same logical event submitted more than once in one request),
		// and WriteBatch collapses those to a single ledger/candidate row,
		// so there is exactly one persisted ref per distinct key regardless
		// of which duplicate it "came from".
		if _, ok := byKey[e.DedupKey]; !ok {
			byKey[e.DedupKey] = e
		}
	}
	out := make([]model.Event, 0, len(refs))
	for _, ref := range refs {
		e, ok := byKey[ref.DedupKey]
		if !ok {
			continue
		}
		e.Seq = ref.Seq
		out = append(out, e)
	}
	return out
}

// dropEvents increments Dropped by source and logs once per batch. Every
// path that discards events without persisting them — queue-full,
// pipeline-closing, or a write that exhausted its retries — funnels through
// here so "nothing is silently dropped" (lead decision #4) has exactly one
// implementation to audit.
func (p *Pipeline) dropEvents(batch []model.Event, reason string) {
	bySource := make(map[model.Source]int, 2)
	for _, e := range batch {
		bySource[e.Source]++
	}
	for src, n := range bySource {
		p.metrics.Dropped.WithLabelValues(string(src)).Add(float64(n))
	}
	p.logger.Error("ingest: dropping event batch", "reason", reason, "count", len(batch))
}

// dropMetrics is dropEvents' counterpart for the metric-sample lane. All
// metric samples are counted under model.SourceOTelMetric (see
// EnqueueMetrics' doc: MetricSample carries no Source field).
func (p *Pipeline) dropMetrics(batch []model.MetricSample, reason string) {
	p.metrics.Dropped.WithLabelValues(string(model.SourceOTelMetric)).Add(float64(len(batch)))
	p.logger.Error("ingest: dropping metric batch", "reason", reason, "count", len(batch))
}
