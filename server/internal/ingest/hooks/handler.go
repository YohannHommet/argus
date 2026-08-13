// Package hooks implements ticket P2-11: the Claude Code hooks webhook,
// `POST /ingest/hook` (docs/SPEC.md §3.5). It is a leaf receiver package
// exactly like a future P2-10 OTLP receiver would be: it depends on
// internal/ingest/normalize (pure, in-request decoding) and on a narrow,
// consumer-owned Enqueuer port it declares itself, never on the concrete
// internal/ingest.Pipeline type and never on internal/httpapi (depguard,
// SPEC §3.1 — ingest may not import httpapi or query). internal/app wires
// this package's Handler/Mounter into httpapi.Deps.HookMounter, the mount
// seam router.go already exposes.
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

// Metrics is this package's self-observability surface: just the one
// histogram SPEC §3.5 names as the guard rail for the handler's <20ms p99
// budget. It is its own type (rather than a field bolted onto
// internal/ingest.Metrics) because that struct belongs to P2-09's file and
// this ticket's file-ownership split keeps this package's Files list to
// exactly {handler.go, mount.go, handler_test.go} — a fourth metrics.go
// would need no reason to exist for one histogram.
type Metrics struct {
	// Duration observes ServeHTTP's total wall time for every request,
	// success or error, so p99 reflects what a real SessionEnd hook call
	// actually costs against its 1.5s shared budget (SPEC §3.5).
	Duration prometheus.Histogram
}

// NewMetrics registers Metrics against reg (nil = prometheus.
// DefaultRegisterer), mirroring internal/ingest.NewMetrics's nil-safe
// convention. Buckets are sub-millisecond-to-1s, an order of magnitude
// finer than internal/ingest's write-path prometheus.DefBuckets, because
// this histogram's entire purpose is resolving a <20ms target — DefBuckets'
// coarsest low bucket (5ms) would leave almost every observation in one or
// two buckets.
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

// Enqueuer is the narrow, consumer-owned port this package needs from
// internal/ingest.Pipeline (SPEC §3.1's dependency-inversion rule: the
// receiver depends on an interface it declares, not the concrete type it
// happens to be wired to today). *ingest.Pipeline satisfies this
// structurally. Declaring it here — rather than depending on
// ingest.Pipeline directly — is what let the AC's "fake pipeline" test
// double exist at all, and it is also the reason the handler has no way to
// reach a database: this is the *only* dependency the handler has besides a
// normalizer and a logger, and it has no method that touches storage.
type Enqueuer interface {
	EnqueueEvents(batch []model.Event) error
}

// hookEventNameProbe extracts just the one field the 202 response body
// echoes back (SPEC §3.5's `{"ok":true,"event":"<hook_event_name>"}`).
// It is decoded separately from normalize.HookNormalizer.FromHookPayload
// rather than reading EventName off the returned []model.Event, because a
// `MessageDisplay` payload gated by ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY
// (normalize/hooks.go) yields zero events on a perfectly valid, 202-worthy
// request — the response must still be able to name the event that
// happened. hook_event_name is unconstrained vendor text (SPEC §0): this
// probe never validates or rejects it, only echoes it.
type hookEventNameProbe struct {
	HookEventName string `json:"hook_event_name"`
}

// Handler is the `POST /ingest/hook` HTTP handler (SPEC §3.5). It holds no
// store dependency of any kind — that absence is structural, not
// incidental: the AC "handler makes zero store calls" is true because there
// is no field here a store call could be made through, not because the
// code merely happens not to call one.
type Handler struct {
	enqueuer     Enqueuer
	normalizer   *normalize.HookNormalizer
	maxBodyBytes int64
	metrics      *Metrics
	logger       *slog.Logger
}

// options collects Option values before Handler construction, mirroring
// internal/ingest.options/Option — the established house pattern for
// injectable Prometheus registerer + logger (internal/ingest/pipeline.go).
type options struct {
	registerer prometheus.Registerer
	logger     *slog.Logger
}

// Option configures optional Handler dependencies. The zero value of every
// option is production-safe: NewHandler defaults registerer to
// prometheus.DefaultRegisterer and logger to slog.Default().
type Option func(*options)

// WithRegisterer overrides the Prometheus registerer Metrics registers
// against (lead decision #3: two Handlers in one test binary must each get
// a fresh prometheus.NewRegistry(), since the package default,
// prometheus.DefaultRegisterer, is a process-global that panics on a
// duplicate metric name).
func WithRegisterer(r prometheus.Registerer) Option {
	return func(o *options) { o.registerer = r }
}

// WithLogger overrides the *slog.Logger used for 500-class internal errors
// (an EnqueueEvents failure other than ingest.ErrQueueFull, which SPEC's
// design says should never happen against the real Pipeline but which the
// Enqueuer port cannot rule out for an arbitrary implementation).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// NewHandler builds a Handler. maxBodyBytes is ARGUS_INGEST_MAX_BODY_BYTES
// (SPEC §3.7), injected rather than read from config directly (this
// package must not import internal/config, matching every other ingest
// receiver's config-free-at-the-leaf convention) — tests set it small so
// the 413 AC doesn't need an 8 MiB payload (lead decision #5).
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

// ServeHTTP implements SPEC §3.5 end to end: cap the body, normalize
// in-request (never touching the database), enqueue non-blockingly, and
// respond. Every exit path — success, 400, 413, 429 — is timed by
// argus_hook_handler_duration_seconds, since the SPEC's <20ms p99 budget
// covers the whole handler, not just the happy path.
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

	// Normalization is the whole of SPEC §3.5's validation ("validate
	// session_id, compute the dedup key") and it happens before any
	// enqueue attempt, per SPEC §3.6: "fails fast with a 400 and never
	// occupies queue capacity". A missing/empty session_id, or one invalid
	// element inside a batch array, is the only error FromHookPayload
	// returns (normalize/hooks.go) — surfaced here as a single 400 for the
	// whole request, never a partial 202.
	events, err := h.normalizer.FromHookPayload(body)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid-hook-payload", err.Error())
		return
	}

	// EnqueueEvents is a non-blocking, in-memory handoff (SPEC §3.6) — the
	// last thing this handler does before responding, and the only thing
	// it does that leaves this package. A zero-length events slice (every
	// element gated out, e.g. an all-MessageDisplay payload under the
	// default config) is a documented EnqueueEvents no-op, never an error.
	if err := h.enqueuer.EnqueueEvents(events); err != nil {
		if errors.Is(err, ingest.ErrQueueFull) {
			// SPEC §3.5: "Claude Code does not retry hooks, so this is
			// counted data loss" — already counted by
			// argus_ingest_dropped_total{source="hook"} inside
			// Pipeline.dropEvents (internal/ingest/pipeline.go) before
			// ErrQueueFull is even returned here, so this handler adds no
			// counter of its own (see the report for why: double-counting
			// would make the health-strip number lie).
			w.Header().Set("Retry-After", "1")
			writeProblem(w, r, http.StatusTooManyRequests, "queue-full",
				"ingest queue is full; hooks are not retried by Claude Code, so this request's events were dropped")
			return
		}
		// Unreachable against the real Pipeline (its only non-nil
		// EnqueueEvents error is ErrQueueFull), but the Enqueuer port is an
		// interface an arbitrary caller could implement differently — fail
		// loudly rather than silently swallow an unexpected error (global
		// rule: no swallowed errors).
		h.logger.Error("hooks: enqueue failed", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "enqueue-failed", "internal error enqueueing hook event")
		return
	}

	writeAccepted(w, echoedEventName(body))
}

// echoedEventName computes the `event` field of the 202 response body.
// SPEC §3.5's wire example (`{"ok":true,"event":"<hook_event_name>"}`) is
// written for the single-object case Claude Code always sends; for the
// argus-sim batch-replay array case there is no single "the" event, so this
// echoes every element's raw hook_event_name, comma-joined, in submission
// order — full passthrough (SPEC §0: every vendor-supplied string is
// unconstrained) rather than picking one element and discarding the rest of
// the batch's identity. A single-object body therefore always yields
// exactly `<hook_event_name>`, matching the SPEC example byte for byte; an
// N-element array yields N comma-joined names. Decode failure here (should
// be unreachable: FromHookPayload already proved body decodes) degrades to
// an empty string rather than an error, since by this point the request has
// already been accepted and must not fail on a response-cosmetics path.
func echoedEventName(body []byte) string {
	names, err := rawHookEventNames(body)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.Join(names, ",")
}

// rawHookEventNames mirrors normalize/hooks.go's splitHookPayload sniff (an
// array iff the first non-whitespace byte is `[`) rather than importing an
// unexported helper from that package — this package intentionally reads
// only the one field it needs (hook_event_name) via encoding/json's own
// permissive decoding, ignoring every other key.
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

// trimLeadingJSONSpace strips the JSON whitespace characters (RFC 8259 §2),
// duplicated from normalize/hooks.go's unexported helper of the same name
// rather than imported, since that package exports no such helper and this
// one is three lines.
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

// problem is a duplicate of internal/httpapi.Problem (RFC 9457
// problem+json, SPEC §4.1) — same field set, same JSON tags, same
// "urn:argus:error:<slug>" type scheme and Content-Type. It is
// deliberately re-declared here rather than imported: depguard forbids
// internal/ingest importing internal/httpapi (SPEC §3.1's inward-only
// dependency direction — httpapi.RequireIngestToken already has to arrive
// as a plain func(http.Handler) http.Handler for the same reason, see
// mount.go). Duplicating four struct fields and one helper function is
// cheaper than the alternative of promoting problem+json into a third,
// lower package both sides would depend on, for a wire shape unlikely to
// change independently in the two places.
type problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// problemURNPrefix mirrors internal/httpapi's constant of the same name.
const problemURNPrefix = "urn:argus:error:"

// writeProblem writes an RFC 9457 problem+json response, matching
// internal/httpapi.writeProblem's wire shape exactly (see problem's doc).
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

// acceptedResponse is the SPEC §3.5 202 body. Deliberately two fields only:
// Argus is observe-only (SPEC §3.5's closing rule, and the ticket's own
// AC), so there is no field here — and never will be — for a hook
// decision, permission, or blocking verdict.
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
