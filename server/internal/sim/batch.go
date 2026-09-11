package sim

import "time"

// logFlushInterval / metricFlushInterval mirror SPEC §7.2's batching
// defaults (logs 5s, metrics 60s). hookFlushInterval groups hook POSTs into
// batches for replay (SPEC §3.5); it reuses the logs cadence for fixture
// interleaving (no SPEC citation of its own).
const (
	logFlushInterval    = 5 * time.Second
	metricFlushInterval = 60 * time.Second
	hookFlushInterval   = 5 * time.Second
)

// batchByInterval groups items (in non-decreasing ts order by invariant)
// into windows no wider than interval. immediate=true puts every item in
// its own batch (SPEC §7.2's --flush-immediately).
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
