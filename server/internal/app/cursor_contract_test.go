// cursor_contract_test.go is M14's required "goes through the real
// postgres store" regression test: storetest.Fake never decodes a cursor
// at all (its ListSessionsFunc/ListEventsFunc/ListToolCallsFunc are plain
// Go closures wired per-test), so every other handler-level cursor test in
// this package can only simulate the store's decode failure, not prove the
// real decoder actually produces it. This file wires httpapi.New to a real
// *postgres.Store (storetesting.NewPool's freshly-migrated schema) and
// replays the exact `{"k":"last_event_at","v":["x"]}` payload the M14
// audit finding cites, so the assertion is against production code on both
// sides of the httpapi<->store seam, not just httpapi's half.
//
// It lives in internal/app rather than internal/httpapi because it needs the
// concrete *postgres.Store, and depguard (SPEC §3.1) forbids internal/httpapi
// from importing internal/store/postgres — correctly, since that is the
// layering the rule exists to protect. internal/app is the one package
// documented as allowed to know about every layer at once (see app.go), which
// makes it the honest home for a test whose whole subject is the seam between
// two of them.
package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// TestListSessions_RealPostgres_MalformedCursor_400 skips (via
// storetesting.NewPool) when neither ARGUS_TEST_DATABASE_URL nor a usable
// Docker daemon is available, matching every other real-database test in
// this codebase.
func TestListSessions_RealPostgres_MalformedCursor_400(t *testing.T) {
	pool := storetesting.NewPool(t)
	st := postgres.New(pool)
	r := httpapi.New(httpapi.Deps{Reader: st})

	// base64url(no padding) of {"k":"last_event_at","v":["x"]}: one value
	// where read_sessions.go's decodeSessionCursor requires exactly two
	// (the sort column's own value, then the `id` tiebreak) — structurally
	// valid enough to pass httpapi.DecodeCursor's shallow check (non-empty
	// k, non-empty v), invalid once postgres actually decodes it.
	const malformedCursor = "eyJrIjoibGFzdF9ldmVudF9hdCIsInYiOlsieCJdfQ"

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?cursor="+malformedCursor, nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-cursor"`)
}
