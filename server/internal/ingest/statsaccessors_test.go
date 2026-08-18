package ingest_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// The four accessors below exist so internal/app can build a
// stream.Snapshot (SPEC §5.1's `stats` frame) without importing
// prometheus/client_golang's testutil. They are the operator-facing numbers
// on the live view's health strip — events/sec, ingest lag, queue depth,
// dropped total — so a silent arithmetic mistake in one of them is a wrong
// number on a screen whose whole purpose is being trustworthy, with nothing
// else in the system to contradict it. Hence direct tests rather than relying
// on the broadcaster's own coverage.

func TestMetrics_EventsTotal_SumsEverySourceLabel(t *testing.T) {
	t.Parallel()
	m := ingest.NewMetrics(prometheus.NewRegistry())

	require.Zero(t, m.EventsTotal(), "a fresh registry has observed nothing")

	m.Events.WithLabelValues(string(model.SourceHook)).Add(3)
	m.Events.WithLabelValues(string(model.SourceOTelLog)).Add(4)
	m.Events.WithLabelValues(string(model.SourceOTelMetric)).Add(5)

	require.InDelta(t, float64(12), m.EventsTotal(), 0,
		"EventsTotal must sum across every source label, not report one of them")
}

func TestMetrics_DroppedCount_SumsEverySourceLabel(t *testing.T) {
	t.Parallel()
	m := ingest.NewMetrics(prometheus.NewRegistry())

	require.Zero(t, m.DroppedCount())

	m.Dropped.WithLabelValues(string(model.SourceHook)).Add(2)
	m.Dropped.WithLabelValues(string(model.SourceOTelLog)).Add(7)

	require.InDelta(t, float64(9), m.DroppedCount(), 0)
	require.Zero(t, m.EventsTotal(), "Dropped and Events must not read the same series")
}

func TestMetrics_LagObservations_ReportsCumulativeSumAndCount(t *testing.T) {
	t.Parallel()
	m := ingest.NewMetrics(prometheus.NewRegistry())

	sum, count := m.LagObservations()
	require.Zero(t, sum)
	require.Zero(t, count, "no observation yet — the broadcaster relies on this to skip a window")

	m.Lag.Observe(0.25)
	m.Lag.Observe(0.75)
	m.Lag.Observe(1.0)

	sum, count = m.LagObservations()
	require.InDelta(t, 2.0, sum, 1e-9)
	require.Equal(t, uint64(3), count)
	// The broadcaster divides one delta by the other to get a mean; the pair
	// must therefore come from the same histogram read.
	require.InDelta(t, 2.0/3.0, sum/float64(count), 1e-9)
}

// blockingWriter parks inside WriteBatch until released, so batches pile up in
// the pipeline's own channel and QueueDepth has something non-zero to report.
type blockingWriter struct{ release chan struct{} }

func (b *blockingWriter) WriteBatch(ctx context.Context, evs []model.Event) (store.BatchResult, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return store.BatchResult{}, ctx.Err()
	}
	return store.BatchResult{Written: len(evs)}, nil
}

func (b *blockingWriter) WriteMetrics(context.Context, []model.MetricSample) (store.BatchResult, error) {
	return store.BatchResult{}, nil
}

func TestPipeline_QueueDepth_ReportsBufferedBatches(t *testing.T) {
	t.Parallel()
	bw := &blockingWriter{release: make(chan struct{})}
	p := ingest.New(bw, ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()))

	require.Zero(t, p.QueueDepth(), "nothing enqueued yet")

	// The single worker parks in WriteBatch on the first batch; everything
	// after it stays in the channel, which is exactly what queue_depth means.
	for i := 0; i < 6; i++ {
		require.NoError(t, p.EnqueueEvents([]model.Event{testEvent("q", model.SourceHook)}))
	}
	require.Eventually(t, func() bool { return p.QueueDepth() > 0 }, 2*time.Second, 5*time.Millisecond,
		"queue_depth must report batches still waiting behind a stalled writer")

	close(bw.release)
	require.NoError(t, p.Close(context.Background()))
	require.Zero(t, p.QueueDepth(), "a fully drained pipeline reports an empty queue")
}

// TestHubPublisher_WithLogger_ReportsAFailedSessionRead covers the option and,
// more usefully, the tick's own error branch: a session can be swept or
// retention-deleted between Publish marking it dirty and the debounce tick
// reading it, so a failed read must be logged and skipped rather than
// abandoning the whole tick or (worse) publishing a zero-valued frame that a
// browser would render as a real projection snapshot.
func TestHubPublisher_WithLogger_ReportsAFailedSessionRead(t *testing.T) {
	t.Parallel()
	h := &capturingHandler{}
	hub := &fakeHubTarget{}
	reader := newFakeSessionReader()
	reader.setErr("gone", errors.New("session vanished"))

	pub := ingest.NewHubPublisher(hub, reader,
		ingest.WithSessionDebounce(5*time.Millisecond),
		ingest.WithHubPublisherLogger(slog.New(h)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	pub.Publish([]model.Event{{SessionID: "gone", Kind: model.KindLLMRequest}})

	require.Eventually(t, func() bool {
		_, ok := h.find("ingest: hub publisher: session summary read failed, skipping this debounce tick")
		return ok
	}, 2*time.Second, 5*time.Millisecond, "a failed session read must be logged, never silently swallowed")

	// The event frame still went out — a projection read failing must not cost
	// the browser the events themselves, which are already committed.
	require.Positive(t, hub.eventCount())
	require.Zero(t, hub.sessionFrameCount("gone"), "no session frame may be invented from a failed read")
}
