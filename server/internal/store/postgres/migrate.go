package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationLockKey is the fixed pg_advisory_lock key Migrate takes so that
// concurrent `serve --auto-migrate` or `migrate` invocations (SPEC §3.8)
// serialize instead of racing goose's version table. Arbitrary but stable;
// changing it would let an old and new binary migrate concurrently.
const migrationLockKey int64 = 0x41_52_47_55_53_30_31 // "ARGUS01" packed into an int64

// Migrate runs every pending db/migrations/*.sql migration to the latest
// version, guarded by a session-scoped Postgres advisory lock so two
// concurrent callers (e.g. two `serve` processes starting at once) don't
// race goose's own bookkeeping. It is idempotent: a second call with
// nothing pending is a no-op.
//
// Deviation from SPEC §3.2's "goose supports pgx v5 directly" note: goose
// v3.27.3's Provider always drives migrations through a database/sql
// *sql.DB (see database.Store's DBTxConn parameter and Provider.db field);
// there is no pgx-v5-native entry point. The only way to hand it a *sql.DB
// backed by pgx v5 without pulling in lib/pq is pgx/v5/stdlib, via
// stdlib.OpenDBFromPool, which wraps the existing pgxpool.Pool rather than
// opening a second connection pool. This is reported to the ticket owner as
// a deviation, not silently substituted.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: migrate: acquiring lock connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("postgres: migrate: acquiring advisory lock: %w", err)
	}
	defer func() {
		// Unlock on the same backend connection that took the lock
		// (advisory locks are session-scoped). Use a context that survives
		// the caller's cancellation so a timed-out Migrate still releases
		// the lock instead of stranding it for the connection's lifetime.
		unlockCtx := context.WithoutCancel(ctx)
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	provider, err := s.newGooseProvider()
	if err != nil {
		return err
	}
	defer provider.Close() //nolint:errcheck // Close() only fails to close an already-closed stdlib wrapper; nothing actionable here.

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("postgres: migrate: running migrations: %w", err)
	}
	return nil
}

// AppliedMigration is one row of `argusd migrate status` output.
type AppliedMigration struct {
	Version int64
	Applied bool
}

// MigrateStatus reports every known migration and whether it has been
// applied, for `argusd migrate status`. It does not take the advisory lock
// since it only reads goose's version table.
func (s *Store) MigrateStatus(ctx context.Context) ([]AppliedMigration, error) {
	provider, err := s.newGooseProvider()
	if err != nil {
		return nil, err
	}
	defer provider.Close() //nolint:errcheck // see Migrate.

	statuses, err := provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: migrate status: %w", err)
	}

	out := make([]AppliedMigration, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, AppliedMigration{
			Version: st.Source.Version,
			Applied: st.State == goose.StateApplied,
		})
	}
	return out, nil
}

// newGooseProvider wires goose to the embedded migrations over a
// database/sql handle backed by the existing pgxpool (see the deviation
// note on Migrate).
func (s *Store) newGooseProvider() (*goose.Provider, error) {
	sqlDB := stdlib.OpenDBFromPool(s.pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrationsFS)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("postgres: creating goose provider: %w", err)
	}
	return provider, nil
}
