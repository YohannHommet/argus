package app

import (
	"context"
	"errors"
	"fmt"
	"net"
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
//     §3.6/§3.8, wired in by P2-09), bounded by its OWN separate
//     ARGUS_SHUTDOWN_GRACE budget rather than one shared with step 2 (M4
//     fix, see shutdown's doc comment) — so a slow in-flight request cannot
//     starve the drain and cause healthy, queued batches to be dropped.
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

		// Reader/Analytics wire the Phase-3 read API: P3-07's
		// session/timeline/event/tool-call handlers and P3-08's analytics,
		// facets, meta and quality handlers. postgres.Store satisfies both
		// narrow ports structurally, the same convention Migrations follows.
		//
		// These two lines are load-bearing in a way nothing else here is:
		// router.go mounts each group only when these are non-nil, a nil-safe
		// default that allowed the entire read API to be silently absent from
		// the running server while every handler test passed — those tests and
		// the conformance harness call httpapi.New directly and never come
		// through Serve. `docker compose up` followed by a curl to
		// /api/v1/sessions returned 404 until these were added, which is what
		// TestServe_ReadAPIRoutesAreMounted now pins.
		Reader:    a.store,
		Analytics: a.store,

		// HookMounter wires P2-11's POST /ingest/hook onto the mount seam
		// router.go already exposes; router.go itself is never touched.
		HookMounter: a.hooks,

		// OTLPMounter wires P2-10's POST /v1/{logs,metrics,traces} onto the
		// other mount seam router.go already exposes.
		OTLPMounter: a.otlp,
	})

	// The listener is bound synchronously, before Addr()/Listening() has any
	// meaning to a caller, and before the accept loop's own goroutine
	// starts — net.Listen is fast and this keeps "Serve has bound its
	// listener" an unambiguous, race-free signal rather than something a
	// caller has to poll for. Binding this way (rather than
	// http.Server.ListenAndServe, which opens its own listener internally
	// with no way to read back the resolved address) is what lets a caller
	// pass HTTPAddr="127.0.0.1:0" and discover the OS-assigned port via
	// Addr() instead of guessing a free one ahead of time and racing this
	// bind (P2-13's end-to-end test needs exactly that: an ephemeral port,
	// never a hardcoded one).
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", a.cfg.HTTPAddr)
	if err != nil {
		close(a.addrReady)
		return fmt.Errorf("app: listen on %s: %w", a.cfg.HTTPAddr, err)
	}
	a.listenAddr = ln.Addr().String()
	close(a.addrReady)

	a.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout/WriteTimeout bound the whole request/response
		// lifecycle, not just the headers: chi's mw.Timeout (router.go, 30s)
		// only cancels the request's context, which io.ReadAll(r.Body) in
		// the hooks/OTLP handlers never observes, so a client that finishes
		// headers and then trickles its body would otherwise hold the
		// connection (and a goroutine) open indefinitely — on endpoints
		// unauthenticated by default (ARGUS_INGEST_TOKEN's empty default).
		// 30s matches chi's own request-context timeout so neither layer is
		// the effectively-looser one.
		//
		// NOTE for whoever adds the Phase 5 SSE endpoint
		// (/api/v1/sessions/{id}/stream): a fixed WriteTimeout is
		// incompatible with a connection that must legitimately stay open
		// far longer than 30s. Do not raise this global value for that —
		// use http.ResponseController.SetWriteDeadline (or reset it
		// per-write) on the SSE handler alone once it exists; every other
		// handler should keep the bound this comment describes.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := a.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

	// The rollup job (SPEC §2.4, P3-05) follows the exact same shutdown
	// story as the partition job: it watches ctx, needs no shutdown() step,
	// and a tick in flight when the pool closes just logs one error — its
	// single transaction rolls back cleanly, leaving rollup_dirty intact
	// for the next process's first tick (rollups.go's whole-transaction
	// design).
	go a.rollups.Run(ctx)

	// The retention job (SPEC §2.4, P3-10) follows the same shutdown story
	// as the partition/rollup jobs: it watches ctx and needs no shutdown()
	// step. Unlike them it does not tick immediately (see RetentionJob.Run's
	// doc), so a tick in flight when ctx is cancelled is rarer, but the same
	// reasoning applies: ApplyRetention/PruneDedup are safe to interrupt
	// (one drops a partition per statement inside its own transaction;
	// PruneDedup deletes in independent bounded batches), so an in-flight
	// tick at shutdown just logs one error, never corrupts state.
	go a.retention.Run(ctx)

	// The sweep job (SPEC §2.4/§3.8, "abandoned-session sweep") follows the
	// same shutdown story as the partition/rollup/retention jobs: it watches
	// ctx and needs no shutdown() step, and a tick in flight when the pool
	// closes just logs one error — SweepAbandoned's single UPDATE either
	// commits or doesn't, never leaving a session half-migrated.
	go a.sweep.Run(ctx)

	var serveErrOrNil error
	select {
	case serveErrOrNil = <-serveErr:
		// The server exited on its own (e.g. a bind failure or an accept-loop
		// error) before ctx was ever cancelled. This still must run the full
		// shutdown() sequence below (m6 fix): before this fix, returning
		// serveErrOrNil directly here skipped shutdown() entirely, so
		// ReadyState was never flipped false, the ingest pipeline was never
		// closed/drained (workers and any queued batches simply abandoned,
		// with no drop accounting), and store.Close() never ran — every one
		// of which shutdown() exists specifically to do.
	case <-ctx.Done():
	}

	//nolint:contextcheck // ctx is already Done() (or was never used) here; shutdown deliberately derives its own bounded context(s) from Background rather than a cancelled one
	return errors.Join(serveErrOrNil, a.shutdown())
}

// shutdown runs steps (1)-(4) of the SPEC §3.8 sequence in order. Step (4)
// (closing the store pool) always runs, even if the HTTP shutdown or the
// ingest drain times out, so the pool is never leaked.
func (a *App) shutdown() error {
	a.ready.SetReady(false) // (1) /readyz starts failing

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer cancel()

	httpErr := a.server.Shutdown(shutdownCtx) // (2) bounded, waits for in-flight requests

	// (3) drainCtx gets its OWN full ARGUS_SHUTDOWN_GRACE budget, deliberately
	// not shutdownCtx (M4 fix). Before this fix, a single shared context
	// meant the two steps split one grace between them: if in-flight
	// requests consumed most of it, drainIngest's ctx.Done() branch would
	// fire almost immediately, Pipeline.Close would cancel its workers, and
	// every queued/buffered batch would be dropped — with Postgres perfectly
	// healthy — for no reason but an unlucky timing split with step (2).
	// Each step is independently allowed to take up to the full grace; the
	// two are not summed against a single caller-facing deadline because
	// nothing upstream of Serve enforces one (SPEC §3.8 names ARGUS_SHUTDOWN_
	// GRACE as "the graceful-shutdown budget" for the sequence's bounded
	// steps individually, not a wall-clock cap on the whole process exit).
	drainCtx, drainCancel := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer drainCancel()

	drainErr := a.drainIngest(drainCtx) // (3) ordered seam; no-op until P2's pipeline exists

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
// queue and waiting for it to fully drain, bounded by ctx — its own
// ARGUS_SHUTDOWN_GRACE-length deadline (drainCtx in shutdown, independent of
// step (2)'s shutdownCtx; see shutdown's doc comment, M4 fix). A non-nil
// return means the deadline was hit before every buffered batch was flushed
// — SPEC §3.8: "exit 0 only if the drain completed; 1 if events were
// dropped" — which shutdown propagates as Serve's own error.
func (a *App) drainIngest(ctx context.Context) error {
	return a.ingest.Close(ctx)
}
