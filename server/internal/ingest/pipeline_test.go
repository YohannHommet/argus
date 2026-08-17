package ingest_test

import (
	"context"
	"fmt"
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
// EventRefs carries each event's own DedupKey (M1: the identity
// matchPersisted now matches on, since a real WriteBatch's refs are sorted
// by (ts, seq), not returned in submission order).
func defaultBatchResult(events []model.Event) store.BatchResult {
	refs := make([]model.EventRef, len(events))
	for i, e := range events {
		refs[i] = model.EventRef{TS: e.TS, Seq: int64(i + 1), DedupKey: e.DedupKey}
	}
	return store.BatchResult{Written: len(events), EventRefs: refs}
}

// testEvent builds a fixture with a DedupKey derived from id, so id already
// being unique per call site (the AC's convention throughout this file)
// also makes DedupKey unique — matchPersisted (M1) matches on DedupKey, not
// position, so tests that need two distinct events to stay distinguishable
// through a fake WriteBatch rely on this.
func testEvent(id string, source model.Source) model.Event {
	return model.Event{
		ID:         id,
		DedupKey:   "dedup:" + id,
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

// --- m8: a dropped-batch log must carry identifiers the batch actually ---
// --- has, not the always-empty Event.ID. ---

// capturingHandler is a minimal slog.Handler that records every log record
// verbatim, so tests can inspect structured attrs rather than parsing text
// output.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) find(msg string) (slog.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

func attrValue(r slog.Record, key string) (string, bool) {
	var val string
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value.String()
			found = true
			return false
		}
		return true
	})
	return val, found
}

func TestFlushEvents_DropLogCarriesBatchIdentifiersNotEmptyID(t *testing.T) {
	h := &capturingHandler{}
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		return store.BatchResult{}, pgErr("23505") // permanent, dropped on first attempt
	}}
	// A real Event.ID is only ever set by the events table's uuidv7()
	// column default, never before the insert — pipeline.go's normalizers
	// never mint one — so a fixture bound for the write path always has
	// ID == "" at drop time, exactly like production.
	ev := testEvent("m8", model.SourceHook)
	ev.ID = ""
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(slog.New(h)), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{ev}))

	var rec slog.Record
	require.Eventually(t, func() bool {
		r, ok := h.find("ingest: permanent write error, dropping batch")
		if ok {
			rec = r
		}
		return ok
	}, 2*time.Second, 5*time.Millisecond, "drop log line was never emitted")

	descriptor, ok := attrValue(rec, "batch")
	require.True(t, ok, `drop log must carry a "batch" identifier attribute`)
	require.NotEmpty(t, descriptor, "an operator needs something to search on after a whole-batch loss")
	require.Contains(t, descriptor, "session_id="+ev.SessionID)
	require.Contains(t, descriptor, "dedup_key="+ev.DedupKey)
	require.Contains(t, descriptor, "event_name="+ev.EventName)
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

// --- M6: every write attempt gets a per-attempt deadline off p.ctx ---
//
// Before this fix, retryLoop ran write(p.ctx) directly — p.ctx is a plain
// context.WithCancel(Background()), which never expires on its own — so a
// write blocked on a lock (or, here, a fake store that simply waits for its
// ctx to be cancelled) parked the worker forever: retry classification
// never runs because write() never returns. This test's fake store never
// resolves on its own; only a per-attempt deadline can make it return.
func TestRetry_WriteTimeoutBoundsEachAttempt(t *testing.T) {
	var attempts atomic.Int32
	fw := &fakeWriter{writeBatchFunc: func(ctx context.Context, _ []model.Event) (store.BatchResult, error) {
		attempts.Add(1)
		<-ctx.Done() // exactly like a real pgx call blocked on a lock: only ctx cancellation ends it
		return store.BatchResult{}, ctx.Err()
	}}
	p := ingest.New(fw, ingest.PipelineConfig{
		QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour,
		RetryTransient: 2, WriteTimeout: 20 * time.Millisecond,
	}, ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("a", model.SourceHook)}))

	// Without a per-attempt deadline this never becomes true: write() never
	// returns, so retryLoop never gets to classify anything.
	require.Eventually(t, func() bool {
		return metricValue(t, p.Metrics().WriteFailed.WithLabelValues("transient")) == 1
	}, 2*time.Second, 5*time.Millisecond, "a write stuck forever must still time out and get classified transient")

	require.EqualValues(t, 2, attempts.Load(), "RetryTransient=2: exactly two timed-out attempts before dropping")
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
		// reported as persisted, matched by its DedupKey (M1) rather than
		// position or TS.
		return store.BatchResult{Written: 1, Deduped: 1, EventRefs: []model.EventRef{{TS: kept.TS, Seq: 1, DedupKey: kept.DedupKey}}}, nil
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

// --- M1: matchPersisted must key on DedupKey, not a sorted-order walk ---
//
// The worker's accumulation buffer (pipeline.go's `buf`) is arrival order,
// never guaranteed TS-sorted, while a real WriteBatch sorts its own copy
// and returns EventRefs sorted by (ts, seq) (store/postgres/write.go). This
// test reproduces exactly that mismatch with a fake WriteBatch playing the
// role of the real one: two events land on the pipeline out of TS order and
// with an equal TS to a third, and the fake reports refs in sorted (ts,
// seq) order, as the real store does. The old single forward-walk
// implementation (matching each ref to the next batch event with an equal
// TS, `i` never reset) either skipped e1 entirely or paired it with e2's
// Seq — see this test's "before" run in the ticket report.
func TestPublisher_OutOfOrderBufferAndDuplicateTS_MatchesByDedupKey(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	// e1 and e2 share a TS (the "duplicate TS" half of the finding); e2 is
	// enqueued (and therefore buffered) before e1, so the buffer this
	// worker accumulates is [e2, e1, e3] — not TS-sorted, matching the
	// claim that `buf` is arrival order, not WriteBatch's sort order.
	e1 := testEvent("e1", model.SourceHook)
	e1.TS = base.Add(5 * time.Second) // 10:00:05
	e2 := testEvent("e2", model.SourceHook)
	e2.TS = base.Add(5 * time.Second) // 10:00:05, same TS as e1
	e3 := testEvent("e3", model.SourceHook)
	e3.TS = base.Add(1 * time.Second) // 10:00:01, earlier than both

	var gotBatch []model.Event
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, events []model.Event) (store.BatchResult, error) {
		gotBatch = events
		// Mirror a real WriteBatch: refs sorted by (ts, seq), which is NOT
		// this batch's arrival order [e2, e1, e3].
		refs := []model.EventRef{
			{TS: e3.TS, Seq: 10, DedupKey: e3.DedupKey}, // 10:00:01
			{TS: e1.TS, Seq: 20, DedupKey: e1.DedupKey}, // 10:00:05 (e1)
			{TS: e2.TS, Seq: 21, DedupKey: e2.DedupKey}, // 10:00:05 (e2)
		}
		return store.BatchResult{Written: 3, EventRefs: refs}, nil
	}}
	pub := &recordingPublisher{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 3, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()),
		ingest.WithSleep(instantSleep), ingest.WithPublisher(pub))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	// Enqueue order == buffer/arrival order: e2, e1, e3 — deliberately not
	// TS-sorted.
	require.NoError(t, p.EnqueueEvents([]model.Event{e2}))
	require.NoError(t, p.EnqueueEvents([]model.Event{e1}))
	require.NoError(t, p.EnqueueEvents([]model.Event{e3}))

	require.Eventually(t, func() bool { return len(pub.seen()) == 3 }, 2*time.Second, 5*time.Millisecond)
	require.Len(t, gotBatch, 3, "sanity: the fake WriteBatch received the arrival-order buffer")
	require.Equal(t, []string{"e2", "e1", "e3"}, []string{gotBatch[0].ID, gotBatch[1].ID, gotBatch[2].ID},
		"sanity: the buffer really is arrival order, not TS order")

	got := pub.seen()
	bySeq := make(map[int64]string, len(got))
	for _, e := range got {
		bySeq[e.Seq] = e.ID
	}
	require.Equal(t, "e3", bySeq[10], "e3 (10:00:01) must publish with Seq 10")
	require.Equal(t, "e1", bySeq[20], "e1 must publish with its own Seq 20, not e2's, despite the equal TS")
	require.Equal(t, "e2", bySeq[21], "e2 must publish with its own Seq 21, not e1's, despite the equal TS")
}

// --- m7-minor: the Publisher hand-off contract (a blocking hub must not ---
// --- stall a flush; a panicking hub must not kill the worker; ordering ---
// --- within one flush must be preserved). ---

// blockingPublisher never returns from Publish, standing in for a hub stuck
// on a slow subscriber (SPEC §5.3's seam, audit finding m7-minor).
type blockingPublisher struct{ calls atomic.Int32 }

func (b *blockingPublisher) Publish(_ []model.Event) {
	b.calls.Add(1)
	select {} // blocks forever
}

func TestPublisher_BlockingPublisherDoesNotStallFlush(t *testing.T) {
	pub := &blockingPublisher{}
	fw := &fakeWriter{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()),
		ingest.WithSleep(instantSleep), ingest.WithPublisher(pub))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	// Every one of these would have been a separate flush (BatchSize=1)
	// that would have called Publish inline, and Publish never returns —
	// if the flush path itself blocked on it, only the first would ever
	// complete. All of them completing (WriteBatch called for each, and
	// each one's blockingPublisher.Publish eventually invoked) proves the
	// hand-off, not the flush, is what's exposed to the slow hub.
	for i := 0; i < 5; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent(fmt.Sprintf("e%d", i), model.SourceHook)}))
	}

	require.Eventually(t, func() bool {
		calls, _ := fw.calls()
		return calls == 5
	}, 2*time.Second, 5*time.Millisecond, "flushes stalled behind a Publisher that never returns")
	require.Eventually(t, func() bool { return pub.calls.Load() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"the blocking Publisher was never even invoked")
}

// panicPublisher panics on every call, standing in for a hub bug (audit
// finding m7-minor's panic-safety requirement).
type panicPublisher struct{ calls atomic.Int32 }

func (p *panicPublisher) Publish(_ []model.Event) {
	p.calls.Add(1)
	panic("hub bug")
}

func TestPublisher_PanicDoesNotKillWorker(t *testing.T) {
	pub := &panicPublisher{}
	fw := &fakeWriter{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()),
		ingest.WithSleep(instantSleep), ingest.WithPublisher(pub))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("before-panic", model.SourceHook)}))
	require.Eventually(t, func() bool { return pub.calls.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)

	// The worker (and the publish goroutine) must still be alive: a second,
	// independent flush must still reach WriteBatch and the recovered
	// panic must not have taken the process down.
	require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("after-panic", model.SourceHook)}))
	require.Eventually(t, func() bool {
		calls, _ := fw.calls()
		return calls == 2
	}, 2*time.Second, 5*time.Millisecond, "a Publisher panic must not stop subsequent flushes from being written")
	require.Eventually(t, func() bool { return pub.calls.Load() >= 2 }, 2*time.Second, 5*time.Millisecond,
		"a Publisher panic must not stop subsequent publish attempts")
}

func TestPublisher_OrderPreservedWithinOneFlush(t *testing.T) {
	events := make([]model.Event, 5)
	for i := range events {
		events[i] = testEvent(fmt.Sprintf("ord-%d", i), model.SourceHook)
	}

	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, got []model.Event) (store.BatchResult, error) {
		return defaultBatchResult(got), nil
	}}
	pub := &recordingPublisher{}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 5, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()),
		ingest.WithSleep(instantSleep), ingest.WithPublisher(pub))
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, p.EnqueueEvents(events))

	require.Eventually(t, func() bool { return len(pub.seen()) == 5 }, 2*time.Second, 5*time.Millisecond)
	got := pub.seen()
	for i, e := range got {
		require.Equal(t, fmt.Sprintf("ord-%d", i), e.ID, "publish order must match the flush's own order")
	}
}

// --- lead decision #5: registration hygiene ---

// --- M7: the shutdown drain must chunk on BatchSize, not coalesce the ---
// --- whole backlog into one unbounded write. ---
//
// The steady-state path (runEventWorker's outer `case batch := <-p.events`)
// already checks `len(buf) >= p.cfg.BatchSize` before flushing. The old
// drain loop under `case <-p.stopCh` did not: it appended every remaining
// batch first and flushed once at the end, with no size check at all. This
// test forces a real backlog to sit in the channel while the worker is
// blocked inside its first WriteBatch call, closes the pipeline while that
// backlog is still queued, and then asserts the invariant a bounded drain
// must uphold regardless of how the runtime interleaves the worker's
// select between the event channel and stopCh: no single WriteBatch call
// ever receives more than BatchSize events.
func TestClose_DrainChunksBacklogOnBatchSize(t *testing.T) {
	const batchSize = 5
	const backlog = 60 // several multiples of batchSize, so old code's single
	// unbounded flush is overwhelmingly likely to be caught red-handed even
	// though which of the runtime's select branches serves which event is
	// not under the test's control (see this test's doc).

	firstCallBlock := make(chan struct{})
	var attempts atomic.Int32
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, events []model.Event) (store.BatchResult, error) {
		if attempts.Add(1) == 1 {
			<-firstCallBlock
		}
		return defaultBatchResult(events), nil
	}}
	p := ingest.New(fw, ingest.PipelineConfig{QueueCap: backlog + 1, Workers: 1, BatchSize: batchSize, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()), ingest.WithSleep(instantSleep))

	// One event triggers the size-1... no: batchSize events trigger the
	// first flush, which blocks inside WriteBatch, freezing the worker
	// mid-flush so every subsequent Enqueue just queues up untouched.
	for i := 0; i < batchSize; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent(fmt.Sprintf("first-%d", i), model.SourceHook)}))
	}
	require.Eventually(t, func() bool { return attempts.Load() >= 1 }, time.Second, 2*time.Millisecond,
		"first flush never started")

	for i := 0; i < backlog; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent(fmt.Sprintf("backlog-%d", i), model.SourceHook)}))
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close(context.Background()) }()
	// Close's own work before it can block on the drain (CompareAndSwap,
	// the m5 enqueue barrier, close(stopCh)) is all non-blocking — this
	// gives that goroutine ample time to actually reach close(stopCh)
	// before the worker's blocked first flush is released, so the backlog
	// enqueued above is still sitting in the channel, and stopCh is
	// already closed, at the moment the worker resumes.
	time.Sleep(100 * time.Millisecond)
	close(firstCallBlock)

	require.NoError(t, <-closeErr)

	batches, _ := fw.calls()
	require.Positive(t, batches, "expected more than one WriteBatch call")

	fw.mu.Lock()
	defer fw.mu.Unlock()
	total := 0
	for _, b := range fw.receivedBatches {
		total += len(b)
		require.LessOrEqualf(t, len(b), batchSize,
			"a single WriteBatch call received %d events, more than BatchSize=%d: the drain coalesced the backlog instead of chunking it",
			len(b), batchSize)
	}
	require.Equal(t, batchSize+backlog, total, "every enqueued event must still be written exactly once")
}

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
