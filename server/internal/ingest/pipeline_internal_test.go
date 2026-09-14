package ingest

// White-box tests: reach unexported Pipeline.testAfterClosingCheck (m5 hook).

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// internalFakeWriter records WriteBatch calls for testing.
type internalFakeWriter struct {
	mu       sync.Mutex
	received [][]model.Event
}

func (f *internalFakeWriter) WriteBatch(_ context.Context, events []model.Event) (store.BatchResult, error) {
	f.mu.Lock()
	f.received = append(f.received, events)
	f.mu.Unlock()
	refs := make([]model.EventRef, len(events))
	for i, e := range events {
		refs[i] = model.EventRef{TS: e.TS, Seq: int64(i + 1), DedupKey: e.DedupKey}
	}
	return store.BatchResult{Written: len(events), EventRefs: refs}, nil
}

func (f *internalFakeWriter) WriteMetrics(_ context.Context, samples []model.MetricSample) (store.BatchResult, error) {
	return store.BatchResult{Written: len(samples)}, nil
}

func (f *internalFakeWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardTestWriter{}, nil))
}

type discardTestWriter struct{}

func (discardTestWriter) Write(p []byte) (int, error) { return len(p), nil }

func instantTestSleep(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// m5: enqueue/drain race fix — batches passed to EnqueueEvents must never land after Close (closeMu RLock/WLock).
func TestEnqueueEvents_CannotLandAfterClose(t *testing.T) {
	fw := &internalFakeWriter{}
	p := New(fw, PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		WithRegisterer(prometheus.NewRegistry()), WithLogger(discardTestLogger()), WithSleep(instantTestSleep))

	started := make(chan struct{})
	resumed := make(chan struct{})
	p.testAfterClosingCheck = func() {
		close(started)
		<-resumed
	}

	enqueueErr := make(chan error, 1)
	go func() {
		enqueueErr <- p.EnqueueEvents([]model.Event{{ID: "e1", DedupKey: "d1", TS: time.Now(), SessionID: "s1", Source: model.SourceHook, Kind: model.KindToolResult, EventName: "tool_result"}})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("producer never reached the paused point")
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close(context.Background()) }()

	// Close must not be able to finish while the producer is still paused
	// mid-send: that is exactly the race window m5 closes.
	select {
	case err := <-closeErr:
		t.Fatalf("Close returned (err=%v) while a producer was still paused between its closing check and its send", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(resumed)

	require.NoError(t, <-enqueueErr, "the paused Enqueue must still succeed once it resumes")
	require.NoError(t, <-closeErr)

	require.Equal(t, 1, fw.count(), "the batch sent while Close was racing it must still reach the store")
}
