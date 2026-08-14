//go:build e2e

// Package app — read_api_e2e_test.go pins that the Phase-3 read API is
// actually mounted on the server this package builds.
//
// It exists because it was missing, and its absence hid a defect that would
// have shipped: httpapi/router.go mounts each read-API group only
// `if d.Reader != nil` (a nil-safe default inherited from P1-05's
// convention), and Serve did not set Reader or AnalyticsReader. Every one of
// P3-07's and P3-08's handler tests passed, and so did P3-09's conformance
// harness covering 100% of operationIds — all of them construct httpapi.New
// directly with a fake reader and never go through Serve. The real binary
// answered 404 on every read endpoint. `docker compose up` plus one curl was
// enough to see it; no test was.
//
// So this test deliberately goes the long way round: it starts the real App
// via New + Serve and speaks HTTP to the port it bound, exactly as an
// operator would. A route-table regression fails here even though the
// handlers themselves are fine.
package app

import (
	"net/http"
	"testing"

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
	a, baseURL, pool := newE2EApp(t)
	defer func() { _ = pool }()

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
