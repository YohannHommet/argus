package stream_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/stream"
)

// fakeStatsTarget is stream.StatsTarget's test double: it only ever records
// what it was handed. StatsBroadcaster's derivation logic (EventsPerSec,
// IngestLagMS) is exercised against this, never a real *Hub, so these tests
// need no subscriber and no real wall-clock 2s interval.
type fakeStatsTarget struct {
	mu    sync.Mutex
	stats []stream.Stats
}

func (f *fakeStatsTarget) PublishStats(s stream.Stats) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats = append(f.stats, s)
}

func (f *fakeStatsTarget) all() []stream.Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stream.Stats(nil), f.stats...)
}

func (f *fakeStatsTarget) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stats)
}

// scriptedSnapshotFunc returns snaps[0], snaps[1], ... in order, then keeps
// returning the last one forever (so a broadcaster ticking a few extra
// times after the script runs out doesn't panic the test).
func scriptedSnapshotFunc(snaps ...stream.Snapshot) stream.SnapshotFunc {
	var mu sync.Mutex
	i := 0
	return func(_ context.Context) (stream.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(snaps) {
			return snaps[len(snaps)-1], nil
		}
		s := snaps[i]
		i++
		return s, nil
	}
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- AC: stats reports non-zero events_per_sec under load and zero after ---

func TestStatsBroadcaster_EventsPerSec_NonZeroUnderLoadThenZeroAtPlateau(t *testing.T) {
	// Three readings: baseline (0 events), one tick after 1000 events landed
	// (load), then a plateau where EventsTotal stops climbing.
	snap := scriptedSnapshotFunc(
		stream.Snapshot{EventsTotal: 0},
		stream.Snapshot{EventsTotal: 1000},
		stream.Snapshot{EventsTotal: 1000}, // plateau: no new events this window
	)
	hub := &fakeStatsTarget{}
	b := stream.NewStatsBroadcaster(hub, snap, 20*time.Millisecond, discardTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	require.Eventually(t, func() bool { return hub.count() >= 2 }, time.Second, 2*time.Millisecond)
	cancel()

	got := hub.all()
	require.Positive(t, got[0].EventsPerSec, "the tick covering the load window must report a non-zero events_per_sec")
	require.Zero(t, got[1].EventsPerSec, "the tick covering the plateau (no new events) must report exactly zero")
}

// --- Baseline-before-first-tick and measured-elapsed-time behavior ---

func TestStatsBroadcaster_Run_TakesBaselineBeforeFirstTick(t *testing.T) {
	calls := make(chan struct{}, 8)
	snap := func(_ context.Context) (stream.Snapshot, error) {
		calls <- struct{}{}
		return stream.Snapshot{EventsTotal: 5}, nil
	}
	hub := &fakeStatsTarget{}
	// A long interval: if Run did NOT take a baseline immediately, no
	// snapshot would be read at all within this test's timeout.
	b := stream.NewStatsBroadcaster(hub, snap, time.Hour, discardTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("Run must call SnapshotFunc immediately for its baseline, before waiting for the first tick")
	}
}

// --- IngestLagMS: carried forward when a window sees no new observations,
// and 0 only until the very first observation ever (implemented decision,
// flagged in the report as a SPEC §4.1 null-vs-zero tension) ---

func TestStatsBroadcaster_IngestLagMS_CarriesForwardAcrossQuietWindows(t *testing.T) {
	snap := scriptedSnapshotFunc(
		stream.Snapshot{LagSum: 0, LagCount: 0},   // baseline: nothing measured yet
		stream.Snapshot{LagSum: 2, LagCount: 2},   // window 1: mean lag = 1s = 1000ms
		stream.Snapshot{LagSum: 2, LagCount: 2},   // window 2: no new observations
		stream.Snapshot{LagSum: 6.5, LagCount: 7}, // window 3: 5 new obs summing 4.5s -> mean 900ms
	)
	hub := &fakeStatsTarget{}
	b := stream.NewStatsBroadcaster(hub, snap, 10*time.Millisecond, discardTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	require.Eventually(t, func() bool { return hub.count() >= 3 }, time.Second, 2*time.Millisecond)
	cancel()

	got := hub.all()
	require.Equal(t, int64(1000), got[0].IngestLagMS, "window 1 must report the mean lag it actually observed")
	require.Equal(t, int64(1000), got[1].IngestLagMS, "a window with zero new lag observations must carry the last observed value forward, never reset to 0")
	require.Equal(t, int64(900), got[2].IngestLagMS, "window 3 must report its own freshly observed mean")
}

func TestStatsBroadcaster_IngestLagMS_ZeroUntilFirstEverObservation(t *testing.T) {
	snap := scriptedSnapshotFunc(
		stream.Snapshot{LagSum: 0, LagCount: 0}, // baseline
		stream.Snapshot{LagSum: 0, LagCount: 0}, // window 1: still nothing ever measured
	)
	hub := &fakeStatsTarget{}
	b := stream.NewStatsBroadcaster(hub, snap, 10*time.Millisecond, discardTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	require.Eventually(t, func() bool { return hub.count() >= 1 }, time.Second, 2*time.Millisecond)
	cancel()

	require.Equal(t, int64(0), hub.all()[0].IngestLagMS,
		"0 before any lag observation has ever been made means \"nothing measured yet\", not a measured zero")
}

// --- A snapshot error is logged and skipped, never published, and never
// corrupts the next window's delta ---

func TestStatsBroadcaster_SnapshotError_SkipsTickAndKeepsPreviousBaseline(t *testing.T) {
	var mu sync.Mutex
	call := 0
	snap := func(_ context.Context) (stream.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return stream.Snapshot{EventsTotal: 100}, nil // baseline
		case 2:
			return stream.Snapshot{}, errors.New("synthetic snapshot failure") // this tick must be skipped
		default:
			return stream.Snapshot{EventsTotal: 300}, nil // next good tick
		}
	}
	hub := &fakeStatsTarget{}
	b := stream.NewStatsBroadcaster(hub, snap, 15*time.Millisecond, discardTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	require.Eventually(t, func() bool { return hub.count() >= 1 }, time.Second, 2*time.Millisecond)
	cancel()

	got := hub.all()
	require.Len(t, got, 1, "the failed-snapshot tick must never publish a frame")
	require.Positive(t, got[0].EventsPerSec, "the surviving tick must diff against the baseline (100), not against the skipped failed read")
}

// --- Passthrough fields (QueueDepth/ActiveSessions/DroppedTotal) ---

func TestStatsBroadcaster_PassesThroughGaugeAndCounterFields(t *testing.T) {
	snap := scriptedSnapshotFunc(
		stream.Snapshot{QueueDepth: 3, ActiveSessions: 7, DroppedTotal: 2},
		stream.Snapshot{QueueDepth: 5, ActiveSessions: 9, DroppedTotal: 4},
	)
	hub := &fakeStatsTarget{}
	b := stream.NewStatsBroadcaster(hub, snap, 10*time.Millisecond, discardTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	require.Eventually(t, func() bool { return hub.count() >= 1 }, time.Second, 2*time.Millisecond)
	cancel()

	got := hub.all()[0]
	require.Equal(t, 5, got.QueueDepth)
	require.Equal(t, 9, got.ActiveSessions)
	require.Equal(t, int64(4), got.DroppedTotal)
}
