package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
)

// spaFixtureAssets provides a minimal SPA build: index.html, robots.txt, and a hashed asset.
func spaFixtureAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<html><body>argus spa</body></html>")},
		"robots.txt":  &fstest.MapFile{Data: []byte("User-agent: *\nDisallow:\n")},
		"assets/x.js": &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
}

// TestMountSPA_MissingRootStaticFile_404NotIndex verifies missing static assets 404 (not serve index.html).
func TestMountSPA_MissingRootStaticFile_404NotIndex(t *testing.T) {
	t.Parallel()

	r := httpapi.New(httpapi.Deps{Assets: spaFixtureAssets()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/favicon.svg", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), "argus spa", "a missing static asset must not fall back to index.html")
}

// TestMountSPA_ExistingRootStaticFile_ServedWithRealContentType verifies existing static files are served correctly.
func TestMountSPA_ExistingRootStaticFile_ServedWithRealContentType(t *testing.T) {
	t.Parallel()

	r := httpapi.New(httpapi.Deps{Assets: spaFixtureAssets()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/robots.txt", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	require.Equal(t, "User-agent: *\nDisallow:\n", rec.Body.String())
}

// TestMountSPA_ClientSideRoute_StillServesIndex verifies client-side routes still serve index.html.
func TestMountSPA_ClientSideRoute_StillServesIndex(t *testing.T) {
	t.Parallel()

	r := httpapi.New(httpapi.Deps{Assets: spaFixtureAssets()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/sessions/abc", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), "argus spa")
}

// TestEmbeddedAssets_ServesCommittedPlaceholder exercises the real embed
// build (no Deps.Assets override), proving the placeholder committed at
// assets/dist/index.html compiles into the binary and is served for a clean
// checkout that has never run `pnpm build`.
func TestEmbeddedAssets_ServesCommittedPlaceholder(t *testing.T) {
	r := httpapi.New(httpapi.Deps{})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

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
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
}
