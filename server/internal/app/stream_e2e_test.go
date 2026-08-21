//go:build e2e

// stream_e2e_test.go pins P5-03's two ACs that only the real, running
// server can prove:
//
//   - the SSE routes are actually mounted (TestServe_StreamRoutesAreMounted
//     is TestServe_ReadAPIRoutesAreMounted's P5-03 sibling — same defect
//     class: router.go mounts them `if d.Stream != nil`, and every
//     httpapi-level SSE handler test constructs httpapi.New directly, so
//     none of them can see Serve leaving Deps.Stream nil);
//   - shutdown actually delivers the `event: shutdown` frame promptly,
//     pinning the App.shutdown ordering fix documented in serve.go (hub
//     shutdown before http.Server.Shutdown).
package app

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestServe_StreamRoutesAreMounted(t *testing.T) {
	// Its own registry: this test constructs a second App in this package's
	// e2e binary, and the ingest/rollup collectors would otherwise be
	// registered on the default registerer twice, which panics (the same
	// reason TestServe_ReadAPIRoutesAreMounted's own newE2EApp call does
	// this).
	_, baseURL, _ := newE2EApp(t, WithRegisterer(prometheus.NewRegistry()))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/v1/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.NotEqual(t, http.StatusNotFound, resp.StatusCode,
		"GET /api/v1/stream is not mounted on the real server: this is the symptom of Deps.Stream left nil in Serve, which no handler-level httpapi test can see")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

// readUntilEvent scans raw SSE lines off r until one names wantEvent (an
// "event: <name>" line), then returns. It has no timeout of its own —
// callers bound it externally (a select against time.After, mirroring
// internal/httpapi/sse_test.go's own sseReader convention of leaving the
// timeout to the caller's context/select rather than baking one in here).
func readUntilEvent(t *testing.T, r *bufio.Reader, wantEvent string) {
	t.Helper()
	want := "event: " + wantEvent
	for {
		line, err := r.ReadString('\n')
		require.NoError(t, err, "reading SSE frame off the wire before seeing %q", want)
		if strings.TrimRight(line, "\r\n") == want {
			return
		}
	}
}

// TestE2E_ShutdownDeliversStreamFrame pins the App.shutdown ordering fix
// (serve.go: a.hub.Shutdown() must run BEFORE a.server.Shutdown). Before
// that fix, http.Server.Shutdown would block for the WHOLE
// ARGUS_SHUTDOWN_GRACE waiting for the SSE handler's still-open connection
// to return on its own — which it never does until the hub closes its
// subscription — so the shutdown frame would arrive only once the grace
// itself expired, if at all. This test asserts both halves of the fix: the
// client actually receives `event: shutdown`, AND it arrives (and Serve
// returns) well before the grace elapses, not merely before it times out.
func TestE2E_ShutdownDeliversStreamFrame(t *testing.T) {
	// Generous, not tight (same posture as shutdown_test.go's own grace
	// constants): the assertion that matters is "well before grace", which
	// a healthy delivery satisfies by margins of milliseconds vs. seconds,
	// not by shaving the bound to the wire on a machine that might be
	// loaded.
	const grace = 5 * time.Second
	t.Setenv("ARGUS_SHUTDOWN_GRACE", grace.String())

	_, baseURL, _, cancel, result := newShutdownTestApp(t)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/api/v1/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	r := bufio.NewReader(resp.Body)
	// The very first frame is always `retry:` (SPEC §5.3), followed by the
	// blank line ending it — consume both so readUntilEvent starts scanning
	// from a known position.
	first, err := r.ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first, "retry: "), "first SSE line must be retry:, got %q", first)
	_, err = r.ReadString('\n')
	require.NoError(t, err)

	shutdownReceived := make(chan struct{})
	go func() {
		readUntilEvent(t, r, "shutdown")
		close(shutdownReceived)
	}()

	start := time.Now()
	cancel() // triggers Serve's shutdown() sequence

	select {
	case <-shutdownReceived:
	case <-time.After(grace):
		t.Fatal("client never received event: shutdown within the shutdown grace")
	}
	require.Less(t, time.Since(start), grace,
		"the shutdown frame must arrive well before the grace period elapses — the whole point of shutting the hub down before http.Server.Shutdown")

	select {
	case <-result.done:
	case <-time.After(grace):
		t.Fatal("Serve did not return within the shutdown grace")
	}
	require.Less(t, time.Since(start), grace,
		"Serve must return promptly once shutdown's steps complete, not block for the full grace")
}
