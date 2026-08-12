// Package postgres is the production implementation of internal/store's
// Store interface, backed by pgx v5's pgxpool (SPEC §3.1, §3.2). Only
// Health, Close, and Migrate have real bodies in P1-04; every other method
// returns store.ErrNotImplemented so later tickets can fill them in without
// touching the store.Store interface.
package postgres

import (
	"context"
	"fmt"
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

func (s *Store) WriteBatch(ctx context.Context, b []model.Event) (store.BatchResult, error) {
	return store.BatchResult{}, store.ErrNotImplemented
}

// --- Reader -------------------------------------------------------------

func (s *Store) ListSessions(ctx context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error) {
	return nil, "", store.ErrNotImplemented
}

func (s *Store) GetSession(ctx context.Context, id string) (*model.SessionDetail, error) {
	return nil, store.ErrNotImplemented
}

func (s *Store) ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error) {
	return nil, store.ErrNotImplemented
}

func (s *Store) ListEvents(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error) {
	return nil, "", store.ErrNotImplemented
}

func (s *Store) GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error) {
	return nil, store.ErrNotImplemented
}

func (s *Store) ListToolCalls(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error) {
	return nil, "", store.ErrNotImplemented
}

func (s *Store) SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error) {
	return model.SubagentTree{}, store.ErrNotImplemented
}

func (s *Store) AnalyticsSummary(ctx context.Context, f store.AnalyticsFilter) (model.Summary, error) {
	return model.Summary{}, store.ErrNotImplemented
}

func (s *Store) AnalyticsSeries(ctx context.Context, f store.AnalyticsFilter, g store.Grouping) (model.Series, error) {
	return model.Series{}, store.ErrNotImplemented
}

func (s *Store) AnalyticsBreakdown(ctx context.Context, f store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error) {
	return model.Breakdown{}, store.ErrNotImplemented
}

func (s *Store) AnalyticsDecisions(ctx context.Context, f store.AnalyticsFilter) (model.DecisionMatrix, error) {
	return model.DecisionMatrix{}, store.ErrNotImplemented
}

func (s *Store) EventsSince(ctx context.Context, after model.EventRef, windowStart time.Time, limit int) ([]model.Event, error) {
	return nil, store.ErrNotImplemented
}

func (s *Store) Facets(ctx context.Context) (model.Facets, error) {
	return model.Facets{}, store.ErrNotImplemented
}

func (s *Store) DataQuality(ctx context.Context) (model.DataQuality, error) {
	return model.DataQuality{}, store.ErrNotImplemented
}

func (s *Store) UnknownKinds(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error) {
	return nil, store.ErrNotImplemented
}

func (s *Store) HookLatency(ctx context.Context, f store.AnalyticsFilter) (model.HookLatency, error) {
	return model.HookLatency{}, store.ErrNotImplemented
}

// --- Maintenance (Migrate excepted; see migrate.go) ----------------------

func (s *Store) EnsurePartitions(ctx context.Context, from, to time.Time) error {
	return store.ErrNotImplemented
}

func (s *Store) RunRollups(ctx context.Context, max int) (store.RollupStats, error) {
	return store.RollupStats{}, store.ErrNotImplemented
}

func (s *Store) SweepAbandoned(ctx context.Context, idle time.Duration) (int64, error) {
	return 0, store.ErrNotImplemented
}

func (s *Store) ApplyRetention(ctx context.Context, cutoff time.Time, dryRun bool) ([]string, error) {
	return nil, store.ErrNotImplemented
}

func (s *Store) PruneDedup(ctx context.Context, cutoff time.Time) (int64, error) {
	return 0, store.ErrNotImplemented
}

func (s *Store) RebuildProjections(ctx context.Context, fromTS time.Time) error {
	return store.ErrNotImplemented
}

// compile-time assertion that Store satisfies store.Store.
var _ store.Store = (*Store)(nil)
