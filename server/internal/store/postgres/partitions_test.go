package postgres_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// wantEventsPartitionIndexes are the 6 per-partition indexes SPEC §2.2
// names for events, by suffix (the partition name prefixes each).
var wantEventsPartitionIndexSuffixes = []string{
	"_session_ts_seq_idx",
	"_kind_ts_idx",
	"_session_tool_use_idx",
	"_session_prompt_idx",
	"_ingested_at_idx",
	"_decision_source_idx",
}

var wantMetricSamplesPartitionIndexSuffixes = []string{
	"_name_ts_idx",
	"_series_hash_ts_idx",
}

// TestEnsurePartitions_CreatesMonthlyPartitionsAndIndexes is the P2-05 AC:
// after EnsurePartitions over a 3-month range, pg_indexes on each events
// partition shows exactly the 6 created indexes plus the inherited
// `*_ts_dedup_key_key` unique index (from the parent-level UNIQUE (ts,
// dedup_key) constraint, SPEC §2.2) — never a 7th created one, never the
// unique index missing. The partition's own `*_pkey` index (inherited from
// the parent PRIMARY KEY) is excluded from the comparison: it is not part
// of this ticket's index set, just an unavoidable side effect of the PK.
func TestEnsurePartitions_CreatesMonthlyPartitionsAndIndexes(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	from := time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.March, 3, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.EnsurePartitions(ctx, from, to))

	for _, month := range []string{"2026_01", "2026_02", "2026_03"} {
		t.Run(month, func(t *testing.T) {
			eventsPartition := "events_" + month
			got := indexNamesExcludingPKey(ctx, t, pool, eventsPartition)

			want := make(map[string]bool, len(wantEventsPartitionIndexSuffixes)+1)
			for _, suffix := range wantEventsPartitionIndexSuffixes {
				want[eventsPartition+suffix] = true
			}
			want[eventsPartition+"_ts_dedup_key_key"] = true
			require.ElementsMatch(t, mapKeys(want), got,
				"events partition %s must have exactly the 6 created indexes plus the inherited unique index", eventsPartition)

			metricsPartition := "metric_samples_" + month
			gotMetrics := indexNamesExcludingPKey(ctx, t, pool, metricsPartition)
			wantMetrics := make(map[string]bool, len(wantMetricSamplesPartitionIndexSuffixes))
			for _, suffix := range wantMetricSamplesPartitionIndexSuffixes {
				wantMetrics[metricsPartition+suffix] = true
			}
			require.ElementsMatch(t, mapKeys(wantMetrics), gotMetrics,
				"metric_samples partition %s must have exactly the 2 created indexes (no inherited unique: metric_samples' PK includes series_hash+dedup_key, not just ts)", metricsPartition)
		})
	}
}

// TestEnsurePartitions_IsIdempotent is the P2-05 AC: calling EnsurePartitions
// twice over the same range is a no-op (no error, no duplicate objects).
func TestEnsurePartitions_IsIdempotent(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	from := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.EnsurePartitions(ctx, from, to))
	require.NoError(t, store.EnsurePartitions(ctx, from, to))

	got := indexNamesExcludingPKey(ctx, t, pool, "events_2026_06")
	require.Len(t, got, len(wantEventsPartitionIndexSuffixes)+1, "a second EnsurePartitions call must not duplicate indexes")
}

// TestEvents_OnConflictAgainstParentSucceeds is the direct regression test
// for review blocker B1: the parent-level UNIQUE (ts, dedup_key) constraint
// (002_events.sql) must satisfy `ON CONFLICT (ts, dedup_key)` when inserted
// through the parent `events` relation. Before that constraint moved to the
// parent, this failed with "there is no unique or exclusion constraint
// matching the ON CONFLICT specification" — reproduced on PG 18.4 (see this
// ticket's report). It also proves the second insert of the same
// (ts, dedup_key) pair is silently absorbed, not duplicated.
func TestEvents_OnConflictAgainstParentSucceeds(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	now := time.Now().UTC()
	require.NoError(t, store.EnsurePartitions(ctx, now, now))

	insert := `INSERT INTO events (ts, session_id, vendor, source, kind, event_name, dedup_key)
		VALUES ($1, 'sess-1', 'claude-code', 'otel_log', 'llm.request', 'llm.request', $2)
		ON CONFLICT (ts, dedup_key) DO NOTHING`

	ts := now
	dedupKey := "otel:sess-1:0:llm.request:deadbeef"

	tag, err := pool.Exec(ctx, insert, ts, dedupKey)
	require.NoError(t, err, "first insert against the parent must succeed")
	require.EqualValues(t, 1, tag.RowsAffected())

	tag, err = pool.Exec(ctx, insert, ts, dedupKey)
	require.NoError(t, err, "ON CONFLICT (ts, dedup_key) must be satisfiable against the parent-level constraint (review blocker B1)")
	require.EqualValues(t, 0, tag.RowsAffected(), "the conflicting row must be a no-op, not a duplicate")

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE dedup_key = $1", dedupKey).Scan(&count))
	require.Equal(t, 1, count)
}

// TestEvents_InsertOutsideAllPartitions_ErrorsAndIsClassifiableTooOld is the
// P2-05 AC: with no DEFAULT partition (SPEC §2.2), an insert whose ts falls
// outside every partition created so far must error, and that error must be
// classifiable via postgres.IsTooOld (SPEC §1.7 rule 3).
func TestEvents_InsertOutsideAllPartitions_ErrorsAndIsClassifiableTooOld(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	now := time.Now().UTC()
	require.NoError(t, store.EnsurePartitions(ctx, now, now)) // only the current month exists

	insert := `INSERT INTO events (ts, session_id, vendor, source, kind, event_name, dedup_key)
		VALUES ($1, 'sess-1', 'claude-code', 'otel_log', 'llm.request', 'llm.request', 'k')
		ON CONFLICT (ts, dedup_key) DO NOTHING`

	tooOld := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := pool.Exec(ctx, insert, tooOld)
	require.Error(t, err, "an insert outside every partition must error (no DEFAULT partition)")
	require.True(t, postgres.IsTooOld(err), "the error must be classifiable as too_old, got: %v", err)
}

// TestIsTooOld_DoesNotMatchUnrelatedConstraintViolations guards IsTooOld's
// message-text check: Postgres overloads SQLSTATE 23514 (check_violation)
// for both "no partition found" and an ordinary CHECK constraint failure
// (verified empirically — see this ticket's report), so matching on
// SQLSTATE alone would misclassify the latter as too_old.
func TestIsTooOld_DoesNotMatchUnrelatedConstraintViolations(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `CREATE TABLE it_chk (a int CHECK (a > 0))`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO it_chk (a) VALUES (-1)`)
	require.Error(t, err)
	require.False(t, postgres.IsTooOld(err), "an ordinary CHECK constraint violation must not be classified too_old")

	require.False(t, postgres.IsTooOld(errors.New("some unrelated error")))
	require.False(t, postgres.IsTooOld(nil))
}

// TestUUIDv7_DefaultsWork is the P2-05 AC: events.id's DEFAULT uuidv7()
// (Postgres 18 built-in) actually populates rows, and this test fails with
// a clear, explicit message rather than an opaque SQL error if it is ever
// run against a pre-18 server.
func TestUUIDv7_DefaultsWork(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	var versionNumText string
	require.NoError(t, pool.QueryRow(ctx, "SHOW server_version_num").Scan(&versionNumText))
	versionNum, err := strconv.Atoi(versionNumText)
	require.NoError(t, err, "server_version_num must be numeric")
	require.GreaterOrEqualf(t, versionNum, 180000, "Argus requires Postgres 18+ for uuidv7(): server reports version_num=%d", versionNum)

	now := time.Now().UTC()
	require.NoError(t, store.EnsurePartitions(ctx, now, now))

	_, err = pool.Exec(ctx, `INSERT INTO events (ts, session_id, vendor, source, kind, event_name, dedup_key)
		VALUES ($1, 'sess-1', 'claude-code', 'otel_log', 'llm.request', 'llm.request', 'uuidv7-check')`, now)
	require.NoError(t, err)

	var idText string
	require.NoError(t, pool.QueryRow(ctx, "SELECT id::text FROM events WHERE dedup_key = 'uuidv7-check'").Scan(&idText))
	require.NotEmpty(t, idText, "events.id must default to a non-empty uuidv7() value")
	require.Equal(t, "7", string(idText[14]), "uuidv7() must set the UUID version nibble to 7")
}

// TestMigrationsCurrent_TrueAfterMigrate is the lead-decision AC: after the
// harness's implicit Migrate, MigrationsCurrent must report true.
func TestMigrationsCurrent_TrueAfterMigrate(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	current, err := store.MigrationsCurrent(ctx)
	require.NoError(t, err)
	require.True(t, current, "MigrationsCurrent must be true once every embedded migration has been applied")
}

// TestMigrationsCurrent_FalseWithPendingMigration constructs an honest
// "pending migration" case by rolling one migration back with goose's
// down-to (via MigrateStatus's own provider is unexported, so this drives
// goose's version table directly the same way `argusd migrate down-to`
// would): deleting the latest goose_db_version row makes that version
// "pending" again from MigrationsCurrent's point of view, exactly as if the
// binary had shipped a migration this schema hasn't run yet.
func TestMigrationsCurrent_FalseWithPendingMigration(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	before, err := store.MigrationsCurrent(ctx)
	require.NoError(t, err)
	require.True(t, before)

	var maxVersion int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version").Scan(&maxVersion))
	require.Positive(t, maxVersion, "goose_db_version must have at least one applied migration row")

	_, err = pool.Exec(ctx, "DELETE FROM goose_db_version WHERE version_id = $1", maxVersion)
	require.NoError(t, err)

	after, err := store.MigrationsCurrent(ctx)
	require.NoError(t, err)
	require.False(t, after, "MigrationsCurrent must be false once goose reports the latest migration as pending")
}

func indexNamesExcludingPKey(ctx context.Context, t *testing.T, pool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tableName string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = $1 AND indexname NOT LIKE '%\_pkey' ESCAPE '\'`,
		tableName)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		out = append(out, name)
	}
	require.NoError(t, rows.Err())
	return out
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
