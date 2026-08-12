package model

import "time"

// clockClampFuture is the fixed upper bound of the clock-sanity window
// (SPEC §1.2: "ts outside [now − ARGUS_RETENTION_RAW_DAYS, now + 1h]"). Only
// the lower bound is tied to retention; the future bound is a flat 1h
// regardless of retention window size.
const clockClampFuture = time.Hour

// ClampTimestamp sanity-clamps an agent-reported event timestamp against the
// server clock (SPEC §1.2). ts is accepted verbatim when it falls in
// [now-retention, now+1h]; otherwise the returned timestamp is now and
// skewed is true, so the event lands in a partition that actually exists
// (no clamped event can address a dropped partition) and is flagged
// clock_skewed for the data-quality surface.
//
// retention is passed in, not a package constant, because SPEC §1.2 is
// explicit that the lower bound must track ARGUS_RETENTION_RAW_DAYS: "so a
// legitimate backfill inside the retention window is never rewritten."
func ClampTimestamp(ts, now time.Time, retention time.Duration) (clamped time.Time, skewed bool) {
	lower := now.Add(-retention)
	upper := now.Add(clockClampFuture)
	if ts.Before(lower) || ts.After(upper) {
		return now, true
	}
	return ts, false
}
