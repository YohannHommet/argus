// cursor_contract_test.go: M14 regression test against real postgres store.
// Handler-level tests use fakes and can only simulate decode failure; this
// file exercises the real httpapi<->store seam, which depguard requires live
// in internal/app (the only package allowed to import both, app.go's doc).
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
