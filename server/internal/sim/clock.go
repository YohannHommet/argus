package sim

import "time"

// FixedEpoch is the deterministic clock origin for --out/--deterministic
// (SPEC §7.2: ensures identical seed ⇒ byte-identical payloads).
var FixedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ResolveClockOrigin implements SPEC §7.2's --clock-origin defaults:
// explicit flag, FixedEpoch if --out/--deterministic, else now - backfill.
func ResolveClockOrigin(explicit string, useOut, deterministic bool, nowFn func() time.Time, backfill time.Duration) (time.Time, error) {
	if explicit != "" {
		return time.Parse(time.RFC3339, explicit)
	}
	if useOut || deterministic {
		return FixedEpoch, nil
	}
	return nowFn().Add(-backfill), nil
}

// Clock is the single time source for all event timestamps (wrapped by
// chaos-clock-skew). Maps cursor (simulated seconds since origin) to wall
// timestamp. Speed only affects pacing of live POSTs, not event timestamps
// (always Origin + uncompressed cursor, per SPEC §7.2).
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
