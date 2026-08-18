// Package stream — stats.go implements P5-03's `event: stats` broadcaster
// (SPEC §5.1, §5.3): every ARGUS_STREAM_STATS_INTERVAL (2s), it turns two
// raw-counter readings into the derived Stats frame Hub.PublishStats fans
// out.
//
// depguard note: this file adds nothing to internal/stream's import list
// (package doc: stdlib + prometheus + internal/model only). Snapshot is
// deliberately a plain struct of raw, cumulative counters rather than
// anything that knows about *ingest.Pipeline or *postgres.Store — collecting
// one is internal/app's job (the only package allowed to know about both,
// package doc on internal/app), wired in through SnapshotFunc.
package stream

import (
	"context"
	"log/slog"
	"math"
	"time"
)

// Snapshot is one reading of the raw, cumulative counters a Stats frame is
// derived from (SPEC §5.1). Every field is monotonically non-decreasing
// across the life of the process it was read from — StatsBroadcaster only
// ever needs the DELTA between two readings, never an absolute value on its
// own (ActiveSessions/QueueDepth excepted: those are already point-in-time
// gauges, not counters).
type Snapshot struct {
	QueueDepth int // point-in-time gauge: batches currently buffered (both ingest lanes)

	EventsTotal float64 // cumulative count of events persisted since process start

	// LagSum/LagCount are a histogram's cumulative sum/observation-count
	// (ingest.Metrics.Lag): (LagSum-prevLagSum)/(LagCount-prevLagCount) is
	// the mean ingest lag, in seconds, over the window between two
	// snapshots — see tick's doc comment for why this must be windowed
	// rather than read as a lifetime average.
	LagSum   float64
	LagCount uint64

	ActiveSessions int   // point-in-time gauge: sessions.status = 'active' right now
	DroppedTotal   int64 // cumulative count, summed across every reason an event never reached a subscriber (see internal/app's Snapshot-building doc comment for what that sum covers)
}

// SnapshotFunc reads one Snapshot. internal/app supplies the production
// implementation, closing over *ingest.Pipeline and *postgres.Store; tests
// supply a fake that returns scripted values with no I/O at all.
type SnapshotFunc func(ctx context.Context) (Snapshot, error)

// StatsTarget is the narrow hub port StatsBroadcaster needs. *Hub satisfies
// it structurally. Depending on this instead of *Hub's whole surface (the
// same convention internal/ingest.HubTarget follows for Publish) means a
// test can exercise the broadcaster's derivation logic against a fake that
// only ever records what it was handed.
type StatsTarget interface {
	PublishStats(Stats)
}

// StatsBroadcaster is the SPEC §5.1/§5.3 `event: stats` producer: on every
// tick it reads a fresh Snapshot, derives a Stats frame from the delta
// against the previous reading, and publishes it. Construct with
// NewStatsBroadcaster; call Run to start the loop (internal/app starts it
// the same way it starts the other scheduler-shaped jobs — PartitionJob,
// SweepJob, HubPublisher — watching ctx, no separate shutdown step).
type StatsBroadcaster struct {
	hub      StatsTarget
	snap     SnapshotFunc
	interval time.Duration
	logger   *slog.Logger

	havePrev bool
	prev     Snapshot
	prevAt   time.Time

	// lastLagMS is the last successfully-derived mean ingest lag, carried
	// forward across any tick whose window saw zero new lag observations
	// (tick's doc comment explains why "no new observations" cannot mean
	// "lag is now zero"). Its zero value, 0, is also the correct answer for
	// "nothing has ever been observed yet" — see this field's use in tick.
	lastLagMS int64
}

// NewStatsBroadcaster constructs a StatsBroadcaster. hub and snap must be
// non-nil. A nil logger defaults to slog.Default(), matching every other
// constructor in this codebase's nil-logger convention.
func NewStatsBroadcaster(hub StatsTarget, snap SnapshotFunc, interval time.Duration, logger *slog.Logger) *StatsBroadcaster {
	if logger == nil {
		logger = slog.Default()
	}
	return &StatsBroadcaster{hub: hub, snap: snap, interval: interval, logger: logger}
}

// Run takes a baseline Snapshot immediately, before starting the ticker —
// so the FIRST published frame covers a true interval-long window instead
// of a fabricated one measured from process start (which could be
// arbitrarily longer or shorter than `interval`, depending on when in the
// startup sequence Run happens to be called). If the baseline read fails,
// it is logged and skipped; the next successful read (baseline or
// tick-time) becomes the new starting point, and no Stats frame is
// published from a window that has no valid starting snapshot to diff
// against. Returns when ctx is done.
func (b *StatsBroadcaster) Run(ctx context.Context) {
	if snap, err := b.snap(ctx); err != nil {
		b.logger.Error("stream: stats broadcaster: baseline snapshot failed", "error", err)
	} else {
		b.prev = snap
		b.prevAt = time.Now()
		b.havePrev = true
	}

	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.tick(ctx)
		}
	}
}

// tick reads one fresh Snapshot and derives+publishes a Stats frame from
// the delta against b.prev, using the MEASURED elapsed wall-clock time
// between the two readings (never the nominal `interval`) — a GC pause, a
// slow snap() call, or ordinary scheduler jitter all make the true window
// slightly longer than `interval`, and dividing by the nominal value would
// systematically bias events_per_sec.
//
// A snapshot error is logged and this tick is skipped entirely: b.prev is
// left untouched, so the NEXT successful tick still diffs against a valid
// starting point (a longer, but still correct, window) rather than
// publishing a frame derived from a failed read.
func (b *StatsBroadcaster) tick(ctx context.Context) {
	now := time.Now()
	snap, err := b.snap(ctx)
	if err != nil {
		b.logger.Error("stream: stats broadcaster: snapshot failed, skipping this tick", "error", err)
		return
	}
	if !b.havePrev {
		// Only reachable if Run's own baseline read failed: treat this as
		// the new baseline instead of diffing against the zero value, which
		// would report a nonsensical events_per_sec spike from "0" to
		// whatever EventsTotal has accumulated since process start.
		b.prev, b.prevAt, b.havePrev = snap, now, true
		return
	}

	elapsed := now.Sub(b.prevAt).Seconds()
	var eventsPerSec float64
	if elapsed > 0 {
		eventsPerSec = (snap.EventsTotal - b.prev.EventsTotal) / elapsed
	}

	// IngestLagMS: mean lag over the window = (LagSum-prevLagSum) /
	// (LagCount-prevLagCount) * 1000, rounded. When LagCount did not change
	// (no events were ingested during this window), that mean is
	// undefined — dividing by zero, not "zero lag" — so the last value ever
	// observed is carried forward instead (b.lastLagMS's own doc comment).
	// openapi.yaml's StreamStatsFrame.ingest_lag_ms is a required
	// non-nullable integer, so there is no `null` available to mean
	// "nothing measured this window"; 0 here means "nothing has EVER been
	// measured yet", not a measured zero-lag reading — a minor SPEC §4.1
	// null-vs-zero tension worth a lead ruling, not something this file can
	// resolve on its own by widening the wire schema.
	//
	// snap.LagCount > b.prev.LagCount (strictly greater, not !=) also
	// guards against a counter that somehow went backwards (a metrics
	// registry reset mid-process, which should not happen in production but
	// costs nothing to not trust blindly): if it ever did, this tick simply
	// carries the last good value forward exactly like a zero-delta window
	// would, rather than computing a nonsensical negative-count division.
	if snap.LagCount > b.prev.LagCount {
		lagCountDelta := snap.LagCount - b.prev.LagCount
		lagSumDelta := snap.LagSum - b.prev.LagSum
		meanLagSeconds := lagSumDelta / float64(lagCountDelta)
		b.lastLagMS = int64(math.Round(meanLagSeconds * 1000))
	}

	b.hub.PublishStats(Stats{
		EventsPerSec:   eventsPerSec,
		ActiveSessions: snap.ActiveSessions,
		QueueDepth:     snap.QueueDepth,
		IngestLagMS:    b.lastLagMS,
		DroppedTotal:   snap.DroppedTotal,
	})

	b.prev, b.prevAt = snap, now
}
