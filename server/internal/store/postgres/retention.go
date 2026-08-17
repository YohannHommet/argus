// Package postgres — retention.go implements store.Maintenance.ApplyRetention
// and PruneDedup (SPEC §2.4 "Retention job", P3-10): dropping fully-expired
// monthly `events`/`metric_samples` partitions (coarse, SPEC §2.2: "a
// partition drops only when entirely older than the cutoff"), an additional
// `--precise` batched-delete mode for the one boundary partition a coarse
// drop cannot touch, and pruning `ingest_dedup` in bounded batches (SPEC
// §1.7 rule 2's ledger retention).
//
// # Partition listing duplicates partitionCoverage's pg_inherits query
//
// partitions.go's partitionCoverage (P2-05) already lists a parent's
// attached monthly partitions via pg_inherits and parses their [start, end)
// range from the name via monthlyPartitionName — exactly what ApplyRetention
// needs to decide which partitions are fully expired. This file reuses
// monthlyPartitionName and monthRange (both already unexported package-level
// symbols in partitions.go) rather than duplicating the regex, but it does
// re-issue the pg_inherits query itself (listPartitionRanges below) instead
// of calling partitionCoverage: this ticket's file-ownership boundary
// (P3-10 owns retention.go/rebuild.go/explain_test.go plus the ApplyRetention/
// PruneDedup/RebuildProjections stub lines in pool.go — partitions.go itself
// is out of scope) rules out refactoring partitionCoverage into a shared
// "list partitions with their ranges" helper both files could call. The
// duplication is small (one query, one loop) and documented here rather than
// silently repeated.
package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// retentionPartitionParents are the two SPEC §2.2/§2.3 RANGE-partitioned
// tables the retention job drops expired partitions from.
var retentionPartitionParents = []string{"events", "metric_samples"}

// pruneDedupBatchSize bounds each PruneDedup DELETE (SPEC §2.4: "in bounded
// batches"), mirroring rollups.go's ClaimDirtyBuckets/ARGUS_ROLLUP_MAX_BUCKETS
// idiom of capping one statement's row count rather than deleting the whole
// expired set in one unbounded statement, which could hold a lock for a long
// time against a large backlog.
const pruneDedupBatchSize = 5000

// listPartitionRanges lists, inside tx, the monthly partitions currently
// attached to parent ("events" or "metric_samples") and their [Start, End)
// coverage, keyed by partition name. See this file's package doc for why
// this duplicates (rather than calls) partitions.go's partitionCoverage.
func listPartitionRanges(ctx context.Context, tx pgx.Tx, parent string) (map[string]monthRange, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE p.relname = $1 AND n.nspname = current_schema()`, parent)
	if err != nil {
		return nil, fmt.Errorf("postgres: list partitions: %s: %w", parent, err)
	}
	defer rows.Close()

	out := map[string]monthRange{}
	for rows.Next() {
		var relname string
		if err := rows.Scan(&relname); err != nil {
			return nil, fmt.Errorf("postgres: list partitions: scanning %s: %w", parent, err)
		}
		m := monthlyPartitionName.FindStringSubmatch(relname)
		if m == nil || m[1] != parent {
			continue
		}
		var year, month int
		if _, err := fmt.Sscanf(m[2]+" "+m[3], "%d %d", &year, &month); err != nil {
			continue
		}
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		out[relname] = monthRange{Start: start, End: start.AddDate(0, 1, 0)}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list partitions: %s: %w", parent, err)
	}
	return out, nil
}

// ApplyRetention implements store.Maintenance (SPEC §2.4 "Retention job",
// P3-10): drops every events/metric_samples monthly partition whose upper
// bound is at or before cutoff (coarse retention, SPEC §2.2 — a partition
// drops only when *entirely* older than the cutoff), inside one transaction
// so the drop set is all-or-nothing. dryRun=true lists exactly the
// partitions that would be dropped and changes nothing (the transaction is
// rolled back, never committed). Rollups (rollup_hourly/rollup_daily) and
// the four projection tables (sessions/turns/tool_calls/subagents) are never
// touched here — SPEC §2.4: "rollups and projections are never deleted by
// raw retention" — which is what leaves a dropped period's rollup rows and
// its sessions intact with an empty timeline.
//
// The returned slice is sorted for deterministic test/log output; it names
// bare partition table names (e.g. "events_2026_02"), not schema-qualified.
func (s *Store) ApplyRetention(ctx context.Context, cutoff time.Time, dryRun bool) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: apply retention: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit; the only path on dryRun

	cutoffUTC := cutoff.UTC()
	var expired []string
	for _, parent := range retentionPartitionParents {
		ranges, err := listPartitionRanges(ctx, tx, parent)
		if err != nil {
			return nil, err
		}
		for name, r := range ranges {
			if !r.End.After(cutoffUTC) { // End <= cutoff: entirely expired
				expired = append(expired, name)
			}
		}
	}
	sort.Strings(expired)

	if dryRun {
		return expired, nil
	}

	for _, name := range expired {
		if _, err := tx.Exec(ctx, `DROP TABLE `+pgx.Identifier{name}.Sanitize()); err != nil {
			return nil, fmt.Errorf("postgres: apply retention: dropping %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: apply retention: commit: %w", err)
	}
	return expired, nil
}

// ApplyRetentionPrecise implements the SPEC §2.2/§2.4 "--precise" retention
// mode: `DELETE`s, in bounded batches, every events/metric_samples row with
// ts < cutoff. It is a Store-only method (not part of store.Maintenance,
// which fixes ApplyRetention's signature at (cutoff, dryRun) with no room
// for a --precise flag) — the same "extra concrete method beyond the
// interface" shape as MigrateStatus/ImportPrices, called directly by
// argusd's retention subcommand and the daily RetentionJob when
// --precise/ARGUS_RETENTION_PRECISE is requested.
//
// A coarse ApplyRetention pass only ever leaves stale rows in the ONE
// partition straddling cutoff (SPEC §2.2: "a partition only drops when
// entirely older than the cutoff") — every other partition is either fully
// dropped already or entirely in the future. Deleting `WHERE ts < cutoff`
// against the parent table therefore only ever touches that boundary
// partition; Postgres's partition pruning routes the DELETE there without
// this code needing to name it. Call ApplyRetention first (or accept that a
// stale boundary partition needs multiple ApplyRetentionPrecise/
// ApplyRetention cycles as it drains): this method never drops a partition,
// only rows within one.
func (s *Store) ApplyRetentionPrecise(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for _, parent := range retentionPartitionParents {
		pkCols := "ts, seq"
		if parent == "metric_samples" {
			pkCols = "ts, series_hash, dedup_key"
		}
		for {
			tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
				DELETE FROM %s
				WHERE (%s) IN (
					SELECT %s FROM %s WHERE ts < $1 ORDER BY ts LIMIT %d
				)`, parent, pkCols, pkCols, parent, pruneDedupBatchSize), cutoff)
			if err != nil {
				return total, fmt.Errorf("postgres: apply retention precise: %s: %w", parent, err)
			}
			n := tag.RowsAffected()
			total += n
			if n < pruneDedupBatchSize {
				break
			}
		}
	}
	return total, nil
}

// sessionRetentionBatchSize bounds each DeleteExpiredSessions DELETE (m11
// fix), the same bounded-batch idiom PruneDedup/ApplyRetentionPrecise use:
// one long-running unbounded DELETE against `sessions` could hold locks (and
// cascade through turns/tool_calls/subagents) for a long time against a
// large backlog.
const sessionRetentionBatchSize = 5000

// DeleteExpiredSessions implements the ARGUS_RETENTION_SESSION_DAYS half of
// SPEC §2.4's "Retention job" bullet ("optionally deletes `sessions` older
// than ARGUS_RETENTION_SESSION_DAYS … cascading to turns/tool_calls/
// subagents"). It deletes, in bounded batches of sessionRetentionBatchSize,
// every `sessions` row whose last_event_at is older than cutoff; the
// `REFERENCES sessions(id) ON DELETE CASCADE` FKs on turns/tool_calls/
// subagents (SPEC §2.1/§2.3) do the rest inside the same statement.
//
// rollup_hourly/rollup_daily are never touched — they carry no session_id
// (SPEC §2.4's rollup_hourly schema keys on bucket/project/vendor/model/
// source only) — matching SPEC §2.4's "rollups and projections are never
// deleted by raw retention" for the exact same reason ApplyRetention's own
// doc comment gives: a deleted session's cost stays in its bucket forever,
// which is the documented trade-off (a UI showing an aggregate with no
// corresponding session row), not a bug this method could introduce.
//
// This key is separate from ARGUS_RETENTION_RAW_DAYS/ApplyRetention: a
// session can easily outlive its raw events (default 0 = never delete
// sessions at all, only their events age out via the coarse partition
// drop), so cutoff is computed independently by the caller from
// ARGUS_RETENTION_SESSION_DAYS, not from the raw-events cutoff.
func (s *Store) DeleteExpiredSessions(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM sessions
			WHERE id IN (
				SELECT id FROM sessions WHERE last_event_at < $1 ORDER BY last_event_at LIMIT $2
			)`, cutoff, sessionRetentionBatchSize)
		if err != nil {
			return total, fmt.Errorf("postgres: delete expired sessions: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < sessionRetentionBatchSize {
			break
		}
	}
	return total, nil
}

// PruneDedup implements store.Maintenance (SPEC §1.7 rule 2, §2.4): deletes
// ingest_dedup rows whose first_seen_at is older than cutoff
// (now - ARGUS_DEDUP_WINDOW) in bounded batches of pruneDedupBatchSize,
// leaving every newer row untouched. Each batch is its own statement/
// implicit transaction rather than one long-running DELETE, so a large
// backlog never holds a single lock for an extended period — the same
// bounded-batch rationale as ApplyRetentionPrecise and rollups.go's
// ClaimDirtyBuckets.
func (s *Store) PruneDedup(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM ingest_dedup
			WHERE dedup_key IN (
				SELECT dedup_key FROM ingest_dedup WHERE first_seen_at < $1 ORDER BY first_seen_at LIMIT $2
			)`, cutoff, pruneDedupBatchSize)
		if err != nil {
			return total, fmt.Errorf("postgres: prune dedup: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < pruneDedupBatchSize {
			break
		}
	}
	return total, nil
}
