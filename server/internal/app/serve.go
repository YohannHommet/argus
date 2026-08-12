package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/YohannHommet/argus/server/internal/httpapi"
)

// Serve builds the httpapi router, starts the HTTP listener, and blocks
// until ctx is cancelled (SIGINT/SIGTERM upstream — see cmd/argusd), then
// runs the SPEC §3.8 graceful-shutdown sequence in order:
//
//  1. /readyz starts failing (ReadyState flips false).
//  2. http.Server.Shutdown, bounded by ARGUS_SHUTDOWN_GRACE, so in-flight
//     requests finish.
//  3. Close/drain the ingest queue (the ordered seam for P2's pipeline;
//     nothing exists to drain in Phase 1).
//  4. pool.Close().
//
// It returns nil only if every step completed cleanly; a non-nil error
// (from either the HTTP server or the drain) should make the caller exit
// non-zero, per SPEC §3.8's "exit 0 only if the drain completed" rule.
func (a *App) Serve(ctx context.Context) error {
	handler := httpapi.New(httpapi.Deps{
		Config: a.cfg,
		Store:  a.store,
		Logger: a.logger,
		Ready:  a.ready,
	})

	a.server = &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// The server exited on its own (e.g. a bind failure) before ctx was
		// ever cancelled; nothing to shut down.
		return err
	case <-ctx.Done():
	}

	return a.shutdown() //nolint:contextcheck // ctx is already Done() here (that's why we're shutting down); shutdown deliberately derives its own bounded context from Background rather than a cancelled one
}

// shutdown runs steps (1)-(4) of the SPEC §3.8 sequence in order. Step (4)
// (closing the store pool) always runs, even if the HTTP shutdown or the
// ingest drain times out, so the pool is never leaked.
func (a *App) shutdown() error {
	a.ready.SetReady(false) // (1) /readyz starts failing

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer cancel()

	httpErr := a.server.Shutdown(shutdownCtx) // (2) bounded, waits for in-flight requests

	drainErr := a.drainIngest(shutdownCtx) // (3) ordered seam; no-op until P2's pipeline exists

	a.store.Close() // (4) always runs

	if httpErr != nil {
		return fmt.Errorf("app: shutdown: http server: %w", httpErr)
	}
	if drainErr != nil {
		return fmt.Errorf("app: shutdown: ingest drain: %w", drainErr)
	}
	return nil
}

// drainIngest is step (3) of the shutdown sequence: closing the ingest
// queue and waiting for it to fully drain. No queue exists until P2's
// internal/ingest pipeline lands; this seam exists now so that pipeline
// only has to fill in a body, not change Serve's ordering.
func (a *App) drainIngest(_ context.Context) error {
	return nil
}
