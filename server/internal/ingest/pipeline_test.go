package ingest_test

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// discardLogger swallows every log line so tests that intentionally trigger
// ERROR-level drop paths don't spam `go test -v` output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// instantSleep is the SleepFunc every test but the deadline test injects:
// it never actually waits, but still honours ctx cancellation so
// TestClose_NoGoroutineLeakWhenStoreBlocksForever remains meaningful.
func instantSleep(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// fakeWriter is the "fake store" the AC requires: a store.Writer whose
// WriteBatch/WriteMetrics behaviour is entirely test-controlled, with no
// database involved.
type fakeWriter struct {
	mu               sync.Mutex
	batchCalls       int
	metricsCalls     int
	receivedBatches  [][]model.Event
	writeBatchFunc   func(ctx context.Context, events []model.Event) (store.BatchResult, error)
	writeMetricsFunc func(ctx context.Context, samples []model.MetricSample) (store.BatchResult, error)
}

func (f *fakeWriter) WriteBatch(ctx context.Context, events []model.Event) (store.BatchResult, error) {
	f.mu.Lock()
	f.batchCalls++
	f.receivedBatches = append(f.receivedBatches, events)
	f.mu.Unlock()
	if f.writeBatchFunc != nil {
		return f.writeBatchFunc(ctx, events)
	}
	return defaultBatchResult(events), nil
}

func (f *fakeWriter) WriteMetrics(ctx context.Context, samples []model.MetricSample) (store.BatchResult, error) {
	f.mu.Lock()
	f.metricsCalls++
	f.mu.Unlock()
	if f.writeMetricsFunc != nil {
		return f.writeMetricsFunc(ctx, samples)
	}
	refs := make([]model.EventRef, 0)
	return store.BatchResult{Written: len(samples), EventRefs: refs}, nil
}

func (f *fakeWriter) calls() (batches, metrics int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batchCalls, f.metricsCalls
}

// defaultBatchResult reports every event as written and persisted, in
// order, mirroring what a real WriteBatch does when nothing is deduped.
func defaultBatchResult(events []model.Event) store.BatchResult {
	refs := make([]model.EventRef, len(events))
	for i, e := range events {
		refs[i] = model.EventRef{TS: e.TS, Seq: int64(i + 1)}
	}
	return store.BatchResult{Written: len(events), EventRefs: refs}
}

func testEvent(id string, source model.Source) model.Event {
	return model.Event{
		ID:         id,
		TS:         time.Now(),
		IngestedAt: time.Now(),
		SessionID:  "sess-1",
		Vendor:     "claude_code",
		Source:     source,
		Kind:       model.KindToolResult,
		EventName:  "tool_result",
	}
}

// pgErr builds a *pgconn.PgError with the given SQLSTATE, for exercising
// ClassifyError/retryLoop without a real database.
func pgErr(code string) error {
	return &pgconn.PgError{Code: code, Message: "synthetic test error"}
}

// metricValue reads the current value straight off a single Prometheus
// metric instance (what CounterVec.WithLabelValues/GaugeVec.WithLabelValues
// return), via the same Write(*dto.Metric) method
// prometheus/client_golang/prometheus/testutil.ToFloat64 uses internally.
// Written by hand against client_model rather than importing testutil
// itself: testutil.go pulls in github.com/kylelemons/godebug, a transitive
// dependency this module's go.mod does not declare, and P2-09 owns neither
// go.mod nor go.sum right now (P2-07 does, concurrently) — this sidesteps
// that dependency entirely rather than reaching for `go get`.
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var pb dto.Metric
	require.NoError(t, m.Write(&pb))
	switch {
	case pb.Counter != nil:
		return pb.Counter.GetValue()
	case pb.Gauge != nil:
		return pb.Gauge.GetValue()
	default:
		t.Fatal("metricValue: metric is neither a counter nor a gauge")
		return 0
	}
}

// --- AC: enqueue beyond capacity returns ErrQueueFull without blocking ---

func TestEnqueueEvents_QueueFull_ReturnsErrQueueFullWithoutBlocking(t *testing.T) {
	block := make(chan struct{})
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		<-block // never returns during this test, so nothing ever drains the queue
		return store.BatchResult{}, nil
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 1, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	// Defers run LIFO: close(block) must run BEFORE Close, or Close would
	// block forever waiting for a worker that will never return.
	defer func() { _ = p.Close(context.Background()) }()
	defer close(block)

	// First batch: the sole worker receives it off the channel immediately
	// and, since BatchSize=1, flushes right away — which blocks forever on
	// `block`. The channel itself is now empty again but nothing will ever
	// drain it further.
	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("e1", model.SourceHook)}))
	require.Eventually(t, func() bool { calls, _ := fw.calls(); return calls >= 1 }, time.Second, 2*time.Millisecond)
	// Second batch: fills the 1-slot channel, since the worker is stuck.
	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("e2", model.SourceHook)}))

	done := make(chan error, 1)
	go func() {
		done <- p.EnqueueEvents([]model.Event{testEvent("e3", model.SourceHook)})
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, ingest.ErrQueueFull)
	case <-time.After(2 * time.Second):
		t.Fatal("EnqueueEvents blocked instead of returning ErrQueueFull immediately")
	}

	require.InDelta(t, float64(1), metricValue(t, p.Metrics().Dropped.WithLabelValues(string(model.SourceHook))), 0)
}

// --- AC: batches flush on size and on the timer ---

func TestFlush_OnBatchSize(t *testing.T) {
	fw := &fakeWriter{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 2, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))
	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("b", model.SourceHook)}))

	require.Eventually(t, func() bool {
		calls, _ := fw.calls()
		return calls >= 1
	}, 2*time.Second, 5*time.Millisecond, "size-triggered flush never called WriteBatch")
}

func TestFlush_OnTimer(t *testing.T) {
	fw := &fakeWriter{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1000, FlushInterval: 20 * time.Millisecond},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))

	require.Eventually(t, func() bool {
		calls, _ := fw.calls()
		return calls >= 1
	}, 2*time.Second, 5*time.Millisecond, "flush timer never fired")
}

// --- AC: a 40P01 error retries up to 8 times and then succeeds on the 8th ---

func TestRetry_ConflictSucceedsOnEighthAttempt(t *testing.T) {
	var attempts atomic.Int32
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, events []model.Event) (store.BatchResult, error) {
		n := attempts.Add(1)
		if n < 8 {
			return store.BatchResult{}, pgErr("40P01")
		}
		return defaultBatchResult(events), nil
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour, RetryConflict: 8},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))

	require.Eventually(t, func() bool {
		return attempts.Load() == 8
	}, 2*time.Second, 5*time.Millisecond, "expected exactly 8 WriteBatch calls")

	// No data loss: the batch succeeded, so it must be counted as an event,
	// never as dropped or permanently failed.
	require.Eventually(t, func() bool {
		return metricValue(t, p.Metrics().Events.WithLabelValues(string(model.SourceHook))) == 1
	}, time.Second, 5*time.Millisecond)
	require.InDelta(t, float64(0), metricValue(t, p.Metrics().Dropped.WithLabelValues(string(model.SourceHook))), 0)
	require.InDelta(t, float64(7), metricValue(t, p.Metrics().Retries.WithLabelValues("conflict")), 0)
}

// --- AC: a 23505 error is not retried and increments class="permanent" ---

func TestRetry_PermanentErrorNotRetried(t *testing.T) {
	var attempts atomic.Int32
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		attempts.Add(1)
		return store.BatchResult{}, pgErr("23505")
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))

	require.Eventually(t, func() bool {
		return metricValue(t, p.Metrics().WriteFailed.WithLabelValues("permanent")) == 1
	}, 2*time.Second, 5*time.Millisecond)

	require.EqualValues(t, 1, attempts.Load(), "a permanent error must never be retried")
	require.InDelta(t, float64(1), metricValue(t, p.Metrics().Dropped.WithLabelValues(string(model.SourceHook))), 0)
}

// --- AC: a transient error retries 3x then drops ---

func TestRetry_TransientExhaustsThenDrops(t *testing.T) {
	var attempts atomic.Int32
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		attempts.Add(1)
		return store.BatchResult{}, pgErr("08006") // connection_failure
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour, RetryTransient: 3},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))

	require.Eventually(t, func() bool {
		return metricValue(t, p.Metrics().WriteFailed.WithLabelValues("transient")) == 1
	}, 2*time.Second, 5*time.Millisecond)

	require.EqualValues(t, 3, attempts.Load())
	require.InDelta(t, float64(2), metricValue(t, p.Metrics().Retries.WithLabelValues("transient")), 0)
	require.InDelta(t, float64(1), metricValue(t, p.Metrics().Dropped.WithLabelValues(string(model.SourceHook))), 0)
}

// --- AC: Close() drains queued batches before returning ---

func TestClose_DrainsQueuedBatchesBeforeReturning(t *testing.T) {
	fw := &fakeWriter{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 2, BatchSize: 1000, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))

	for i := 0; i < 10; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("x", model.SourceHook)}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, p.Close(ctx))

	require.InDelta(t, float64(10), metricValue(t, p.Metrics().Events.WithLabelValues(string(model.SourceHook))), 0)
}

// --- AC: Close() errors if the deadline is hit ---

func TestClose_ErrorsOnDeadlineExceeded(t *testing.T) {
	block := make(chan struct{})
	fw := &fakeWriter{writeBatchFunc: func(ctx context.Context, _ []model.Event) (store.BatchResult, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return store.BatchResult{}, ctx.Err()
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer close(block)

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))
	// Give the worker a moment to actually pick the batch up and be blocked
	// inside WriteBatch before we start the deadline clock.
	require.Eventually(t, func() bool { calls, _ := fw.calls(); return calls >= 1 }, time.Second, 2*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.Close(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, ingest.ErrDrainDeadlineExceeded)
}

// --- AC: a store that blocks forever leaks no goroutines after Close ---

func TestClose_NoGoroutineLeakWhenStoreBlocksForever(t *testing.T) {
	before := runtime.NumGoroutine()

	fw := &fakeWriter{writeBatchFunc: func(ctx context.Context, _ []model.Event) (store.BatchResult, error) {
		<-ctx.Done() // "blocks forever" except for context cancellation, exactly like a real pgx call would
		return store.BatchResult{}, ctx.Err()
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 4, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))

	for i := 0; i < 4; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.Close(ctx)
	require.ErrorIs(t, err, ingest.ErrDrainDeadlineExceeded)

	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before+1 // +1 slack for test-runner scheduling noise
	}, 2*time.Second, 10*time.Millisecond, "goroutines leaked after Close returned")
}

// --- AC (-race clean with 8 concurrent producers) ---

func TestEnqueueEvents_ConcurrentProducersRaceClean(t *testing.T) {
	fw := &fakeWriter{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 1024, Workers: 4, BatchSize: 50, FlushInterval: 10 * time.Millisecond},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))

	const producers = 8
	const perProducer = 100
	var wg sync.WaitGroup
	wg.Add(producers)
	for i := 0; i < producers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perProducer; j++ {
				_ = p.EnqueueEvents([]model.Event{testEvent("c", model.SourceHook)})
			}
		}()
	}
	wg.Wait()

	require.NoError(t, p.Close(context.Background()))
}

// --- lead decision #3: the Publisher seam sees only persisted events ---

type recordingPublisher struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *recordingPublisher) Publish(events []model.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, events...)
}

func (r *recordingPublisher) seen() []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Event(nil), r.events...)
}

func TestPublisher_SeesOnlyPersistedEvents(t *testing.T) {
	kept := testEvent("kept", model.SourceHook)
	deduped := testEvent("deduped", model.SourceHook)
	deduped.TS = kept.TS.Add(time.Millisecond) // distinct TS so matchPersisted can tell them apart

	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		// Simulate the "deduped" event being suppressed: only "kept" is
		// reported as persisted.
		return store.BatchResult{Written: 1, Deduped: 1, EventRefs: []model.EventRef{{TS: kept.TS, Seq: 1}}}, nil
	}}
	pub := &recordingPublisher{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 2, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()),
		ingest.WithSleep(instantSleep), ingest.WithPublisher(pub))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{kept, deduped}))

	require.Eventually(t, func() bool { return len(pub.seen()) == 1 }, 2*time.Second, 5*time.Millisecond)
	got := pub.seen()
	require.Equal(t, "kept", got[0].ID)
	require.Equal(t, int64(1), got[0].Seq)
}

// --- lead decision #5: registration hygiene ---

func TestNewMetrics_TwoPipelinesDoNotPanicOnDuplicateRegistration(t *testing.T) {
	require.NotPanics(t, func() {
		fw1, fw2 := &fakeWriter{}, &fakeWriter{}
		p1 := ingest.New(fw1, ingest.PipelineConfig{Workers: 1}, ingest.WithRegisterer(prometheus.NewRegistry()))
		p2 := ingest.New(fw2, ingest.PipelineConfig{Workers: 1}, ingest.WithRegisterer(prometheus.NewRegistry()))
		require.NoError(t, p1.Close(context.Background()))
		require.NoError(t, p2.Close(context.Background()))
	})
}

// --- readiness (lead decision #1): QueueSaturated crosses the threshold ---

func TestQueueSaturated_CrossesThreshold(t *testing.T) {
	block := make(chan struct{})
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		<-block
		return store.BatchResult{}, nil
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 10, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	// Defers run LIFO: close(block) must run BEFORE Close, or Close would
	// block forever waiting for a worker that will never return.
	defer func() { _ = p.Close(context.Background()) }()
	defer close(block)

	require.False(t, p.QueueSaturated())

	// The first event is picked up by the sole worker immediately and, since
	// BatchSize=1, flushed straight away — which blocks forever, so nothing
	// drains the channel from here on.
	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("trigger", model.SourceHook)}))
	require.Eventually(t, func() bool { calls, _ := fw.calls(); return calls >= 1 }, time.Second, 2*time.Millisecond)

	// 9 more fill the 10-slot channel to a depth of 9 -> 9/10 = 0.9, at the
	// saturation threshold.
	for i := 0; i < 9; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))
	}

	require.Eventually(t, func() bool { return p.QueueSaturated() }, time.Second, 5*time.Millisecond)
}
