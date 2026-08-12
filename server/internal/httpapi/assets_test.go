package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
)

// TestEmbeddedAssets_ServesCommittedPlaceholder exercises the real
// go:embed build (no Deps.Assets override), proving the placeholder
// committed at assets/dist/index.html compiles into the binary and is
// served for a clean checkout that has never run `pnpm build`.
func TestEmbeddedAssets_ServesCommittedPlaceholder(t *testing.T) {
	r := httpapi.New(httpapi.Deps{})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), "pnpm build")
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

// TestEmbeddedAssets_UIDisabled asserts ARGUS_UI_ENABLED=false stops the
// embedded SPA (including its placeholder) from being served at all.
func TestEmbeddedAssets_UIDisabled(t *testing.T) {
	r := httpapi.New(httpapi.Deps{Config: &config.Config{UIEnabled: false}})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
}
