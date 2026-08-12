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

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
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

	server *http.Server // set by Serve
}

// New connects the database pool, wraps it in the postgres Store, and runs
// migrations when ARGUS_AUTO_MIGRATE is true (SPEC §3.7, §3.8) — all before
// the HTTP listener exists, so a `serve` that fails to migrate never starts
// accepting traffic. It does not start serving; call Serve for that.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return nil, fmt.Errorf("app: connecting to database: %w", err)
	}

	st := postgres.New(pool)

	if cfg.AutoMigrate {
		if err := st.Migrate(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("app: running migrations: %w", err)
		}
	}

	return &App{
		cfg:    cfg,
		logger: logger,
		store:  st,
		ready:  httpapi.NewReadyState(),
	}, nil
}
