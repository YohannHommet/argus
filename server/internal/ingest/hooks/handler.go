// Package hooks implements P2-11: Claude Code hooks webhook POST /ingest/hook (SPEC §3.5).
// Leaf receiver: depends on normalize and narrow Enqueuer port only (depguard: no httpapi).
package hooks

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// Metrics guards the <20ms p99 budget (SPEC §3.5 guard rail for 1.5s shared).
type Metrics struct {
	// Duration observes ServeHTTP wall time (SPEC §3.5).
	Duration prometheus.Histogram
}

// NewMetrics registers Metrics (sub-ms to 1s buckets for <20ms p99 resolution).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		Duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "argus",
			Name:      "hook_handler_duration_seconds",
			Help:      "POST /ingest/hook handler wall time, success or error (SPEC §3.5's <20ms p99 budget).",
			Buckets:   []float64{.0005, .001, .002, .005, .01, .02, .05, .1, .25, .5, 1},
		}),
	}
	reg.MustRegister(m.Duration)
	return m
}

// Enqueuer is the narrow port (*ingest.Pipeline satisfies structurally, SPEC §3.1).
// This is the *only* dependency (besides normalizer/logger), no store access possible.
type Enqueuer interface {
	EnqueueEvents(batch []model.Event) error
}

// hookEventNameProbe extracts echo field for 202 response (SPEC §3.5).
type hookEventNameProbe struct {
	HookEventName string `json:"hook_event_name"`
}

// Handler is POST /ingest/hook (SPEC §3.5). No store dependency by design (structural).
type Handler struct {
	enqueuer     Enqueuer
	normalizer   *normalize.HookNormalizer
	maxBodyBytes int64
	metrics      *Metrics
	logger       *slog.Logger
}

// options collects Option values (standard pattern for Prometheus registerer + logger).
type options struct {
	registerer prometheus.Registerer
	logger     *slog.Logger
}

// Option configures optional Handler dependencies (zero-value production-safe).
type Option func(*options)

// WithRegisterer overrides the Prometheus registerer (tests must use fresh registry).
func WithRegisterer(r prometheus.Registerer) Option {
	return func(o *options) { o.registerer = r }
}

// WithLogger overrides the logger for 500-class internal errors (EnqueueEvents failure).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// NewHandler builds a Handler (maxBodyBytes injected, not from config per ingest convention).
func NewHandler(enqueuer Enqueuer, normalizer *normalize.HookNormalizer, maxBodyBytes int64, opts ...Option) *Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.registerer == nil {
		o.registerer = prometheus.DefaultRegisterer
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return &Handler{
		enqueuer:     enqueuer,
		normalizer:   normalizer,
		maxBodyBytes: maxBodyBytes,
		metrics:      NewMetrics(o.registerer),
		logger:       o.logger,
	}
}

// ServeHTTP implements SPEC §3.5: validate, normalize, enqueue, respond (all paths timed).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() { h.metrics.Duration.Observe(time.Since(start).Seconds()) }()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "payload-too-large",
				"hook payload exceeds the configured ARGUS_INGEST_MAX_BODY_BYTES limit")
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "invalid-body", "failed to read request body")
		return
	}

	// Validate before enqueue to fail fast per SPEC §3.6 (single 400, never partial 202).
	events, err := h.normalizer.FromHookPayload(body)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid-hook-payload", err.Error())
		return
	}

	// Zero-length events is a documented no-op per SPEC §3.6, never an error.
	if err := h.enqueuer.EnqueueEvents(events); err != nil {
		if errors.Is(err, ingest.ErrQueueFull) {
			// Already counted by Pipeline.dropEvents; no counter here (avoid double-counting).
			w.Header().Set("Retry-After", "1")
			writeProblem(w, r, http.StatusTooManyRequests, "queue-full",
				"ingest queue is full; hooks are not retried by Claude Code, so this request's events were dropped")
			return
		}
		// Interface could be implemented differently; fail loudly (global rule: no swallowed errors).
		h.logger.Error("hooks: enqueue failed", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "enqueue-failed", "internal error enqueueing hook event")
		return
	}

	writeAccepted(w, echoedEventName(body))
}

// echoedEventName echoes comma-joined hook_event_names (SPEC §3.5 covers single-object only; batch is passthrough).
func echoedEventName(body []byte) string {
	names, err := rawHookEventNames(body)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.Join(names, ",")
}

// rawHookEventNames extracts hook_event_name; duplicates normalize/hooks logic to avoid cross-package import.
func rawHookEventNames(body []byte) ([]string, error) {
	trimmed := trimLeadingJSONSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var probes []hookEventNameProbe
		if err := json.Unmarshal(body, &probes); err != nil {
			return nil, err
		}
		names := make([]string, len(probes))
		for i, p := range probes {
			names[i] = p.HookEventName
		}
		return names, nil
	}
	var probe hookEventNameProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	return []string{probe.HookEventName}, nil
}

// trimLeadingJSONSpace strips JSON whitespace (RFC 8259 §2); duplicated, not imported.
func trimLeadingJSONSpace(body []byte) []byte {
	i := 0
	for i < len(body) {
		switch body[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return body[i:]
		}
	}
	return body[i:]
}

// problem duplicates internal/httpapi.Problem (RFC 9457) to respect depguard (SPEC §3.1: inward-only).
type problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// problemURNPrefix is the URN prefix for problem types (RFC 9457).
const problemURNPrefix = "urn:argus:error:"

// writeProblem writes an RFC 9457 problem+json response (see problem struct).
func writeProblem(w http.ResponseWriter, r *http.Request, status int, slug, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:     problemURNPrefix + slug,
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	})
}

// acceptedResponse is the SPEC §3.5 202 body (observe-only, no verdict field).
type acceptedResponse struct {
	OK    bool   `json:"ok"`
	Event string `json:"event"`
}

// writeAccepted writes the SPEC §3.5 202 response.
func writeAccepted(w http.ResponseWriter, event string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(acceptedResponse{OK: true, Event: event})
}
