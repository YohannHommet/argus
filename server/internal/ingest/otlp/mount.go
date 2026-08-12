// Package otlp implements docs/SPEC.md §3.4: the OTLP/HTTP receiver mounted
// at the root of the router (POST /v1/logs, /v1/metrics, /v1/traces) so that
// OTEL_EXPORTER_OTLP_ENDPOINT=http://argus:8080 works with zero extra
// config — the OTel SDK appends the per-signal path itself.
//
// depguard (SPEC §3.1, same rule as internal/ingest): this package must
// never import internal/httpapi or internal/query. The ingest-token
// middleware (httpapi/middleware.RequireIngestToken) therefore arrives here
// as a plain func(http.Handler) http.Handler parameter to New, built by
// internal/app, which is the only package allowed to know about both sides
// of that seam.
package otlp

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// Enqueuer is the narrow, consumer-owned port this package needs from
// internal/ingest.Pipeline (SPEC §3.6): *ingest.Pipeline satisfies this
// structurally, but handlers depend on this interface rather than the
// concrete type so handler_test.go can enqueue against a fake and assert
// exactly how many events/samples were handed to it, per this ticket's ACs.
type Enqueuer interface {
	// EnqueueEvents hands a batch of normalized log-derived events to the
	// event lane. See ingest.Pipeline.EnqueueEvents for the full contract
	// (non-blocking; ingest.ErrQueueFull on backpressure).
	EnqueueEvents(batch []model.Event) error

	// EnqueueMetrics is EnqueueEvents' counterpart for OTLP metric data
	// points. See ingest.Pipeline.EnqueueMetrics.
	EnqueueMetrics(batch []model.MetricSample) error
}

// metricsNamespace/metricsSubsystem give this package's self-metrics the
// "argus_otlp_*" prefix SPEC §3.4 names explicitly
// (argus_otlp_traces_discarded_total).
const (
	metricsNamespace = "argus"
	metricsSubsystem = "otlp"
)

// Metrics is this package's Prometheus self-observability surface. It is
// registered against an injectable prometheus.Registerer (lead note 5)
// rather than prometheus.DefaultRegisterer directly, so a test binary that
// constructs more than one Handler never panics on a duplicate metric-name
// registration — the same pattern internal/ingest.NewMetrics already
// establishes (internal/ingest/metrics.go).
type Metrics struct {
	// TracesDiscarded counts spans accepted and dropped by handleTraces
	// (SPEC §3.4: "accept, discard, count
	// argus_otlp_traces_discarded_total" — traces are out of scope per
	// DECISIONS.md, but silently 404-ing an exporter causes noisy
	// client-side retry loops).
	TracesDiscarded prometheus.Counter
}

// NewMetrics registers Metrics against reg. A nil reg defaults to
// prometheus.DefaultRegisterer (the production default; every test that
// cares about isolation passes its own prometheus.NewRegistry()).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		TracesDiscarded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "traces_discarded_total",
			Help: "OTLP spans accepted and discarded via POST /v1/traces (SPEC §3.4: traces are out of scope).",
		}),
	}
	reg.MustRegister(m.TracesDiscarded)
	return m
}

// Handler is the OTLP/HTTP receiver (SPEC §3.4). Construct with New, then
// call Mount to attach its three routes to a chi.Router — it satisfies
// internal/httpapi.Mounter structurally (Mount(r chi.Router)) so
// internal/app can wire it into httpapi.Deps.OTLPMounter without either
// package importing the other's concrete type.
type Handler struct {
	enqueuer     Enqueuer
	normalizer   *normalize.Normalizer
	maxBodyBytes int64
	auth         func(http.Handler) http.Handler
	logger       *slog.Logger
	metrics      *Metrics
}

// New builds a Handler.
//
//   - enqueuer is where normalized batches go (production: *ingest.Pipeline;
//     tests: a fake implementing Enqueuer).
//   - normalizer does the SPEC §1.5.1/§3.4 decode-to-model work; construct
//     it with normalize.NewNormalizer(clock, retentionRaw) — see that
//     package's doc, never a zero-value Normalizer.
//   - maxBodyBytes is ARGUS_INGEST_MAX_BODY_BYTES (SPEC §3.4: enforced on
//     the decompressed stream).
//   - auth is httpapi/middleware.RequireIngestToken(cfg.IngestToken) (or
//     any equivalent func(http.Handler) http.Handler) supplied by
//     internal/app, since this package cannot import internal/httpapi
//     (depguard). A nil auth is a no-op passthrough, matching
//     RequireIngestToken's own behaviour when the token is empty.
//   - logger defaults to slog.Default() when nil.
//   - reg is passed straight to NewMetrics.
func New(
	enqueuer Enqueuer,
	normalizer *normalize.Normalizer,
	maxBodyBytes int64,
	auth func(http.Handler) http.Handler,
	logger *slog.Logger,
	reg prometheus.Registerer,
) *Handler {
	if auth == nil {
		auth = func(next http.Handler) http.Handler { return next }
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		enqueuer:     enqueuer,
		normalizer:   normalizer,
		maxBodyBytes: maxBodyBytes,
		auth:         auth,
		logger:       logger,
		metrics:      NewMetrics(reg),
	}
}

// Mount attaches the three SPEC §3.4 routes at the root, each guarded by
// the auth middleware supplied to New. It satisfies httpapi.Mounter.
func (h *Handler) Mount(r chi.Router) {
	r.With(h.auth).Post("/v1/logs", h.handleLogs)
	r.With(h.auth).Post("/v1/metrics", h.handleMetrics)
	r.With(h.auth).Post("/v1/traces", h.handleTraces)
}
