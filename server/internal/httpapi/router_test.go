package httpapi_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
)

// fakeStore is the minimal httpapi.HealthChecker fake used to exercise
// /readyz's up/down branches without a real database (SPEC §3.5 / §3.8).
type fakeStore struct {
	err error
}

func (f fakeStore) Health(_ context.Context) error { return f.err }

// mounterFunc lets a test satisfy httpapi.Mounter with a plain function,
// exercising the ingest.Mounter-shaped seam the PLAN's file-ownership note
// requires router.go to leave for P2.
type mounterFunc func(r chi.Router)

func (f mounterFunc) Mount(r chi.Router) { f(r) }

func testAssets(t *testing.T) fs.FS {
	t.Helper()
	return fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<html><body>argus</body></html>")},
		"assets/x.js": &fstest.MapFile{Data: []byte("console.log('hi')")},
		"favicon.ico": &fstest.MapFile{Data: []byte("ico")},
	}
}

func TestHealthz_OKWithoutDB(t *testing.T) {
	r := httpapi.New(httpapi.Deps{Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestReadyz_DownDB(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:  fakeStore{err: errors.New("connection refused")},
		Assets: testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:`)
}

func TestReadyz_UpDB(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:  fakeStore{},
		Assets: testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok","migrations":"current"}`, rec.Body.String())
}

func TestUnknownAPIRoute_404Problem(t *testing.T) {
	r := httpapi.New(httpapi.Deps{Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/nope", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-found"`)
}

func TestSPAFallback_ServesIndexHTML(t *testing.T) {
	r := httpapi.New(httpapi.Deps{Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/frontend/route", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), "argus")
}

func TestHashedAsset_ImmutableCacheHeader(t *testing.T) {
	r := httpapi.New(httpapi.Deps{Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/x.js", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
}

func TestAPIToken_401WithoutBearer200With(t *testing.T) {
	cfg := &config.Config{APIToken: "s3cret", UIEnabled: true}
	r := httpapi.New(httpapi.Deps{Config: cfg, Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/meta", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/meta", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMeta_NoTokenConfigured_AlwaysOK(t *testing.T) {
	cfg := &config.Config{RetentionRawDays: 90, UIEnabled: true}
	r := httpapi.New(httpapi.Deps{Config: cfg, Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/meta", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"retention_days":90`)
}

// TestShutdown_InFlightRequestCompletes exercises the same http.Server.
// Shutdown mechanism internal/app.Serve uses for the SPEC §3.8 shutdown
// sequence: an in-flight request must complete, and Shutdown must return,
// well before any reasonable grace deadline. It uses the HookMounter seam
// to install a slow handler, which doubles as coverage that the seam is
// wired correctly.
func TestShutdown_InFlightRequestCompletes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	slow := mounterFunc(func(r chi.Router) {
		r.Get("/slow", func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
			w.WriteHeader(http.StatusOK)
		})
	})

	handler := httpapi.New(httpapi.Deps{HookMounter: slow, Assets: testAssets(t)})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	var reqErr error
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		req, buildErr := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/slow", nil)
		if buildErr != nil {
			reqErr = buildErr
			return
		}
		resp, err := http.DefaultClient.Do(req)
		reqErr = err
		if resp != nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow handler never started")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Config.Shutdown(context.Background())
	}()

	// Give Shutdown a moment to actually be blocking on the in-flight
	// request before releasing it, so this test would fail if Shutdown
	// dropped the connection instead of waiting for it.
	time.Sleep(50 * time.Millisecond)
	close(release)

	const grace = 2 * time.Second
	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
	case <-time.After(grace):
		t.Fatalf("Shutdown did not return within the %s grace deadline", grace)
	}

	<-reqDone
	require.NoError(t, reqErr)
}

// TestRootMountedRoute_WrongMethod_ProblemJSON405 is the m18 audit
// finding's regression test: the problem+json NotFound/MethodNotAllowed
// handlers used to be installed only on the /api and /api/v1 subrouters, so
// a wrong-method request against a root-mounted route (here standing in for
// the real OTLP receivers' POST-only /v1/logs|metrics|traces, or the hooks
// webhook's POST-only /ingest/hook) fell through to chi's bodyless default
// 405 — while openapi.yaml declares problem+json for all four.
func TestRootMountedRoute_WrongMethod_ProblemJSON405(t *testing.T) {
	t.Parallel()

	otlp := mounterFunc(func(r chi.Router) {
		r.Post("/v1/logs", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	r := httpapi.New(httpapi.Deps{OTLPMounter: otlp, Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/logs", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:method-not-allowed"`)
}
