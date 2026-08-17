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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// partitionsLockKey is the pg_try_advisory_xact_lock key EnsurePartitions
// takes to single-flight partition creation across processes (m34 fix).
// Transaction-scoped, same idiom as rollups.go's rollupLockKey: it releases
// itself on COMMIT or ROLLBACK, so there is no matching unlock call to get
// right. Continues this codebase's lock-key registry (migrate.go's
// migrationLockKey "ARGUS01", rollups.go's rollupLockKey "ARGUS02") —
// "ARGUS03" is already claimed (ticket W8, rebuild-projections), so this one
// is "ARGUS04" to avoid a collision.
//
// Without this lock, two argusd processes starting together (or a process
// racing PartitionJob's hourly tick against another replica's) could both
// observe "table does not exist" from CREATE TABLE IF NOT EXISTS's own
// existence check before either takes Postgres's internal lock on the
// parent, and one loses the race with 42P07/23505 — narrow today (single-
// argusd topology, `restart: unless-stopped` recovers) but real, and fatal
// at startup (App.New calls this and treats a failure as fatal, per SPEC
// §2.4's "startup fails loudly"). Serializing via this lock means the loser
// simply waits, then finds every table/index already created by the winner
// (still idempotent, still a no-op) instead of erroring.
const partitionsLockKey int64 = 0x41_52_47_55_53_30_34 // "ARGUS04"

// execer is the subset of pgxpool.Pool/pgx.Tx that ensureMonthlyPartition
// needs, so EnsurePartitions can run every CREATE TABLE/INDEX statement
// inside the same advisory-locked transaction rather than against the pool
// directly.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

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
//
// The whole pass runs inside one transaction holding partitionsLockKey (m34
// fix): a non-blocking pg_try_advisory_xact_lock would need a caller-visible
// "someone else is already doing this" signal this method's signature has
// no room for, so this uses the blocking pg_advisory_xact_lock instead — a
// second concurrent caller simply waits for the first to commit, then finds
// every table/index it needed already created (still idempotent), rather
// than racing CREATE TABLE IF NOT EXISTS's own existence check against a
// concurrent process and losing with 42P07/23505.
func (s *Store) EnsurePartitions(ctx context.Context, from, to time.Time) error {
	start := time.Date(from.UTC().Year(), from.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(to.UTC().Year(), to.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	if end.Before(start) {
		return fmt.Errorf("postgres: ensure partitions: to (%s) precedes from (%s)", to, from)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: ensure partitions: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", partitionsLockKey); err != nil {
		return fmt.Errorf("postgres: ensure partitions: advisory lock: %w", err)
	}

	for month := start; !month.After(end); month = month.AddDate(0, 1, 0) {
		next := month.AddDate(0, 1, 0)

		if err := ensureMonthlyPartition(ctx, tx, "events", month, next, eventsIndexDDL); err != nil {
			return err
		}
		if err := ensureMonthlyPartition(ctx, tx, "metric_samples", month, next, metricSamplesIndexDDL); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: ensure partitions: commit: %w", err)
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
// pgx.Identifier.Sanitize rather than raw interpolation. exec is
// EnsurePartitions's own locked transaction (see its doc comment, m34 fix),
// not the bare pool, so every statement this issues is covered by
// partitionsLockKey.
func ensureMonthlyPartition(ctx context.Context, exec execer, parent string, month, next time.Time, indexDDL func(pgx.Identifier) []string) error {
	partitionName := fmt.Sprintf("%s_%04d_%02d", parent, month.Year(), month.Month())
	partition := pgx.Identifier{partitionName}

	createSQL := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
		partition.Sanitize(), pgx.Identifier{parent}.Sanitize(),
		month.UTC().Format(time.RFC3339), next.UTC().Format(time.RFC3339),
	)
	if _, err := exec.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("postgres: ensure partitions: creating %s: %w", partitionName, err)
	}

	for _, ddl := range indexDDL(partition) {
		if _, err := exec.Exec(ctx, ddl); err != nil {
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

// monthlyPartitionName matches the `<parent>_YYYY_MM` naming
// ensureMonthlyPartition uses, so partitionCoverage can recover a
// partition's [start, end) range from its name alone instead of parsing
// pg_get_expr's textual partition-bound representation.
var monthlyPartitionName = regexp.MustCompile(`^(.+)_(\d{4})_(\d{2})$`)

// monthRange is one events/metric_samples monthly partition's covered
// [Start, End) window, UTC.
type monthRange struct {
	Start, End time.Time
}

// partitionCoverage reads, inside tx, the set of monthly partitions
// currently attached to parent ("events" or "metric_samples") and returns a
// predicate reporting whether a given ts falls inside one of them.
//
// WriteBatch/WriteMetrics use this as a pre-flight, in-transaction
// too_old check performed *before* building the sessions/turns/rollup_dirty
// projection statements (SPEC §1.7 rule 3), rather than relying solely on
// the reactive "no partition of relation" error IsTooOld classifies. This is
// a deliberate P2-06 design choice, not a spec deviation in the ON CONFLICT
// or ledger sense: SPEC §1.6's fixed lock order updates sessions/turns
// *before* events, so by the time an events insert could raise IsTooOld the
// projection statements touching those rows would already be queued. Precomputing
// too_old membership up front keeps the projection aggregates in Go
// consistent with exactly the rows that will actually reach `events`,
// without reordering the invariant's table sequence. IsTooOld itself is
// still exercised as defence in depth: if a partition is dropped by the
// retention job between this check and the insert (an unavoidable, narrow
// TOCTOU window), the insert still errors and that error still propagates
// as a normal write failure — an accepted, documented trade-off for a race
// this rare.
func partitionCoverage(ctx context.Context, tx pgx.Tx, parent string) (func(ts time.Time) bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE p.relname = $1 AND n.nspname = current_schema()`, parent)
	if err != nil {
		return nil, fmt.Errorf("postgres: partition coverage: listing %s partitions: %w", parent, err)
	}
	defer rows.Close()

	var ranges []monthRange
	for rows.Next() {
		var relname string
		if err := rows.Scan(&relname); err != nil {
			return nil, fmt.Errorf("postgres: partition coverage: scanning %s partitions: %w", parent, err)
		}
		m := monthlyPartitionName.FindStringSubmatch(relname)
		if m == nil || m[1] != parent {
			continue
		}
		year, yerr := strconv.Atoi(m[2])
		month, merr := strconv.Atoi(m[3])
		if yerr != nil || merr != nil {
			continue
		}
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		ranges = append(ranges, monthRange{Start: start, End: start.AddDate(0, 1, 0)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: partition coverage: %s: %w", parent, err)
	}

	return func(ts time.Time) bool {
		t := ts.UTC()
		for _, r := range ranges {
			if !t.Before(r.Start) && t.Before(r.End) {
				return true
			}
		}
		return false
	}, nil
}
