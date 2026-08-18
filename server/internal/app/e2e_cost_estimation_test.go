//go:build e2e

// Package app — e2e_cost_estimation_test.go is D-30's end-to-end regression
// test (docs/review/phase-4-gauntlet.md, candidate D-30, owner-ratified
// 2026-08-18: "the sessions/turns projections report a measured $0.00 for
// cost they cannot know"). It drives the real argus-sim CLI
// (--cost-mode=omit, exactly the flag the gauntlet's own repro used) against
// a real running App over real HTTP — never a hand-built batch of
// model.Event — and reads the result back through the real GET
// /api/v1/sessions endpoint, so it exercises the whole path the gauntlet's
// captured screenshot came from: ingest -> normalize -> WriteBatch's
// session/turn fold -> the read API -> the wire shape the UI renders.
//
// Before the fix, every session in a --cost-mode=omit run showed the exact
// D-30 signature: cost.usd == 0 && cost.estimated_usd == 0 while the session
// still burned real, non-zero tokens — a measured-looking zero for a cost
// Argus simply never tried to estimate. This test fails on that signature
// and passes once upsert_session.go/upsert_turn.go actually run the
// estimator (internal/pricing, via the model_prices App.New imports at
// startup — see read_api_e2e_test.go's comment on that startup import).
package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// sessionCostRow is the slice of GET /api/v1/sessions' wire shape
// (model.SessionSummary, openapi.yaml's SessionsListResponse) this test
// needs — decoded locally rather than importing model/httpapi's response
// structs, since this file only reads a handful of fields off it.
type sessionCostRow struct {
	ID     string `json:"id"`
	Tokens struct {
		Input         int64 `json:"input"`
		Output        int64 `json:"output"`
		CacheRead     int64 `json:"cache_read"`
		CacheCreation int64 `json:"cache_creation"`
	} `json:"tokens"`
	Cost struct {
		USD            float64 `json:"usd"`
		ReportedUSD    float64 `json:"reported_usd"`
		EstimatedUSD   float64 `json:"estimated_usd"`
		EstimatedShare float64 `json:"estimated_share"`
	} `json:"cost"`
}

type sessionsCostListResponse struct {
	Data []sessionCostRow `json:"data"`
}

// TestE2E_CostEstimation_D30_OmitModeSessionsReportEstimatedNotZero drives a
// --cost-mode=omit demo run and asserts every session that actually burned
// tokens has a non-zero cost_estimated_usd/estimated_share — the D-30 fix —
// and that no session exhibits the D-30 signature (measured-looking zero
// cost on a session with non-zero tokens).
func TestE2E_CostEstimation_D30_OmitModeSessionsReportEstimatedNotZero(t *testing.T) {
	app, baseURL, pool := newE2EApp(t, WithRegisterer(prometheus.NewRegistry()))

	// App.New imports server/db/prices/*.json on startup (see
	// read_api_e2e_test.go's comment on this exact startup step): the sim's
	// demo-mode models are drawn from precisely that fixed set
	// (claude-opus-5, claude-sonnet-4-5, claude-haiku-4-5 — internal/sim/
	// projects.go), so every uncosted llm.request this run produces must
	// resolve a price.
	require.Positive(t, scalarInt(t, pool, `SELECT count(*) FROM model_prices`))

	args := []string{
		"--mode=demo",
		"--seed=99",
		"--cost-mode=omit",
		"--flush-immediately",
		"--target=" + baseURL,
	}
	stdout, stderr, code, report := runSim(t, args)
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.True(t, report.AllOK(), "expected an all-2xx run: %+v", report.StatusHistogram)

	waitForIngestQuiescence(t, pool, app)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/v1/sessions?limit=500", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body sessionsCostListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.Data, "expected the seeded demo sessions to come back")

	checkedAny := false
	for _, s := range body.Data {
		totalTokens := s.Tokens.Input + s.Tokens.Output + s.Tokens.CacheRead + s.Tokens.CacheCreation
		if totalTokens == 0 {
			continue // a session with no llm.request tokens at all has nothing to estimate; not the D-30 case
		}
		checkedAny = true

		// The D-30 signature itself: a measured-looking zero on a session
		// that demonstrably has real token usage behind it.
		isD30Signature := s.Cost.USD == 0 && s.Cost.EstimatedUSD == 0
		require.False(t, isD30Signature, "session %s exhibits the D-30 signature: usd=0 and estimated_usd=0 with %d non-zero tokens", s.ID, totalTokens)

		require.Positive(t, s.Cost.EstimatedUSD, "session %s: cost-mode=omit means no event carried a reported cost, so cost.estimated_usd must be > 0", s.ID)
		require.Positive(t, s.Cost.EstimatedShare, "session %s: an all-estimated session must report a non-zero estimated_share", s.ID)
		require.Zero(t, s.Cost.ReportedUSD, "session %s: --cost-mode=omit means no event ever carried a reported cost_usd", s.ID)
	}
	require.True(t, checkedAny, "expected at least one seeded session with non-zero tokens to actually exercise the D-30 assertion")
}
