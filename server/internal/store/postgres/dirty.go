// Package postgres — dirty.go implements SPEC §2.4's dirty-marking rules,
// the last step of WriteBatch/WriteMetrics's fixed lock order (SPEC §1.6):
// every batch marks the hour buckets it touched in rollup_dirty inside the
// same transaction as the events/projection writes, which is what makes the
// rollup job immune to pre-commit sequence allocation (SPEC §2.4's
// "Why there is no events.seq watermark").
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// sourceEvent and sourceMetric are rollup_dirty.source's two values (SPEC
// §2.4). Deliberately plain constants, not a Go enum type: source is one of
// Argus's own four closed taxonomies (SPEC §0), but rollup_dirty predates
// this ticket introducing a dedicated type for it, and two string constants
// used only inside this package need no more ceremony than that.
const (
	sourceEvent  = "event"
	sourceMetric = "metric"
)

// hourBucket truncates ts to the hour, UTC — SPEC §2.4's
// "date_trunc('hour', ts)" bucket key, computed in Go so WriteBatch can
// dedupe buckets before ever issuing SQL.
func hourBucket(ts time.Time) time.Time {
	t := ts.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

// hourBucketsBetween returns every hour bucket in [first, last], inclusive,
// capped at max entries (SPEC §2.4's ARGUS_ROLLUP_SESSION_REMARK_MAX). If
// the true range exceeds max, only the first max buckets (from `first`
// forward) are returned and truncated is true, so the caller can log the
// SPEC-required warning.
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

// dirtyMark is one (bucket, source) pair to upsert into rollup_dirty.
type dirtyMark struct {
	Bucket time.Time
	Source string
}

// markRollupDirty inserts marks into rollup_dirty (deduped, sorted by
// (bucket, source) ascending — the lock-ordering invariant's last statement,
// SPEC §1.6) inside tx. ON CONFLICT DO NOTHING: a bucket already dirty from
// an earlier statement in this same run, or from a concurrent batch, needs
// no second row.
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

// projectChangeRemarks builds the SPEC §2.4 second dirty-marking rule: when
// a session's project/cwd changed in this batch (the late-SessionStart
// case), every hour bucket from the session's (post-merge) first_seen_at to
// last_event_at is re-marked dirty, source='event', capped at
// s.rollupSessionRemarkMax with a logged warning if the true range is
// larger. changed reports, per session id, whether its stored cwd/project
// value actually changed (comparing the upsert's before/after RETURNING),
// and span reports each such session's merged [first_seen_at, last_event_at].
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
