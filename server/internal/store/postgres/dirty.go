// Package postgres implements dirty-marking via rollup_dirty (SPEC §2.4, lock-ordering SPEC §1.6).
// Marks in-transaction dedupes sequence allocation immunity.
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// sourceEvent and sourceMetric are rollup_dirty.source's two values (SPEC §2.4).
// Deliberately plain constants, not an enum: they predate a dedicated type.
const (
	sourceEvent  = "event"
	sourceMetric = "metric"
)

// hourBucket truncates ts to hour UTC; computed in Go to dedupe before SQL.
func hourBucket(ts time.Time) time.Time {
	t := ts.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

// hourBucketsBetween returns hour buckets [first, last] capped at maxBuckets, reporting truncation.
func hourBucketsBetween(first, last time.Time, maxBuckets int) (buckets []time.Time, truncated bool) {
	start := hourBucket(first)
	end := hourBucket(last)
	if end.Before(start) {
		start, end = end, start
	}
	for b := start; !b.After(end); b = b.Add(time.Hour) {
		if len(buckets) >= maxBuckets {
			return buckets, true
		}
		buckets = append(buckets, b)
	}
	return buckets, false
}

// dirtyMark represents one (bucket, source) pair for rollup_dirty.
type dirtyMark struct {
	Bucket time.Time
	Source string
}

// markRollupDirty inserts deduped marks into rollup_dirty, sorted by (bucket, source) (SPEC §1.6).
func markRollupDirty(ctx context.Context, tx pgx.Tx, marks []dirtyMark) error {
	if len(marks) == 0 {
		return nil
	}

	dedup := make(map[dirtyMark]struct{}, len(marks))
	for _, m := range marks {
		dedup[m] = struct{}{}
	}
	uniq := make([]dirtyMark, 0, len(dedup))
	for m := range dedup {
		uniq = append(uniq, m)
	}
	sort.Slice(uniq, func(i, j int) bool {
		if !uniq[i].Bucket.Equal(uniq[j].Bucket) {
			return uniq[i].Bucket.Before(uniq[j].Bucket)
		}
		return uniq[i].Source < uniq[j].Source
	})

	buckets := make([]time.Time, len(uniq))
	sources := make([]string, len(uniq))
	for i, m := range uniq {
		buckets[i] = m.Bucket
		sources[i] = m.Source
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO rollup_dirty (bucket, source)
		SELECT * FROM unnest($1::timestamptz[], $2::text[])
		ON CONFLICT (bucket, source) DO NOTHING`,
		buckets, sources)
	if err != nil {
		return fmt.Errorf("postgres: mark rollup_dirty: %w", err)
	}
	return nil
}

// projectChangeRemarks implements the SPEC §2.4 second dirty-marking rule:
// when a session's project/cwd changed in this batch (late-SessionStart case),
// mark hour buckets [first_seen_at, last_event_at] dirty, capped with warning
// if needed. changed[id] reports whether that session's cwd/project actually
// changed; span[id] holds its merged [first_seen_at, last_event_at] bounds.
func (s *Store) projectChangeRemarks(changed map[string]bool, span map[string][2]time.Time) []dirtyMark {
	var marks []dirtyMark
	for id, didChange := range changed {
		if !didChange {
			continue
		}
		bounds, ok := span[id]
		if !ok {
			continue
		}
		buckets, truncated := hourBucketsBetween(bounds[0], bounds[1], s.rollupSessionRemarkMax)
		if truncated {
			slog.Default().Warn("postgres: rollup_dirty re-mark capped by ARGUS_ROLLUP_SESSION_REMARK_MAX",
				"session_id", id, "cap", s.rollupSessionRemarkMax)
		}
		for _, b := range buckets {
			marks = append(marks, dirtyMark{Bucket: b, Source: sourceEvent})
		}
	}
	return marks
}
