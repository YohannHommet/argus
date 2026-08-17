//go:build e2e

// Package app's shutdown-grace tests (M4/m6 fixes) exercise the real
// Serve/shutdown sequence against a real Postgres and a real HTTP
// connection, the same way e2e_ingest_test.go and sweep_test.go do, because
// both bugs are about exactly how much wall-clock budget two lifecycle
// steps get relative to each other — not observable from a unit test that
// calls shutdown()'s pieces directly with already-expired contexts.
package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// blockingBody signals onStart the moment its first Read call is made (a
// reliable proxy for "the client has begun sending the request, and the
// server is now blocked inside io.ReadAll(r.Body) waiting for it"), sleeps
// delay in that same first call, then hands back the whole payload at once.
// A single sleep — rather than many small paced chunks — avoids compounding
// per-call scheduling jitter under a loaded test machine into the total
// held-open duration, and the onStart signal lets the caller synchronize on
// an event instead of guessing a fixed real-time delay for "the request
// must be in flight by now". This is what lets a test hold a real in-flight
// HTTP request open past ARGUS_SHUTDOWN_GRACE deterministically, without
// touching router.go or any handler.
type blockingBody struct {
	payload []byte
	delay   time.Duration
	onStart chan struct{}
	once    sync.Once
	sent    bool
}

func (r *blockingBody) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.onStart)
		time.Sleep(r.delay)
	})
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(p, r.payload), nil
}

// debugLogOutput is io.Discard, unless ARGUS_SHUTDOWN_TEST_DEBUG_LOG is set
// (a debugging escape hatch for this file only), in which case it is
// os.Stderr.
func debugLogOutput() io.Writer {
	if os.Getenv("ARGUS_SHUTDOWN_TEST_DEBUG_LOG") != "" {
		return os.Stderr
	}
	return io.Discard
}

// serveResult is a Serve() outcome that is safe to observe from more than
// one place (the test itself, and t.Cleanup): unlike a plain channel, Err()
// can be called repeatedly and from multiple goroutines without the second
// caller blocking forever on an already-drained channel.
type serveResult struct {
	err  error
	done chan struct{}
}

// Err blocks until Serve has returned, then returns its error every time
// it's called (the value was written before done was closed, so every
// caller that only reads after <-done observes it safely).
func (r *serveResult) Err() error {
	<-r.done
	return r.err
}

// newShutdownTestApp is newE2EApp's shutdown-test-specific twin: it needs
// to control exactly when Serve's ctx is cancelled (relative to an
// in-flight slow request this file drives) and to observe Serve's own
// return value directly (m6's fix is only visible in what Serve returns
// and in side effects observable after it returns), neither of which the
// shared newE2EApp/e2e_ingest_test.go helper exposes — that helper installs
// its own t.Cleanup(cancel) and never returns the cancel func or the result.
func newShutdownTestApp(t *testing.T) (app *App, baseURL string, pool *pgxpool.Pool, cancel context.CancelFunc, result *serveResult) {
	t.Helper()
	ctx := context.Background()

	dsn := storetesting.NewDSN(t)
	t.Setenv("ARGUS_DATABASE_URL", dsn)
	t.Setenv("ARGUS_HTTP_ADDR", "127.0.0.1:0")

	cfg, warnings, err := config.Load("")
	require.NoError(t, err)
	require.Empty(t, warnings)

	logger := slog.New(slog.NewTextHandler(debugLogOutput(), nil))
	// Its own registry, not the default one: this file's Apps share a test
	// binary with e2e_ingest_test.go's, and the ingest pipeline's metric
	// names would collide on prometheus.DefaultRegisterer — which panics
	// rather than failing gracefully. Same reason, and same fix, as
	// read_api_e2e_test.go's newE2EApp.
	a, err := New(ctx, cfg, logger, WithRegisterer(prometheus.NewRegistry()))
	require.NoError(t, err)

	serveCtx, cancelServe := context.WithCancel(context.Background())
	res := &serveResult{done: make(chan struct{})}
	go func() {
		res.err = a.Serve(serveCtx)
		close(res.done)
	}()

	select {
	case <-a.Listening():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not start listening in time")
	}
	require.NotEmpty(t, a.Addr())

	t.Cleanup(func() {
		cancelServe()
		select {
		case <-res.done:
		case <-time.After(20 * time.Second):
		}
	})

	assertPool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(assertPool.Close)

	return a, "http://" + a.Addr(), assertPool, cancelServe, res
}

// TestE2E_ShutdownDrainSurvivesASlowInFlightRequest pins the M4 fix:
// ARGUS_SHUTDOWN_GRACE must not be spent once against http.Server.Shutdown
// and then again (with whatever's left, possibly nothing) against the
// ingest drain. It sets ARGUS_SHUTDOWN_GRACE well below the time a
// deliberately slow in-flight POST /ingest/hook request takes to finish
// reading its body, so http.Server.Shutdown is guaranteed to hit its
// deadline while that request is still being read — the exact scenario the
// M4 finding describes (serve.go:142's evidence). It then asserts that a
// SEPARATE, already-enqueued hook event (sitting unflushed in the ingest
// pipeline the whole time, since ARGUS_INGEST_FLUSH is set far longer than
// the whole test) still lands in the database once shutdown finishes.
//
// Before the fix, drainIngest received the SAME shutdownCtx passed to
// http.Server.Shutdown — already expired by the time the slow request
// forces Shutdown to hit its own deadline — so Pipeline.Close's
// `case <-ctx.Done()` branch fired immediately, cancelled the in-flight
// flush, and the queued event was dropped even though Postgres was
// healthy the entire time and the drop had nothing to do with the slow
// request itself.
func TestE2E_ShutdownDrainSurvivesASlowInFlightRequest(t *testing.T) {
	// Generous, not tight: this test's point is the RELATIVE ordering (the
	// slow request must outlast the grace, and the drain must still
	// complete after its own fresh grace), not shaving the grace to the
	// bone — a loaded CI/dev machine can add real seconds of latency to an
	// otherwise-instant write, and this must still pass.
	const grace = 3 * time.Second
	const slowRequestDelay = 8 * time.Second // deliberately >> grace
	t.Setenv("ARGUS_SHUTDOWN_GRACE", grace.String())
	// Far longer than this whole test, so the fixture event sent below sits
	// unflushed in the pipeline's in-memory buffer until Close() forces an
	// immediate flush on shutdown — never flushed early by the interval.
	t.Setenv("ARGUS_INGEST_FLUSH", "5m")

	_, baseURL, pool, cancel, _ := newShutdownTestApp(t)
	ctx := context.Background()

	// The fixture: a fast, ordinary hook request whose event must still be
	// on disk after shutdown, proving the drain got a real budget.
	fastSessionID := "shutdown-grace-fast-session"
	fastPayload := fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SessionStart","cwd":"/tmp/shutdown-grace-fast"}`, fastSessionID)
	fastReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/ingest/hook", bytes.NewReader([]byte(fastPayload)))
	require.NoError(t, err)
	fastReq.Header.Set("Content-Type", "application/json")
	fastResp, err := http.DefaultClient.Do(fastReq)
	require.NoError(t, err)
	_ = fastResp.Body.Close()
	require.Less(t, fastResp.StatusCode, 300, "the fixture hook request must be accepted")

	// The slow request: its body-read phase alone (slowRequestDelay) is
	// deliberately much longer than `grace`, so http.Server.Shutdown is
	// certain to hit its own deadline while this request is still being
	// read, regardless of how slow the machine running this test is.
	slowSessionID := "shutdown-grace-slow-session"
	slowPayload := []byte(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SessionStart","cwd":"/tmp/shutdown-grace-slow"}`, slowSessionID))
	slowBody := &blockingBody{payload: slowPayload, delay: slowRequestDelay, onStart: make(chan struct{})}
	slowReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/ingest/hook", slowBody)
	require.NoError(t, err)
	slowReq.Header.Set("Content-Type", "application/json")
	slowReq.ContentLength = -1

	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		resp, err := http.DefaultClient.Do(slowReq) //nolint:bodyclose // best-effort background request; the test does not depend on its outcome
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	// Wait for the slow request to actually be in flight (its body's first
	// Read call fired) before triggering shutdown, rather than guessing a
	// fixed real-time delay — robust to however slow this machine is.
	select {
	case <-slowBody.onStart:
	case <-time.After(20 * time.Second):
		t.Fatal("the slow request never started being read by the server")
	}

	cancel() // triggers Serve's shutdown() sequence

	require.Eventually(t, func() bool {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, fastSessionID).Scan(&exists); err != nil {
			return false
		}
		return exists
	}, grace+10*time.Second, 100*time.Millisecond,
		"the fixture event must survive shutdown: the ingest drain must get its own shutdown-grace budget, independent of how long the slow in-flight request kept http.Server.Shutdown busy")

	<-slowDone
}

// TestE2E_HTTPServerHasReadWriteIdleTimeouts pins the M11 fix: serve.go's
// http.Server must set ReadTimeout/WriteTimeout/IdleTimeout, not just
// ReadHeaderTimeout — otherwise a client that finishes headers and then
// trickles its body (or never reads its response) holds a connection and a
// goroutine open indefinitely, on endpoints unauthenticated by default
// (ARGUS_INGEST_TOKEN's empty default). White-box (package app, not
// app_test) so it can read the unexported a.server field Serve populates,
// rather than actually waiting out a 30s+ timeout end to end.
func TestE2E_HTTPServerHasReadWriteIdleTimeouts(t *testing.T) {
	a, baseURL, _, cancel, _ := newShutdownTestApp(t)
	defer cancel()

	// Listening() only guarantees the listener is bound (serve.go sets it
	// BEFORE constructing a.server); wait for a real round trip to succeed
	// so a.server is guaranteed populated before this test reads it.
	resp, err := http.Get(baseURL + "/healthz")
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.NotNil(t, a.server, "Serve must have populated a.server by the time a request succeeds")
	require.Positive(t, a.server.ReadHeaderTimeout, "ReadHeaderTimeout must still be set (pre-existing)")
	require.Positive(t, a.server.ReadTimeout, "ReadTimeout must be set: chi's mw.Timeout only cancels the request context, which io.ReadAll(r.Body) never observes")
	require.Positive(t, a.server.WriteTimeout, "WriteTimeout must be set")
	require.Positive(t, a.server.IdleTimeout, "IdleTimeout must be set")
}

// TestE2E_ServeRunsShutdownWhenServerExitsOnItsOwn pins the m6 fix: when
// the HTTP server exits on its own (serveErr fires) before ctx is ever
// cancelled, Serve must still run the full shutdown() sequence — flipping
// ReadyState false, draining the ingest pipeline, and closing the store —
// not return the bare server outcome and skip shutdown() entirely, which
// left ReadyState permanently "ready" on an already-dead server (serve.go's
// former "nothing to shut down" comment, which was wrong: shutdown() is
// exactly what makes /readyz, the pipeline, and the pool all agree the
// process is going down).
//
// It forces this path with a.server.Close() (not Shutdown, and ctx is never
// cancelled) so the accept loop's goroutine returns via serveErr — an
// ErrServerClosed, which serve.go's own filtering turns into a nil
// serveErrOrNil — exercising exactly the "the case <-serveErr fires" branch
// m6 is about, independent of the ctx.Done() branch every other test in
// this package goes through.
func TestE2E_ServeRunsShutdownWhenServerExitsOnItsOwn(t *testing.T) {
	a, baseURL, _, cancel, result := newShutdownTestApp(t)
	defer cancel()

	resp, err := http.Get(baseURL + "/healthz")
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.True(t, a.ready.Ready(), "sanity: a freshly started App must report ready")

	require.NoError(t, a.server.Close(), "forcing the accept loop to exit on its own")

	select {
	case <-result.done:
	case <-time.After(30 * time.Second):
		t.Fatal("Serve did not return after the server closed itself")
	}

	require.False(t, a.ready.Ready(),
		"Serve must run shutdown() even when the server exits on its own (before ctx.Done()), flipping ReadyState false")
}
