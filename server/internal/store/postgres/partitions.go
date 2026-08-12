// Package postgres — partitions.go implements the SPEC §2.4 partition
// manager for the two RANGE-partitioned tables (events, metric_samples):
// EnsurePartitions creates monthly partitions and their per-partition
// indexes idempotently, and IsTooOld classifies the Postgres error raised
// when a row's ts falls outside every existing partition (SPEC §1.7 rule
// 3) — there is deliberately no DEFAULT partition (SPEC §2.2) to swallow
// such rows silently.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// eventsIndexDDL returns the 6 per-partition indexes SPEC §2.2 names for an
// events partition. The UNIQUE (ts, dedup_key) index is deliberately absent
// here: it lives on the parent constraint (002_events.sql) and Postgres
// creates it automatically on every partition — creating it again would be
// a duplicate-index bug, not a no-op.
func eventsIndexDDL(partition pgx.Identifier) []string {
	p := partition.Sanitize()
	name := partition[len(partition)-1]
	return []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (session_id, ts, seq)`,
			pgx.Identifier{name + "_session_ts_seq_idx"}.Sanitize(), p),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (kind, ts DESC)`,
			pgx.Identifier{name + "_kind_ts_idx"}.Sanitize(), p),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (session_id, tool_use_id) WHERE tool_use_id IS NOT NULL`,
			pgx.Identifier{name + "_session_tool_use_idx"}.Sanitize(), p),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (session_id, prompt_id) WHERE prompt_id IS NOT NULL`,
			pgx.Identifier{name + "_session_prompt_idx"}.Sanitize(), p),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (ingested_at)`,
			pgx.Identifier{name + "_ingested_at_idx"}.Sanitize(), p),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (decision_source) WHERE decision_source IS NOT NULL`,
			pgx.Identifier{name + "_decision_source_idx"}.Sanitize(), p),
	}
}

// metricSamplesIndexDDL returns the 2 per-partition indexes SPEC §2.3 names
// for a metric_samples partition.
func metricSamplesIndexDDL(partition pgx.Identifier) []string {
	p := partition.Sanitize()
	name := partition[len(partition)-1]
	return []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (name, ts DESC)`,
			pgx.Identifier{name + "_name_ts_idx"}.Sanitize(), p),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (series_hash, ts)`,
			pgx.Identifier{name + "_series_hash_ts_idx"}.Sanitize(), p),
	}
}

// EnsurePartitions implements store.Maintenance (SPEC §2.4, §3.3): it
// creates, idempotently, one monthly RANGE partition per calendar month
// from the month containing `from` through the month containing `to`
// (inclusive of both), for both `events` and `metric_samples`, plus each
// partition's per-partition indexes. `CREATE TABLE IF NOT EXISTS` and
// `CREATE INDEX IF NOT EXISTS` make a repeated call over the same range a
// no-op. There is no DEFAULT partition to create — SPEC §2.2 requires an
// out-of-range insert to error (see IsTooOld).
func (s *Store) EnsurePartitions(ctx context.Context, from, to time.Time) error {
	start := time.Date(from.UTC().Year(), from.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(to.UTC().Year(), to.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	if end.Before(start) {
		return fmt.Errorf("postgres: ensure partitions: to (%s) precedes from (%s)", to, from)
	}

	for month := start; !month.After(end); month = month.AddDate(0, 1, 0) {
		next := month.AddDate(0, 1, 0)

		if err := ensureMonthlyPartition(ctx, s.pool, "events", month, next, eventsIndexDDL); err != nil {
			return err
		}
		if err := ensureMonthlyPartition(ctx, s.pool, "metric_samples", month, next, metricSamplesIndexDDL); err != nil {
			return err
		}
	}
	return nil
}

// ensureMonthlyPartition creates one `parent_YYYY_MM` partition of `parent`
// covering [month, next) if it does not already exist, then runs indexDDL
// against it. `CREATE TABLE ... PARTITION OF ... FOR VALUES FROM/TO` is a
// utility statement Postgres refuses to plan with bind parameters
// ("syntax error" on PREPARE, verified empirically — see this ticket's
// report), so the bounds are formatted as RFC 3339 literals instead; this
// is safe because month/next are computed internally from a time.Time, never
// from untrusted input. Table and index identifiers, which Postgres gives
// no placeholder syntax for regardless, are quoted via
// pgx.Identifier.Sanitize rather than raw interpolation.
func ensureMonthlyPartition(ctx context.Context, pool *pgxpool.Pool, parent string, month, next time.Time, indexDDL func(pgx.Identifier) []string) error {
	partitionName := fmt.Sprintf("%s_%04d_%02d", parent, month.Year(), month.Month())
	partition := pgx.Identifier{partitionName}

	createSQL := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
		partition.Sanitize(), pgx.Identifier{parent}.Sanitize(),
		month.UTC().Format(time.RFC3339), next.UTC().Format(time.RFC3339),
	)
	if _, err := pool.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("postgres: ensure partitions: creating %s: %w", partitionName, err)
	}

	for _, ddl := range indexDDL(partition) {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("postgres: ensure partitions: indexing %s: %w", partitionName, err)
		}
	}
	return nil
}

// tooOldSQLSTATE is check_violation. Postgres overloads it: a partitioned
// table with no matching (and no DEFAULT) partition raises it with the
// message below (verified empirically against postgres:18-alpine — see
// this ticket's report), but so does an ordinary CHECK constraint. Argus
// has zero CHECK constraints (SPEC §0), but IsTooOld still matches on the
// message text too, so it can never be fooled by one appearing later.
const tooOldSQLSTATE = "23514"

// tooOldMessagePrefix is the verbatim, version-stable prefix Postgres uses
// for "no partition found" — as opposed to "new row ... violates check
// constraint ...", which is the other 23514 producer.
const tooOldMessagePrefix = "no partition of relation"

// IsTooOld reports whether err is the Postgres error raised when an
// events/metric_samples insert's ts falls outside every existing partition
// (SPEC §1.7 rule 3): "no partition of relation ... found for row",
// SQLSTATE 23514 (check_violation). There is no DEFAULT partition (SPEC
// §2.2) specifically so this error is observable and classifiable — a
// silent catch-all partition would make `too_old` undetectable. Any other
// 23514 (e.g. a genuine CHECK constraint violation, should one ever be
// added against this rule) does not match.
func IsTooOld(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != tooOldSQLSTATE {
		return false
	}
	return strings.HasPrefix(pgErr.Message, tooOldMessagePrefix)
}
