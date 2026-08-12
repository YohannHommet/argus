package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/YohannHommet/argus/server/internal/config"
)

// requestTimeout bounds how long a single request may run before chi's
// Timeout middleware cancels its context. SPEC §3.7 has no dedicated config
// key for this yet, so it is a constant until one is added.
const requestTimeout = 30 * time.Second

// accessLogSampleRate mirrors SPEC §3.8's ingest-log sampling rule, applied
// to the general access log.
const accessLogSampleRate = 100

// HealthChecker is the minimal capability httpapi needs from the storage
// layer for GET /readyz: internal/store.Store satisfies it structurally,
// but httpapi depends only on this narrower, consumer-owned port rather
// than the full Store interface.
type HealthChecker interface {
	Health(ctx context.Context) error
}

// Mounter lets a not-yet-built package attach its own routes to the router
// without router.go ever being edited again. P2-10 (OTLP receivers under
// /v1/*) and P2-11 (the hooks webhook under /ingest/hook) each implement
// one. A nil Mounter in Deps defaults to a no-op so P1-05 compiles and
// serves correctly with nothing mounted yet.
type Mounter interface {
	Mount(r chi.Router)
}

type noopMounter struct{}

func (noopMounter) Mount(chi.Router) {}

// Deps are everything New needs to build the router.
type Deps struct {
	Config *config.Config // nil is treated as all-defaults (no CORS, no auth, UI enabled)
	Store  HealthChecker  // nil readyz skips the DB check (used by tests that don't care)
	Logger *slog.Logger   // nil disables the access log
	Ready  *ReadyState    // nil is treated as always-ready

	OTLPMounter Mounter // future P2-10 (POST /v1/logs, /v1/metrics, /v1/traces); nil = no-op
	HookMounter Mounter // future P2-11 (POST /ingest/hook); nil = no-op

	Assets fs.FS // nil uses the embedded web/dist build (assets.go)
}

// New builds the full Argus HTTP router: the middleware chain, ops
// endpoints, the versioned read API, the ingest mount seams, and (if
// ARGUS_UI_ENABLED) the embedded SPA.
func New(d Deps) http.Handler {
	if d.OTLPMounter == nil {
		d.OTLPMounter = noopMounter{}
	}
	if d.HookMounter == nil {
		d.HookMounter = noopMounter{}
	}
	if d.Assets == nil {
		d.Assets = embeddedAssets()
	}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP) //nolint:staticcheck // deprecated (IP spoofing risk) with no chi-provided trusted-proxy replacement yet; acceptable for the Phase 1 single-host walking skeleton, revisit before exposing Argus behind an untrusted load balancer
	r.Use(chimw.Timeout(requestTimeout))
	if d.Logger != nil {
		r.Use(AccessLog(d.Logger, accessLogSampleRate))
	}
	if d.Config != nil && d.Config.CORSOrigins != "" {
		r.Use(CORS(d.Config.CORSOrigins))
	}

	r.Get("/healthz", healthzHandler)
	r.Get("/readyz", readyzHandler(d.Store, d.Ready))
	r.Handle("/metrics", promhttp.Handler())

	// Ingest mount seam #1: future OTLP receivers (SPEC §3.4), top-level
	// /v1/* — distinct from the read API's /api/v1/*.
	d.OTLPMounter.Mount(r)

	r.Route("/api", func(api chi.Router) {
		api.NotFound(problemNotFoundHandler)
		api.MethodNotAllowed(problemMethodNotAllowedHandler)

		api.Route("/v1", func(v1 chi.Router) {
			v1.NotFound(problemNotFoundHandler)
			v1.MethodNotAllowed(problemMethodNotAllowedHandler)
			if d.Config != nil {
				v1.Use(RequireAPIToken(d.Config.APIToken))
			}

			v1.Get("/meta", metaHandler(d.Config))
		})
	})

	// Ingest mount seam #2: the future hooks webhook (SPEC §3.5), POST
	// /ingest/hook.
	d.HookMounter.Mount(r)

	if d.Config == nil || d.Config.UIEnabled {
		mountSPA(r, d.Assets)
	} else {
		// UI disabled: keep unmatched paths in the same problem+json shape
		// as the rest of the API rather than falling back to a plain-text
		// 404 the SPA path would otherwise supply.
		r.NotFound(problemNotFoundHandler)
	}

	return r
}

func problemNotFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusNotFound, "not-found", "no such resource")
}

func problemMethodNotAllowedHandler(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusMethodNotAllowed, "method-not-allowed", "method not allowed on this resource")
}
