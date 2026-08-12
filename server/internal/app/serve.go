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
//  3. Close/drain the ingest queue (internal/ingest.Pipeline.Close, SPEC
//     §3.6/§3.8, wired in by P2-09).
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
		// Migrations/Queue satisfy httpapi's narrow ports structurally
		// (postgres.Store.MigrationsCurrent, ingest.Pipeline.QueueSaturated)
		// without httpapi importing either package (SPEC §3.8's third
		// readiness condition, P2-09).
		Migrations: a.store,
		Queue:      a.ingest,
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

	// The partition-manager job (SPEC §2.4) watches the same ctx as the
	// server: it stops ticking as soon as ctx is cancelled and needs no
	// entry in the shutdown() sequence below — a tick in flight when the
	// pool closes just logs one error, never corrupts state, since
	// EnsurePartitions is idempotent.
	go a.partitions.Run(ctx)

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
// queue and waiting for it to fully drain, bounded by the same
// ARGUS_SHUTDOWN_GRACE deadline as the HTTP shutdown (ctx is shutdownCtx,
// shared with step (2)). A non-nil return means the deadline was hit before
// every buffered batch was flushed — SPEC §3.8: "exit 0 only if the drain
// completed; 1 if events were dropped" — which shutdown propagates as
// Serve's own error.
func (a *App) drainIngest(ctx context.Context) error {
	return a.ingest.Close(ctx)
}
