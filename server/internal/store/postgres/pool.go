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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// applicationName identifies argusd's connections in pg_stat_activity.
const applicationName = "argusd"

// Store is the postgres-backed store.Store implementation.
type Store struct {
	pool *pgxpool.Pool
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
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
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

// --- Writer -----------------------------------------------------------

// WriteBatch implements store.Writer; not yet implemented (P1-04 stub).
func (s *Store) WriteBatch(_ context.Context, _ []model.Event) (store.BatchResult, error) {
	return store.BatchResult{}, store.ErrNotImplemented
}

// --- Reader -------------------------------------------------------------

// ListSessions implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) ListSessions(_ context.Context, _ store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
	return nil, "", store.ErrNotImplemented
}

// GetSession implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) GetSession(_ context.Context, _ string) (*model.SessionDetail, error) {
	return nil, store.ErrNotImplemented
}

// ListTurns implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) ListTurns(_ context.Context, _ string) ([]model.Turn, error) {
	return nil, store.ErrNotImplemented
}

// ListEvents implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) ListEvents(_ context.Context, _ store.EventFilter, _ store.Page) ([]model.Event, store.Cursor, error) {
	return nil, "", store.ErrNotImplemented
}

// GetEvent implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) GetEvent(_ context.Context, _ model.EventRef) (*model.Event, error) {
	return nil, store.ErrNotImplemented
}

// ListToolCalls implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) ListToolCalls(_ context.Context, _ store.ToolCallFilter, _ store.Page) ([]model.ToolCall, store.Cursor, error) {
	return nil, "", store.ErrNotImplemented
}

// SubagentTree implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) SubagentTree(_ context.Context, _ string) (model.SubagentTree, error) {
	return model.SubagentTree{}, store.ErrNotImplemented
}

// AnalyticsSummary implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) AnalyticsSummary(_ context.Context, _ store.AnalyticsFilter) (model.Summary, error) {
	return model.Summary{}, store.ErrNotImplemented
}

// AnalyticsSeries implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) AnalyticsSeries(_ context.Context, _ store.AnalyticsFilter, _ store.Grouping) (model.Series, error) {
	return model.Series{}, store.ErrNotImplemented
}

// AnalyticsBreakdown implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) AnalyticsBreakdown(_ context.Context, _ store.AnalyticsFilter, _ store.Dimension) (model.Breakdown, error) {
	return model.Breakdown{}, store.ErrNotImplemented
}

// AnalyticsDecisions implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) AnalyticsDecisions(_ context.Context, _ store.AnalyticsFilter) (model.DecisionMatrix, error) {
	return model.DecisionMatrix{}, store.ErrNotImplemented
}

// EventsSince implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) EventsSince(_ context.Context, _ model.EventRef, _ time.Time, _ int) ([]model.Event, error) {
	return nil, store.ErrNotImplemented
}

// Facets implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) Facets(_ context.Context) (model.Facets, error) {
	return model.Facets{}, store.ErrNotImplemented
}

// DataQuality implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) DataQuality(_ context.Context) (model.DataQuality, error) {
	return model.DataQuality{}, store.ErrNotImplemented
}

// UnknownKinds implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) UnknownKinds(_ context.Context, _ time.Time, _ int) ([]model.UnknownKindGroup, error) {
	return nil, store.ErrNotImplemented
}

// HookLatency implements store.Reader; not yet implemented (P1-04 stub).
func (s *Store) HookLatency(_ context.Context, _ store.AnalyticsFilter) (model.HookLatency, error) {
	return model.HookLatency{}, store.ErrNotImplemented
}

// --- Maintenance (Migrate excepted; see migrate.go) ----------------------

// EnsurePartitions implements store.Maintenance; not yet implemented (P1-04 stub).
func (s *Store) EnsurePartitions(_ context.Context, _, _ time.Time) error {
	return store.ErrNotImplemented
}

// RunRollups implements store.Maintenance; not yet implemented (P1-04 stub).
func (s *Store) RunRollups(_ context.Context, _ int) (store.RollupStats, error) {
	return store.RollupStats{}, store.ErrNotImplemented
}

// SweepAbandoned implements store.Maintenance; not yet implemented (P1-04 stub).
func (s *Store) SweepAbandoned(_ context.Context, _ time.Duration) (int64, error) {
	return 0, store.ErrNotImplemented
}

// ApplyRetention implements store.Maintenance; not yet implemented (P1-04 stub).
func (s *Store) ApplyRetention(_ context.Context, _ time.Time, _ bool) ([]string, error) {
	return nil, store.ErrNotImplemented
}

// PruneDedup implements store.Maintenance; not yet implemented (P1-04 stub).
func (s *Store) PruneDedup(_ context.Context, _ time.Time) (int64, error) {
	return 0, store.ErrNotImplemented
}

// RebuildProjections implements store.Maintenance; not yet implemented (P1-04 stub).
func (s *Store) RebuildProjections(_ context.Context, _ time.Time) error {
	return store.ErrNotImplemented
}

// compile-time assertion that Store satisfies store.Store.
var _ store.Store = (*Store)(nil)
