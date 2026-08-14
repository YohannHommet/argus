// Package postgres is the production implementation of internal/store's
// Store interface, backed by pgx v5's pgxpool (SPEC §3.1, §3.2). Only
// Health, Close, and Migrate have real bodies in P1-04; every other method
// returns store.ErrNotImplemented so later tickets can fill them in without
// touching the store.Store interface.
package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// applicationName identifies argusd's connections in pg_stat_activity.
const applicationName = "argusd"

// defaultRollupSessionRemarkMax is SPEC §3.7's ARGUS_ROLLUP_SESSION_REMARK_MAX
// default (720 = 30 days of hourly buckets): the cap on how many rollup_dirty
// hour buckets one session's project/cwd change can re-mark (§2.4).
const defaultRollupSessionRemarkMax = 720

// Store is the postgres-backed store.Store implementation.
type Store struct {
	pool *pgxpool.Pool

	// rollupSessionRemarkMax caps the dirty-bucket re-mark WriteBatch performs
	// when a session's project/cwd changes (SPEC §2.4). It arrives through an
	// Option rather than internal/config directly: internal/store must not
	// import internal/config (depguard, SPEC §3.1), so app.New reads
	// cfg.RollupSessionRemarkMax and passes it in via WithRollupSessionRemarkMax.
	rollupSessionRemarkMax int
}

// Option configures a Store at construction time. The functional-options
// shape (rather than adding New parameters) is what lets New keep its
// existing single-argument call sites (internal/app, internal/store/testing,
// pool_test.go) compiling unchanged while P2-06 threads
// ARGUS_ROLLUP_SESSION_REMARK_MAX through without postgres importing
// internal/config.
type Option func(*Store)

// WithRollupSessionRemarkMax overrides the default cap (720) on how many
// rollup_dirty buckets a single session's project/cwd change may re-mark
// (SPEC §2.4). n <= 0 is ignored (keeps the default) rather than disabling
// the cap, since an unbounded re-mark is exactly what SPEC §2.4 warns
// against.
func WithRollupSessionRemarkMax(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.rollupSessionRemarkMax = n
		}
	}
}

// NewPool builds a pgxpool.Pool from a database URL, applying the pool
// sizing SPEC §3.2/§3.7 call for: a bounded max-connection count, an
// application_name so connections are identifiable in pg_stat_activity, and
// pgx's default prepared-statement cache (QueryExecModeCacheStatement) made
// explicit rather than left implicit.
func NewPool(ctx context.Context, databaseURL string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parsing database URL: %w", err)
	}

	if maxConns > 0 {
		if maxConns > math.MaxInt32 {
			return nil, fmt.Errorf("postgres: max conns %d overflows int32", maxConns)
		}
		cfg.MaxConns = int32(maxConns)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = applicationName

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: creating pool: %w", err)
	}
	return pool, nil
}

// New wraps an already-constructed pool in a Store. Callers that only need
// NewPool + Migrate (e.g. the `migrate` subcommand) can skip this.
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	s := &Store{pool: pool, rollupSessionRemarkMax: defaultRollupSessionRemarkMax}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Health pings the pool. Used by GET /readyz (SPEC §3.8).
func (s *Store) Health(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: health check: %w", err)
	}
	return nil
}

// Close releases the pool. Safe to call once during shutdown.
func (s *Store) Close() {
	s.pool.Close()
}

// --- Writer: WriteBatch and WriteMetrics live in write.go (P2-06). --------

// --- Reader -------------------------------------------------------------

// ListSessions, GetSession, ListTurns implement store.Reader; implemented in
// read_sessions.go (P3-02).

// ListEvents, GetEvent implement store.Reader; implemented in read_events.go
// (P3-03).

// ListToolCalls implements store.Reader; implemented in read_toolcalls.go
// (P3-03).

// SubagentTree implements store.Reader; implemented in subagent_tree.go
// (P2-08).

// AnalyticsSummary, AnalyticsSeries, AnalyticsBreakdown, AnalyticsDecisions
// implement store.Reader; implemented in read_analytics.go (P3-06).

// EventsSince implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) EventsSince(_ context.Context, _ model.EventRef, _ time.Time, _ int) ([]model.Event, error) {
	return nil, store.ErrNotImplemented
}

// Facets, DataQuality, UnknownKinds, HookLatency implement store.Reader;
// implemented in read_quality.go (P3-08).

// --- Maintenance (Migrate, EnsurePartitions, MigrationsCurrent excepted;
// see migrate.go and partitions.go) ---------------------------------------

// RunRollups implements store.Maintenance; implemented in rollups.go (P3-05).

// SweepAbandoned implements store.Maintenance (SPEC §1.7, §1.9, §2.4): moves
// every session whose status is active|unknown, has no ended_at, and has
// been idle longer than idle to abandoned. Implemented in P2-06 (the ticket
// that makes status a real stored column) rather than left for a later
// ticket — SPEC assigns it no separate owner and P2-06's AC (e) needs it
// working. A later session.end still moves the row back to ended (handled
// by the ordinary WriteBatch session upsert, not here).
func (s *Store) SweepAbandoned(ctx context.Context, idle time.Duration) (int64, error) {
	cutoff := time.Now().Add(-idle)
	n, err := gen.New(s.pool).SweepAbandoned(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("postgres: sweep abandoned: %w", err)
	}
	return n, nil
}

// ApplyRetention, PruneDedup implement store.Maintenance; ApplyRetentionPrecise
// is a Store-only extra (not part of the interface). All three, plus
// RebuildProjections below, are implemented in retention.go/rebuild.go (P3-10).

// RebuildProjections implements store.Maintenance; implemented in
// rebuild.go (P3-10).

// compile-time assertion that Store satisfies store.Store.
var _ store.Store = (*Store)(nil)
