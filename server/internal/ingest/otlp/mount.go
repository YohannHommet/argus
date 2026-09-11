// Package otlp implements SPEC §3.4: OTLP/HTTP receiver (POST /v1/logs, /v1/metrics, /v1/traces).
// depguard: no internal/httpapi or internal/query imports; auth middleware injected by internal/app.
package otlp

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// Enqueuer is the narrow port (*ingest.Pipeline satisfies structurally).
type Enqueuer interface {
	// EnqueueEvents queues log-derived events (non-blocking, ErrQueueFull on backpressure).
	EnqueueEvents(batch []model.Event) error
	// EnqueueMetrics queues OTLP metric samples (counterpart).
	EnqueueMetrics(batch []model.MetricSample) error
}

// SPEC §3.4: self-metrics prefix "argus_otlp_*".
const (
	metricsNamespace = "argus"
	metricsSubsystem = "otlp"
)

// Metrics is this package's Prometheus self-observability surface.
type Metrics struct {
	// TracesDiscarded counts spans dropped (SPEC §3.4: out of scope, but counted to avoid 404 retries).
	TracesDiscarded prometheus.Counter
}

// NewMetrics registers Metrics (nil reg → DefaultRegisterer; tests pass fresh registry).
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

// Handler is the OTLP/HTTP receiver (SPEC §3.4, satisfies httpapi.Mounter).
type Handler struct {
	enqueuer     Enqueuer
	normalizer   *normalize.Normalizer
	maxBodyBytes int64
	auth         func(http.Handler) http.Handler
	logger       *slog.Logger
	metrics      *Metrics
}

// New builds a Handler (auth/logger nil → defaults, normalizer must be non-zero).
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

// Mount attaches the three SPEC §3.4 routes (satisfies httpapi.Mounter).
func (h *Handler) Mount(r chi.Router) {
	r.With(h.auth).Post("/v1/logs", h.handleLogs)
	r.With(h.auth).Post("/v1/metrics", h.handleMetrics)
	r.With(h.auth).Post("/v1/traces", h.handleTraces)
}
