package sim

import "time"

// logFlushInterval / metricFlushInterval mirror SPEC §7.2's "Batching
// mirrors Claude Code's defaults (logs 5s, metrics 60s) unless
// --flush-immediately". hookFlushInterval is this package's own choice for
// how it groups synchronous hook POSTs into the JSON-array batch shape
// HookNormalizer.FromHookPayload documents as existing specifically "for
// batch replay by argus-sim" (SPEC §3.5) — real Claude Code sends one hook
// per POST, so this constant has no SPEC citation of its own; it reuses the
// logs cadence purely so a session's hook and log batches interleave at a
// familiar rhythm in --out fixtures.
const (
	logFlushInterval    = 5 * time.Second
	metricFlushInterval = 60 * time.Second
	hookFlushInterval   = 5 * time.Second
)

// batchByInterval groups items (already in non-decreasing ts order, which
// every sessionResult slice is by construction — session.go only ever
// appends after advancing the cursor) into windows no wider than interval.
// immediate=true puts every item in its own batch (SPEC §7.2's
// --flush-immediately).
func batchByInterval[T any](items []T, tsOf func(T) time.Time, interval time.Duration, immediate bool) [][]T {
	if len(items) == 0 {
		return nil
	}
	if immediate {
		out := make([][]T, len(items))
		for i, it := range items {
			out[i] = []T{it}
		}
		return out
	}

	var out [][]T
	windowStart := tsOf(items[0])
	var current []T
	for _, it := range items {
		if len(current) > 0 && tsOf(it).Sub(windowStart) >= interval {
			out = append(out, current)
			current = nil
			windowStart = tsOf(it)
		}
		current = append(current, it)
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}
