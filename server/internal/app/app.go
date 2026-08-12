// Package app wires config → store → httpapi (docs/SPEC.md §3.1) and owns
// the process lifecycle: constructing the storage layer, running migrations
// when configured, and running the HTTP server through the graceful
// shutdown sequence in SPEC §3.8. It is the only package allowed to know
// about every other layer at once; httpapi, store, and config each stay
// ignorant of it.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// App is a constructed, ready-to-serve Argus process: a live database pool
// wrapped in the postgres Store, and the readiness flag Serve's shutdown
// sequence flips.
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	store  *postgres.Store
	ready  *httpapi.ReadyState

	partitions *PartitionJob    // started by Serve
	ingest     *ingest.Pipeline // drained by Serve's shutdown sequence (drainIngest)

	server *http.Server // set by Serve
}

// New connects the database pool, wraps it in the postgres Store, and runs
// migrations when ARGUS_AUTO_MIGRATE is true (SPEC §3.7, §3.8) — all before
// the HTTP listener exists, so a `serve` that fails to migrate never starts
// accepting traffic. It then ensures the current-through-two-months-ahead
// partitions exist (SPEC §2.4's "startup fails loudly if the current
// month's partition cannot be created") for the same reason: an ingest
// request landing before the first hourly PartitionJob tick must never hit
// a missing partition. It does not start serving; call Serve for that.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return nil, fmt.Errorf("app: connecting to database: %w", err)
	}

	// WithRollupSessionRemarkMax threads ARGUS_ROLLUP_SESSION_REMARK_MAX
	// (SPEC §2.4, §3.7) into the store without postgres importing
	// internal/config (depguard, SPEC §3.1) — see pool.go's Option doc.
	st := postgres.New(pool, postgres.WithRollupSessionRemarkMax(cfg.RollupSessionRemarkMax))

	if cfg.AutoMigrate {
		if err := st.Migrate(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("app: running migrations: %w", err)
		}
	}

	now := time.Now()
	if err := st.EnsurePartitions(ctx, now, now.Add(partitionJobHorizon)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("app: ensuring startup partitions: %w", err)
	}

	ing := ingest.New(st, ingest.PipelineConfig{
		QueueCap:       cfg.IngestQueue,
		Workers:        cfg.IngestWorkers,
		BatchSize:      cfg.IngestBatchSize,
		FlushInterval:  cfg.IngestFlush,
		RetryConflict:  cfg.IngestRetryConflict,
		RetryTransient: cfg.IngestRetryTransient,
	}, ingest.WithLogger(logger))

	return &App{
		cfg:        cfg,
		logger:     logger,
		store:      st,
		ready:      httpapi.NewReadyState(),
		partitions: NewPartitionJob(st, logger),
		ingest:     ing,
	}, nil
}
