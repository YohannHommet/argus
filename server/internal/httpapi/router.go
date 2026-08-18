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
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// requestTimeout bounds how long a single request may run before
// StreamAwareTimeout's wrapped chi.Timeout cancels its context. SPEC §3.7
// has no dedicated config key for this yet, so it is a constant until one
// is added. See middleware.go's StreamAwareTimeout doc comment for why this
// is no longer the plain chimw.Timeout middleware (Trap 1, P5-02).
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

// MigrationsChecker is the second narrow, consumer-owned port GET /readyz
// needs (SPEC §3.8's "migrations current" condition, closing Phase-1
// deviation D-5): postgres.Store.MigrationsCurrent satisfies it
// structurally, so httpapi never imports internal/store/postgres just to
// name this method. A nil MigrationsChecker (the P1-05 default, and any
// test that doesn't care) makes readyzHandler report "current" without a
// live check — the same nil-safe convention HealthChecker already
// establishes.
type MigrationsChecker interface {
	MigrationsCurrent(ctx context.Context) (bool, error)
}

// QueueSaturationChecker is the third narrow port GET /readyz needs (SPEC
// §3.8's "queue not saturated" condition): internal/ingest.Pipeline
// satisfies it structurally via QueueSaturated(). httpapi cannot import
// internal/ingest directly (depguard: ingest must never import httpapi, and
// keeping the dependency one-directional through a structurally-satisfied
// port avoids even a docs-only coupling) — internal/app wires the concrete
// *ingest.Pipeline into this interface field. A nil checker (P1-05's
// default, before any pipeline exists) never fails readiness on this
// ground.
type QueueSaturationChecker interface {
	QueueSaturated() bool
}

// Reader is the narrow read-store port GET /api/v1/sessions, /events, and
// /tool-calls need (SPEC §3.1's httpapi -> query -> store direction):
// internal/store.Store satisfies it structurally, but httpapi depends only
// on the Reader methods P3-07 actually calls — not the analytics/facets/
// quality methods P3-08 owns, which that ticket will likely add to a
// sibling interface rather than widening this one. A nil Reader (P1-05's
// default, and any test that doesn't care) mounts none of these routes,
// the same nil-safe convention Mounter already establishes.
type Reader interface {
	ListSessions(ctx context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error)
	GetSession(ctx context.Context, id string) (*model.SessionDetail, error)
	ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error)
	ListEvents(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error)
	GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error)
	ListToolCalls(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error)
	SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error)
}

// AnalyticsReader is the narrow read-store port GET /api/v1/analytics/*,
// /facets, and /quality/* need (SPEC §3.1's httpapi -> query -> store
// direction) — the "sibling interface" Reader's own doc comment anticipates
// P3-08 adding rather than widening Reader itself: internal/store.Store
// satisfies it structurally, but httpapi depends only on the methods
// analytics.go/facets.go/quality.go/meta.go actually call. A nil
// AnalyticsReader (P1-05/P3-07's default, and any test that doesn't care)
// mounts none of these routes and leaves GET /api/v1/meta's P3-08 fields at
// their zero values, the same nil-safe convention Reader already
// establishes.
type AnalyticsReader interface {
	query.AnalyticsReader
	query.QualityReader
}

// Streamer is the narrow hub port GET /api/v1/stream and
// /api/v1/sessions/{id}/stream need (SPEC §5.3, P5-02): *stream.Hub
// satisfies it structurally. httpapi depends on this port alone, never
// stream.Hub's full surface — Publish/PublishStats/Shutdown are the ingest
// pipeline's and internal/app's job, not a read-only HTTP handler's.
type Streamer interface {
	Subscribe(topic stream.Topic, filter stream.Filter) (*stream.Subscription, error)
}

// Replayer is the narrow store port SSE reconnect replay needs (SPEC
// §5.2): postgres.Store.EventsSince satisfies it structurally. Kept
// separate from Reader rather than widening it, per the sibling-interface
// convention Reader's own doc comment prescribes (AnalyticsReader above is
// the precedent) — replay is a P5-02-only concern, not something P3-07's
// existing read handlers call.
type Replayer interface {
	EventsSince(ctx context.Context, after model.EventRef, windowStart time.Time, limit int) ([]model.Event, error)
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

	// Migrations and Queue back GET /readyz's other two SPEC §3.8
	// conditions. Both nil-safe (see their interface docs): P1-05's
	// existing tests and any future test that only cares about the DB
	// check keep working unchanged.
	Migrations MigrationsChecker
	Queue      QueueSaturationChecker

	// Reader backs the P3-07 read API (/sessions, /events, /tool-calls and
	// their sub-resources). nil mounts none of those routes — see Reader's
	// own doc comment.
	Reader Reader

	// Analytics backs the P3-08 read API (/analytics/*, /facets,
	// /quality/*) and extends GET /meta. nil mounts none of those routes
	// and leaves /meta's P3-08 fields at their zero values — see
	// AnalyticsReader's own doc comment.
	Analytics AnalyticsReader

	// Stream backs GET /api/v1/stream and /api/v1/sessions/{id}/stream
	// (SPEC §5, P5-02). nil mounts neither SSE route — the same nil-safe
	// convention Reader/Analytics/Mounter already establish. P5-03 wires
	// *stream.Hub in via internal/app/serve.go; this ticket does not touch
	// that file.
	Stream Streamer

	// Replay backs SSE reconnect (`Last-Event-ID`/`?after=`, SPEC §5.2).
	// nil alongside a non-nil Stream still mounts both SSE routes, but
	// every replay request is answered with `event: reset` instead of a
	// backlog — the honest degradation when there is no store to query,
	// rather than silently pretending no backlog was ever requested. See
	// Streamer's doc comment for why this is a separate port from Reader.
	Replay Replayer

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
	r.Use(StreamAwareTimeout(requestTimeout))
	if d.Logger != nil {
		r.Use(AccessLog(d.Logger, accessLogSampleRate))
	}
	if d.Config != nil && d.Config.CORSOrigins != "" {
		r.Use(CORS(d.Config.CORSOrigins))
	}

	// m18 audit finding: the problem+json NotFound/MethodNotAllowed
	// handlers used to be installed only on the /api and /api/v1
	// subrouters below, so a wrong-method request against a root-mounted
	// route (the OTLP receivers' /v1/logs|metrics|traces, the hooks
	// webhook's /ingest/hook) got chi's bodyless default 405 instead of
	// the problem+json body openapi.yaml declares for all four. Root
	// MethodNotAllowed is installed once, here, before either ingest mount
	// seam registers its own routes.
	r.MethodNotAllowed(problemMethodNotAllowedHandler)

	r.Get("/healthz", healthzHandler)
	r.Get("/readyz", readyzHandler(d.Store, d.Migrations, d.Queue, d.Ready, d.Logger))
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

			v1.Get("/meta", metaHandler(d.Config, d.Analytics, d.Logger))

			// P3-07's read API. A nil Reader (P1-05's default) mounts none
			// of these, matching Mounter's nil-safe convention.
			if d.Reader != nil {
				mountSessionRoutes(v1, d.Reader, d.Logger)
				mountEventRoutes(v1, d.Reader, d.Logger)
				mountToolCallRoutes(v1, d.Reader, d.Logger)
			}

			// P3-08's read API. A nil Analytics (P1-05/P3-07's default)
			// mounts none of these, matching Reader's own nil-safe
			// convention above.
			if d.Analytics != nil {
				mountAnalyticsRoutes(v1, d.Analytics, d.Logger)
				mountFacetRoutes(v1, d.Analytics, d.Logger)
				mountQualityRoutes(v1, d.Analytics, d.Logger)
			}

			// P5-02's SSE routes (SPEC §5). A nil Stream (P1-05 through
			// Phase-4's default) mounts neither, matching Reader/
			// Analytics' own nil-safe convention above.
			if d.Stream != nil {
				mountStreamRoutes(v1, d.Stream, d.Replay, d.Config, d.Logger)
			}
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
