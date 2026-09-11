package app

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// idleWriter is a store.Writer that is never actually written to: both tests
// below construct a Pipeline solely to read its metric accessors, and never
// enqueue anything.
type idleWriter struct{}

func (idleWriter) WriteBatch(context.Context, []model.Event) (store.BatchResult, error) {
	return store.BatchResult{}, nil
}

func (idleWriter) WriteMetrics(context.Context, []model.MetricSample) (store.BatchResult, error) {
	return store.BatchResult{}, nil
}

// TestStatsSnapshot_DroppedTotalCountsOnlyPermanentLoss pins a composition
// decision that is otherwise invisible: SPEC §5.1's `stats` frame carries
// exactly one `dropped_total` field, and it reports ONLY events ingest never
// managed to store.
//
// Folding the hub's own buffer drops in looks tempting — they are real loss
// too — and was the first implementation. It is wrong, because the two facts
// are not interchangeable. An ingest drop is permanent: nothing was stored, so
// no reconnect can replay it. A hub drop means the event IS stored and one
// subscriber's SSE buffer merely fell behind, a condition SPEC §5.1 already
// gives its own dedicated per-subscriber channel (`event: lag`, which the
// client answers by refetching) and which /metrics exposes fleet-wide as
// argus_stream_dropped_total. Summing them would make a self-healing
// display-layer hiccup indistinguishable from permanent data loss in the one
// number the data-quality screen's dropped tile reports — deviation D-28 names
// this exact field as that tile's future backing.
func TestStatsSnapshot_DroppedTotalCountsOnlyPermanentLoss(t *testing.T) {
	t.Parallel()

	// A fresh registry per Pipeline/Hub: prometheus.DefaultRegisterer is a
	// process-global that panics on a duplicate metric name (see
	// ingest.WithRegisterer's and stream.WithRegisterer's docs).
	pipe := ingest.New(idleWriter{}, ingest.PipelineConfig{QueueCap: 4, Workers: 1, BatchSize: 1},
		ingest.WithRegisterer(prometheus.NewRegistry()))
	t.Cleanup(func() { _ = pipe.Close(context.Background()) })

	// Overflow one subscriber's buffer so the hub's drop counter is provably
	// non-zero — without that, this test would pass against an implementation
	// that summed the two counters and simply had nothing to sum.
	hub := stream.New(stream.WithRegisterer(prometheus.NewRegistry()), stream.WithBuffer(1))
	sub, err := hub.Subscribe(stream.AllTopic(), stream.Filter{})
	require.NoError(t, err)
	t.Cleanup(sub.Close)
	hub.Publish([]stream.Envelope{
		{Event: model.Event{SessionID: "s1"}},
		{Event: model.Event{SessionID: "s1"}},
		{Event: model.Event{SessionID: "s1"}},
	}, nil)
	require.Positive(t, hub.DroppedTotal(),
		"the hub must actually have dropped something, or this test asserts nothing")

	snap, err := newStatsSnapshotFunc(pipe, func(context.Context) (int64, error) { return 7, nil })(context.Background())
	require.NoError(t, err)

	require.Equal(t, int64(0), snap.DroppedTotal,
		"dropped_total must be ingest-only: the hub dropped %d and none of it may appear here", hub.DroppedTotal())
	require.Equal(t, 7, snap.ActiveSessions)
}

// TestStatsSnapshot_ActiveSessionErrorIsNotAZeroSnapshot pins the other half of
// the honesty rule: a failed active-session read must surface as an error so
// StatsBroadcaster skips the tick, never as a zero-valued Snapshot that would
// be published as a measured "0 active sessions" (SPEC §4.1's null-vs-zero
// rule — a zero means measured zero).
func TestStatsSnapshot_ActiveSessionErrorIsNotAZeroSnapshot(t *testing.T) {
	t.Parallel()

	pipe := ingest.New(idleWriter{}, ingest.PipelineConfig{QueueCap: 4, Workers: 1, BatchSize: 1},
		ingest.WithRegisterer(prometheus.NewRegistry()))
	t.Cleanup(func() { _ = pipe.Close(context.Background()) })

	sentinel := errors.New("database is down")
	_, err := newStatsSnapshotFunc(pipe, func(context.Context) (int64, error) { return 0, sentinel })(context.Background())

	require.ErrorIs(t, err, sentinel)
}
