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

// spaFixtureAssets is a minimal fake SPA build for the m24 audit finding's
// regression tests: an index.html (the SPA shell) plus one real root static
// file (robots.txt) — deliberately not favicon.svg, since assets.go's half
// of m24 must not depend on that file existing (another ticket adds it to
// web/public/).
func spaFixtureAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<html><body>argus spa</body></html>")},
		"robots.txt":  &fstest.MapFile{Data: []byte("User-agent: *\nDisallow:\n")},
		"assets/x.js": &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
}

// TestMountSPA_MissingRootStaticFile_404NotIndex is m24's core regression
// test: before the fix, any path that missed the /assets/* mount —
// including a genuinely missing root file like /favicon.svg — fell through
// to serveIndex and came back 200 text/html with the whole SPA document, so
// a client fetching a phantom asset never learned it was missing.
func TestMountSPA_MissingRootStaticFile_404NotIndex(t *testing.T) {
	t.Parallel()

	r := httpapi.New(httpapi.Deps{Assets: spaFixtureAssets()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/favicon.svg", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), "argus spa", "a missing static asset must not fall back to index.html")
}

// TestMountSPA_ExistingRootStaticFile_ServedWithRealContentType asserts the
// other half of m24's fix: a root static file that DOES exist in the
// assets FS is served through the FileServer (correct content, correct
// content-type) instead of being swallowed by the SPA fallback.
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

// TestMountSPA_ClientSideRoute_StillServesIndex guards against the
// obvious overcorrection: a client-side route with no file extension (the
// SPA's own URL space) must keep falling through to index.html exactly as
// before, even though it also misses the /assets/* mount.
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
