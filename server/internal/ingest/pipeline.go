// Package ingest implements Argus's ingestion pipeline (SPEC §3.6): bounded queue,
// non-blocking Enqueue, N workers, retrying transient failures per class.
//
// depguard (SPEC §3.1): ingest imports only internal/store, internal/model, stdlib, prometheus.
// This ensures receivers (OTLP, hooks) depend on ingest, never vice versa.
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

// ErrQueueFull is returned when queue capacity is reached (SPEC §3.6 load-shedding).
// Mapped to 503+Retry-After (OTLP, SPEC §3.4) or 429 (hooks, SPEC §3.5).
var ErrQueueFull = errors.New("ingest: queue full")

// ErrDrainDeadlineExceeded signals drain timeout (SPEC §3.8: exit non-zero if events dropped).
var ErrDrainDeadlineExceeded = errors.New("ingest: drain deadline exceeded")

// saturationThreshold is 0.9 (not 1.0: 10% margin to fail readiness before shedding).
const saturationThreshold = 0.9

// PipelineConfig mirrors ARGUS_INGEST_* env vars (SPEC §3.7); zero-values filled by applyDefaults.
type PipelineConfig struct {
	QueueCap       int           // ARGUS_INGEST_QUEUE, batches per lane
	Workers        int           // ARGUS_INGEST_WORKERS, event lane only (see runMetricWorker doc)
	BatchSize      int           // ARGUS_INGEST_BATCH_SIZE
	FlushInterval  time.Duration // ARGUS_INGEST_FLUSH
	RetryConflict  int           // ARGUS_INGEST_RETRY_CONFLICT
	RetryTransient int           // ARGUS_INGEST_RETRY_TRANSIENT

	// WriteTimeout bounds each WriteBatch/WriteMetrics attempt (audit M6).
	// Without it, lock-blocked statements park workers indefinitely.
	// retryLoop wraps each attempt in context.WithTimeout; zero = applyDefaults' 30s.
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

// Publisher is called per-flush; contract: within-flush order, never block (m7-minor, SPEC §5.3).
type Publisher interface {
	Publish(events []model.Event)
}

// NoopPublisher is Pipeline's default Publisher (discards events).
type NoopPublisher struct{}

// Publish implements Publisher.
func (NoopPublisher) Publish([]model.Event) {}

// SleepFunc allows testing Close deadline/retry backoff without real clock waits.
type SleepFunc func(ctx context.Context, d time.Duration) error

// defaultSleep is the production SleepFunc.
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

// options collects Option values before Pipeline construction.
type options struct {
	registerer prometheus.Registerer
	logger     *slog.Logger
	publisher  Publisher
	sleep      SleepFunc
}

// Option configures optional Pipeline dependencies (all have production-safe zero values).
type Option func(*options)

// WithRegisterer overrides the Prometheus registerer (tests must use fresh registry).
func WithRegisterer(r prometheus.Registerer) Option {
	return func(o *options) { o.registerer = r }
}

// WithLogger overrides the logger for drop/retry/permanent-failure paths.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithPublisher overrides the Publisher.
func WithPublisher(p Publisher) Option {
	return func(o *options) { o.publisher = p }
}

// WithSleep overrides the sleep function (tests use for instant retries).
func WithSleep(f SleepFunc) Option {
	return func(o *options) { o.sleep = f }
}

// Pipeline is the SPEC §3.6 ingestion pipeline (two lanes, bounded channels, worker goroutines).
type Pipeline struct {
	cfg       PipelineConfig
	store     store.Writer
	metrics   *Metrics
	logger    *slog.Logger
	publisher Publisher
	sleep     SleepFunc

	events    chan []model.Event
	metricsCh chan []model.MetricSample

	// publishCh is the m7-minor hand-off queue (bounded, drops if full, logged/accounted).
	publishCh chan []model.Event

	// ctx/cancel scope store.Writer calls. cancel only on deadline exceeded (unblock workers).
	ctx    context.Context
	cancel context.CancelFunc

	stopCh  chan struct{}
	closing atomic.Bool
	wg      sync.WaitGroup

	// closeMu is m5's enqueue/drain race fix: Enqueue*{check+send} held RLock,
	// Close held WLock, ensures sends commit before final drain and no silent loss.
	closeMu sync.RWMutex

	// testAfterClosingCheck runs after closing check (m5's regression test hook).
	testAfterClosingCheck func()
}

// New constructs a Pipeline and starts its worker goroutines immediately.
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

// Metrics exposes the registered Prometheus collectors.
func (p *Pipeline) Metrics() *Metrics { return p.metrics }

// QueueSaturated reports whether either lane crossed saturationThreshold (SPEC §3.8).
func (p *Pipeline) QueueSaturated() bool {
	return laneSaturated(len(p.events), cap(p.events)) || laneSaturated(len(p.metricsCh), cap(p.metricsCh))
}

func laneSaturated(depth, capacity int) bool {
	if capacity <= 0 {
		return false
	}
	return float64(depth)/float64(capacity) >= saturationThreshold
}

// QueueDepth reports buffered batches across both lanes (SPEC §5.1).
func (p *Pipeline) QueueDepth() int {
	return len(p.events) + len(p.metricsCh)
}

// EnqueueEvents queues events without blocking (SPEC §3.6). Returns ErrQueueFull if at capacity.
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

// EnqueueMetrics is the metric-sample lane counterpart (separate channel: no shared wire type).
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

// Close drains workers or times out (SPEC §3.8). Idempotent. Error signals exit non-zero.
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

// awaitEnqueueBarrier blocks until in-flight Enqueue* calls complete (m5 race fix: closeMu RLock/WLock).
func (p *Pipeline) awaitEnqueueBarrier() {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
}

func (p *Pipeline) start() {
	p.wg.Add(p.cfg.Workers)
	for i := 0; i < p.cfg.Workers; i++ {
		go p.runEventWorker()
	}
	// Single metric goroutine: OTLP volume small vs. log-events (lead decision #2).
	p.wg.Add(1)
	go p.runMetricWorker()

	// Not in p.wg (m7-minor): must outlive workers, terminates when publishCh closed/drained.
	go p.runPublishWorker()
}

// runEventWorker accumulates batches until BatchSize or FlushInterval, then flushes.
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

// runMetricWorker is the metric-sample lane counterpart (single goroutine).
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

// resetTimer drains a possibly-already-fired timer before Reset.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// runPublishWorker is the m7-minor consumer (only Publish caller, isolates slow hub from workers).
func (p *Pipeline) runPublishWorker() {
	for events := range p.publishCh {
		p.publishOne(events)
	}
}

// publishOne calls Publisher.Publish with panic recovery (m7-minor panic-safety).
func (p *Pipeline) publishOne(events []model.Event) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("ingest: publisher panicked, dropping this batch from the stream",
				"count", len(events), "panic", r)
		}
	}()
	p.publisher.Publish(events)
}

// handoffPublish hands events to publisher non-blockingly; drops if full to avoid stalling writes (m7-minor).
func (p *Pipeline) handoffPublish(events []model.Event) {
	select {
	case p.publishCh <- events:
	default:
		// Not p.metrics.Dropped: events are committed, only missed the stream; logging accounts for the drop.
		first := events[0]
		p.logger.Warn("ingest: publish handoff full, dropping batch from stream",
			"count", len(events), "session_id", first.SessionID)
	}
}

// flushEvents writes batch through retry loop and records metrics (SPEC §3.6).
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

// eventBatchDescriptor summarizes batch for drop/retry logs (m8 fix: non-empty ID).
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

// flushMetrics is flushEvents' counterpart (no Publish: SPEC §5.3 events only).
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

// retryLoop calls write, classifies errors, retries per budget or drops (SPEC §3.6).
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

// matchPersisted maps EventRefs back to submitted batch (SPEC §5.3, M1 fix: match by DedupKey).
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

// dropEvents increments Dropped and logs (lead decision #4: nothing silently dropped).
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

// dropMetrics is dropEvents' counterpart (all samples counted as SourceOTelMetric).
func (p *Pipeline) dropMetrics(batch []model.MetricSample, reason string) {
	p.metrics.Dropped.WithLabelValues(string(model.SourceOTelMetric)).Add(float64(len(batch)))
	p.logger.Error("ingest: dropping metric batch", "reason", reason, "count", len(batch))
}
