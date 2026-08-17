package postgres_test

import (
	"context"
	"net/url"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// --- M6/M8 (pre-Phase-4 audit wave, ticket W3): NewPool must pin every
// connection's TimeZone to UTC and set a lock_timeout, regardless of what
// the database URL itself requests. -----------------------------------

// TestNewPool_PinsUTCTimeZoneAndLockTimeoutRegardlessOfDSN is the pool-level
// unit test for both fixes: it opens a pool via the real postgres.NewPool
// against a DSN that explicitly asks for a different TimeZone and a
// disabled lock_timeout, then asserts the live session GUCs show NewPool's
// values, not the DSN's. rollups_test.go's
// TestRunRollups_NonUTCSessionTimeZone_{Kolkata,Paris} additionally prove
// this pin is what makes the real rollup passes correct end-to-end; this
// test isolates the pool-construction behaviour on its own.
func TestNewPool_PinsUTCTimeZoneAndLockTimeoutRegardlessOfDSN(t *testing.T) {
	ctx := context.Background()
	dsn := storetesting.NewDSN(t) + "&TimeZone=" + url.QueryEscape("America/New_York") + "&lock_timeout=0"

	pool, err := postgres.NewPool(ctx, dsn, 5)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var tz, lockTimeout string
	require.NoError(t, pool.QueryRow(ctx, `SHOW TimeZone`).Scan(&tz))
	require.NoError(t, pool.QueryRow(ctx, `SHOW lock_timeout`).Scan(&lockTimeout))

	require.Equal(t, "UTC", tz, "NewPool must pin TimeZone to UTC even when the DSN requests a different zone")
	require.Equal(t, "15s", lockTimeout, "NewPool must set lock_timeout even when the DSN explicitly disables it")
}

// wantSessionsIndexes are the eight indexes SPEC §2.1 names on sessions.
var wantSessionsIndexes = []string{
	"sessions_last_event_idx",
	"sessions_started_idx",
	"sessions_cost_idx",
	"sessions_events_idx",
	"sessions_project_idx",
	"sessions_status_idx",
	"sessions_vendor_idx",
	"sessions_sweep_idx",
}

func TestMigrate_CreatesCoreTables(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()

	tables := queryStrings(ctx, t, pool, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name IN ('sessions','turns','ingest_dedup')
		ORDER BY table_name`)
	require.Equal(t, []string{"ingest_dedup", "sessions", "turns"}, tables, "sessions/turns/ingest_dedup must all exist")

	sessionCols := queryStrings(ctx, t, pool, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'sessions'
		ORDER BY column_name`)
	for _, want := range []string{
		"id", "vendor", "project", "cwd", "status", "start_type", "end_reason",
		"permission_mode", "app_version", "entrypoint", "terminal_type",
		"user_email", "user_account_uuid", "organization_id", "started_at",
		"ended_at", "first_seen_at", "last_event_at", "event_count", "turn_count",
		"tool_call_count", "tool_reject_count", "subagent_count", "error_count",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"cost_usd", "cost_estimated_usd", "cost_by_query_source", "models",
		"field_ranks", "updated_at",
	} {
		require.Contains(t, sessionCols, want, "sessions.%s must exist", want)
	}

	turnCols := queryStrings(ctx, t, pool, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'turns'
		ORDER BY column_name`)
	for _, want := range []string{
		"session_id", "prompt_id", "turn_index", "started_at", "ended_at",
		"first_seen_at", "last_event_at", "duration_ms", "status",
		"api_request_count", "tool_call_count", "tool_reject_count", "error_count",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"cost_usd", "cost_estimated_usd", "models", "field_ranks",
	} {
		require.Contains(t, turnCols, want, "turns.%s must exist", want)
	}
	require.NotContains(t, turnCols, "cost_source", "turns must NOT have a cost_source column")

	dedupCols := queryStrings(ctx, t, pool, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'ingest_dedup'
		ORDER BY column_name`)
	require.ElementsMatch(t, []string{"dedup_key", "first_seen_at"}, dedupCols)
}

func TestMigrate_CreatesSessionsIndexes(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()

	got := queryStrings(ctx, t, pool, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'sessions'`)

	for _, want := range wantSessionsIndexes {
		require.Contains(t, got, want, "index %s must exist on sessions", want)
	}

	turnIdx := queryStrings(ctx, t, pool, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'turns'`)
	require.Contains(t, turnIdx, "turns_session_started_idx")

	dedupIdx := queryStrings(ctx, t, pool, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'ingest_dedup'`)
	require.Contains(t, dedupIdx, "ingest_dedup_age_idx")
}

// TestMigrate_NoVendorVocabularyCheckConstraints is the SPEC §0 acceptance
// criterion: no CHECK constraint exists on sessions or turns at all, since
// every text column on both tables is either Argus's own closed taxonomy
// (there is none yet in 001_core.sql) or vendor-supplied and must stay
// unconstrained.
func TestMigrate_NoVendorVocabularyCheckConstraints(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT con.conname
		FROM pg_constraint con
		JOIN pg_class rel ON rel.oid = con.conrelid
		JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
		WHERE nsp.nspname = current_schema()
		  AND rel.relname IN ('sessions', 'turns')
		  AND con.contype = 'c'`)
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.Empty(t, names, "sessions/turns must have zero CHECK constraints (SPEC §0)")
}

func TestMigrate_IsIdempotent(t *testing.T) {
	pool := storetesting.NewPool(t) // already ran Migrate once via the harness
	ctx := context.Background()

	store := postgres.New(pool)
	require.NoError(t, store.Migrate(ctx))
	require.NoError(t, store.Migrate(ctx))
}

// TestMigrate_ConcurrentCallsBothSucceed exercises the pg_advisory_lock
// guard: two goroutines calling Migrate at the same time against the same
// schema must both return without error, whether they race for the lock or
// not, and goose's version table must end up consistent.
func TestMigrate_ConcurrentCallsBothSucceed(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.Migrate(ctx)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "concurrent Migrate call %d must succeed", i)
	}

	statuses, err := store.MigrateStatus(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, statuses)
	for _, s := range statuses {
		require.True(t, s.Applied, "migration %d must be applied after concurrent Migrate calls", s.Version)
	}
}

func TestMigrateStatus_ReportsAppliedVersions(t *testing.T) {
	pool := storetesting.NewPool(t)
	ctx := context.Background()
	store := postgres.New(pool)

	statuses, err := store.MigrateStatus(ctx)
	require.NoError(t, err)
	require.Len(t, statuses, 4, "001_core.sql, 002_events.sql, 003_projections.sql, 004_rollups.sql are the only migrations so far")
	for i, want := range []int64{1, 2, 3, 4} {
		require.Equal(t, want, statuses[i].Version)
		require.True(t, statuses[i].Applied)
	}
}

// TestHarness_ParallelTestsGetNonCollidingSchemas is the SPEC §8.4
// acceptance criterion: two tests (here, simulated via two harness pools
// requested in the same test body, which is exactly what two parallel
// t.Run subtests would do) never see each other's tables.
func TestHarness_ParallelTestsGetNonCollidingSchemas(t *testing.T) {
	var pools [2]*pgxpool.Pool
	var wg sync.WaitGroup
	for i := range pools {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pools[i] = storetesting.NewPool(t)
		}(i)
	}
	wg.Wait()

	ctx := context.Background()
	var schemas [2]string
	for i, p := range pools {
		require.NoError(t, p.QueryRow(ctx, "SELECT current_schema()").Scan(&schemas[i]))
		// Each pool must see exactly one sessions table: its own.
		var count int
		require.NoError(t, p.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = 'sessions'`).Scan(&count))
		require.Equal(t, 1, count)
	}
	require.NotEqual(t, schemas[0], schemas[1], "two harness pools must land in different schemas")
}

func queryStrings(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}
