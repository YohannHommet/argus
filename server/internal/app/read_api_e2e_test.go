//go:build e2e

// Package app — read_api_e2e_test.go: pins Phase-3 read API is mounted on
// the real server (not just available as handlers). Router.go mounts each
// group only `if d.Reader != nil`, and Serve omitted setting this for a time
// while all handler tests still passed (they construct httpapi.New directly).
// This test goes through New + Serve to catch route-table regressions.
package app

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestServe_ReadAPIRoutesAreMounted asserts every Phase-3 read route the
// server is supposed to expose answers something other than 404 through the
// real Serve path.
//
// The assertion is deliberately "not 404, not 501" rather than "200": some of
// these need path parameters or return 400 for a missing one, and this test's
// subject is the route table, not the handlers (which have their own suites).
// 404 is the exact symptom of an unmounted group, so that is what it rules
// out — for a session id that does exist, 404 would otherwise be ambiguous.
func TestServe_ReadAPIRoutesAreMounted(t *testing.T) {
	// Its own registry: this is the second App the package's e2e binary
	// constructs, and the ingest/rollup collectors would otherwise be
	// registered on the default registerer twice, which panics.
	a, baseURL, pool := newE2EApp(t, WithRegisterer(prometheus.NewRegistry()))

	// app.go's New imports the embedded model price table on every startup
	// (the fix for the sibling defect: an empty model_prices table leaves
	// cost_estimated_usd/estimated_share silently 0 forever). Nothing else
	// in this suite seeds model_prices or asserts on it, so removing or
	// reordering that startup import would leave the whole suite green.
	require.Positive(t, scalarInt(t, pool, `SELECT count(*) FROM model_prices`),
		"App.New must import the embedded price table on startup, or cost_estimated_usd/estimated_share silently stay 0 (SPEC)")

	// A session row so the session-scoped routes have a real id to address:
	// otherwise a legitimate "no such session" 404 is indistinguishable from
	// the unmounted-route 404 this test exists to catch.
	const sessionID = "read-api-route-probe"
	_, err := pool.Exec(t.Context(),
		`INSERT INTO sessions (id, vendor, first_seen_at, last_event_at) VALUES ($1, 'claude_code', now(), now())`,
		sessionID)
	require.NoError(t, err)

	paths := []string{
		"/api/v1/sessions",
		"/api/v1/sessions?limit=2",
		"/api/v1/sessions/" + sessionID,
		"/api/v1/sessions/" + sessionID + "/timeline",
		"/api/v1/sessions/" + sessionID + "/turns",
		"/api/v1/sessions/" + sessionID + "/tool-calls",
		"/api/v1/sessions/" + sessionID + "/subagents",
		"/api/v1/events",
		"/api/v1/tool-calls",
		"/api/v1/analytics/summary",
		"/api/v1/analytics/timeseries?metric=cost",
		"/api/v1/analytics/breakdown?dimension=model",
		"/api/v1/analytics/decisions",
		"/api/v1/facets",
		"/api/v1/meta",
		"/api/v1/quality/unknown-kinds",
		"/api/v1/quality/hook-latency",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+path, nil)
			require.NoError(t, reqErr)
			resp, doErr := http.DefaultClient.Do(req)
			require.NoError(t, doErr)
			defer func() { _ = resp.Body.Close() }()

			require.NotEqual(t, http.StatusNotFound, resp.StatusCode,
				"%s is not mounted on the real server: this is the symptom of a Deps port left nil in Serve, which no handler-level test can see", path)
			require.Less(t, resp.StatusCode, http.StatusInternalServerError,
				"%s returned %d — mounted but failing", path, resp.StatusCode)
		})
	}

	require.NotEmpty(t, a.Addr())
}
