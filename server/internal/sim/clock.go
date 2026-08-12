package sim

import "time"

// FixedEpoch is the deterministic clock origin SPEC §7.2 mandates whenever
// --out is used or --deterministic is passed: "defaulting to the fixed
// epoch 2026-01-01T00:00:00Z … Without a fixed origin, 'identical seed ⇒
// byte-identical payloads' is false (timestamps move with the wall clock)".
var FixedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ResolveClockOrigin implements SPEC §7.2's --clock-origin default logic.
// explicit is the --clock-origin flag value (empty means "not passed").
// useOut and deterministic are --out being set and --deterministic being
// passed, respectively; nowFn/backfill implement the "now - backfill"
// branch for live (non-fixture) runs.
func ResolveClockOrigin(explicit string, useOut, deterministic bool, nowFn func() time.Time, backfill time.Duration) (time.Time, error) {
	if explicit != "" {
		return time.Parse(time.RFC3339, explicit)
	}
	if useOut || deterministic {
		return FixedEpoch, nil
	}
	return nowFn().Add(-backfill), nil
}

// Clock is the single time source every generated event's timestamp goes
// through (doc.go's chaos-hooks note: chaos-clock-skew wraps this). It maps
// a monotonically increasing "simulated seconds since origin" cursor to a
// wall timestamp, compressed by --speed (SPEC §7.2: "--speed=X compresses
// simulated time, so a 14-day backfill lands in seconds" — speed only
// matters for how fast a *live* run's real POSTs are paced; the timestamps
// stamped onto events are always Origin + cursor, uncompressed, so a
// backfilled event's ts is genuinely 14 days old regardless of how fast the
// process producing it runs).
type Clock struct {
	Origin time.Time
}

// NewClock builds a Clock anchored at origin.
func NewClock(origin time.Time) Clock {
	return Clock{Origin: origin}
}

// At returns the wall timestamp for a cursor expressed as a duration since
// Origin.
func (c Clock) At(offset time.Duration) time.Time {
	return c.Origin.Add(offset)
}
