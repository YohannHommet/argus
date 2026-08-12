package hooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/hooks"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// --- test doubles ---

// captureEnqueuer is the "fake pipeline" the AC list asks for: a
// hooks.Enqueuer whose behaviour is entirely test-controlled, with no
// ingest.Pipeline and no database involved at all.
type captureEnqueuer struct {
	mu      sync.Mutex
	batches [][]model.Event
	err     error
}

func (c *captureEnqueuer) EnqueueEvents(batch []model.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.batches = append(c.batches, batch)
	return nil
}

func (c *captureEnqueuer) allEvents() []model.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.Event
	for _, b := range c.batches {
		out = append(out, b...)
	}
	return out
}

// spyWriter is the store.Writer the "zero store calls" AC needs wired into
// a *real* ingest.Pipeline (lead decision #1): the Handler itself never
// receives a store — it only ever sees the narrow hooks.Enqueuer port — so
// the only way to structurally prove "nothing reaches the store
// synchronously during the request" is to sit the spy behind the pipeline
// EnqueueEvents hands off to, and assert it was never called in the same
// instant ServeHTTP returns.
type spyWriter struct {
	mu           sync.Mutex
	batchCalls   int
	metricCalls  int
	writeBatch   func(ctx context.Context, b []model.Event) (store.BatchResult, error)
	writeMetrics func(ctx context.Context, s []model.MetricSample) (store.BatchResult, error)
}

func (s *spyWriter) WriteBatch(ctx context.Context, b []model.Event) (store.BatchResult, error) {
	s.mu.Lock()
	s.batchCalls++
	s.mu.Unlock()
	if s.writeBatch != nil {
		return s.writeBatch(ctx, b)
	}
	refs := make([]model.EventRef, len(b))
	for i, e := range b {
		refs[i] = model.EventRef{TS: e.TS, Seq: int64(i + 1)}
	}
	return store.BatchResult{Written: len(b), EventRefs: refs}, nil
}

func (s *spyWriter) WriteMetrics(ctx context.Context, samples []model.MetricSample) (store.BatchResult, error) {
	s.mu.Lock()
	s.metricCalls++
	s.mu.Unlock()
	if s.writeMetrics != nil {
		return s.writeMetrics(ctx, samples)
	}
	return store.BatchResult{Written: len(samples)}, nil
}

func (s *spyWriter) calls() (batches, metrics int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batchCalls, s.metricCalls
}

// discardLogger swallows every log line so tests that intentionally trigger
// an error-class response don't spam `go test -v` output.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(discardWriter{}, nil)) }

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- helpers ---

func newHookRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/hook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// metricValue reads a counter/gauge straight off the Prometheus wire
// format, the same technique internal/ingest/pipeline_test.go uses instead
// of importing prometheus/client_golang/prometheus/testutil (which pulls in
// a transitive dependency this module's go.sum does not declare).
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var pb dto.Metric
	require.NoError(t, m.Write(&pb))
	switch {
	case pb.Counter != nil:
		return pb.Counter.GetValue()
	case pb.Gauge != nil:
		return pb.Gauge.GetValue()
	default:
		t.Fatal("metricValue: metric is neither a counter nor a gauge")
		return 0
	}
}

const sessionEndPayload = `{"session_id":"sess-1","hook_event_name":"SessionEnd","reason":"clear"}`

// --- AC: a SessionEnd payload returns 202 in under 20ms with a fake pipeline ---

func TestHandler_SessionEnd_Returns202AndIsFast(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	const iterations = 25
	var maxDur time.Duration
	for i := 0; i < iterations; i++ {
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, newHookRequest(t, []byte(sessionEndPayload)))
		d := time.Since(start)
		if d > maxDur {
			maxDur = d
		}
		require.Equal(t, http.StatusAccepted, rec.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, true, resp["ok"])
		require.Equal(t, "SessionEnd", resp["event"])
	}

	require.Len(t, enq.allEvents(), iterations, "one SessionEnd event enqueued per request")

	// SPEC §3.5 targets p99 < 20ms, and the AC says "returns 202 in under
	// 20 ms with a fake pipeline" — so this asserts the SPEC number itself,
	// not a widened one. It is the *max* of 25 in-process calls with no
	// network, no real store and a fake enqueuer, which measures ~0.5-2ms in
	// practice, so 20ms is not a tight fit. t.Logf reports the real figure
	// every run so a regression is visible before it reaches the bound.
	t.Logf("hooks handler: max of %d calls = %s (SPEC §3.5 target: p99 < 20ms)", iterations, maxDur)
	require.Less(t, maxDur, 20*time.Millisecond,
		"handler latency exceeded SPEC §3.5's 20ms budget — the SessionEnd hook shares a hard 1.5s budget, so this is the guard rail")
}

// --- lead decision #3: the duration histogram records a real observation per call ---

func TestHandler_RecordsDurationObservationPerRequest(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newHookRequest(t, []byte(sessionEndPayload)))
		require.Equal(t, http.StatusAccepted, rec.Code)
	}

	// A duplicate-registration panic (if two Handlers ever shared a
	// registry) would fail this test before reaching here — see
	// TestHandler_TwoInstancesDoNotPanicOnDuplicateRegistration below for
	// the direct assertion of that guarantee.
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	require.Len(t, families[0].Metric, 1)
	require.NotNil(t, families[0].Metric[0].Histogram, "expected a histogram metric")
	require.EqualValues(t, 3, families[0].Metric[0].Histogram.GetSampleCount())
}

// TestHandler_TwoInstancesDoNotPanicOnDuplicateRegistration proves lead
// decision #3's registerer-injection requirement: two Handlers built
// against two independent registries in the same test binary must not
// panic, exactly as internal/ingest.NewMetrics already requires of its
// callers.
func TestHandler_TwoInstancesDoNotPanicOnDuplicateRegistration(t *testing.T) {
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	require.NotPanics(t, func() {
		hooks.NewHandler(&captureEnqueuer{}, norm, 1<<20, hooks.WithRegisterer(prometheus.NewRegistry()))
		hooks.NewHandler(&captureEnqueuer{}, norm, 1<<20, hooks.WithRegisterer(prometheus.NewRegistry()))
	})
}

// --- AC: missing session_id -> 400 problem+json ---

func TestHandler_MissingSessionID_Returns400ProblemJSON(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	payload := []byte(`{"hook_event_name":"SessionEnd"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, payload))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	var problem map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, "urn:argus:error:invalid-hook-payload", problem["type"])
	require.InDelta(t, float64(http.StatusBadRequest), problem["status"].(float64), 0)
	require.Empty(t, enq.allEvents(), "an invalid payload must never occupy queue capacity (SPEC §3.6)")
}

// --- AC: MessageDisplay gated by default still yields 202 (zero events is not an error) ---

func TestHandler_MessageDisplayGatedByDefault_StillReturns202(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false) // AllowMessageDisplay=false
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	payload := []byte(`{"session_id":"sess-1","hook_event_name":"MessageDisplay","message":"hi"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, true, resp["ok"])
	require.Equal(t, "MessageDisplay", resp["event"], "hook_event_name is echoed verbatim even when the event itself was gated out")
	require.Empty(t, enq.allEvents(), "MessageDisplay dropped by the default gate must enqueue zero events, not error")
}

// --- AC: an array of 5 payloads enqueues 5 events ---

func TestHandler_ArrayOfFivePayloads_Enqueues5Events(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	names := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "SessionEnd"}
	elems := make([]string, len(names))
	for i, n := range names {
		elems[i] = `{"session_id":"sess-1","hook_event_name":"` + n + `"}`
	}
	payload := []byte("[" + strings.Join(elems, ",") + "]")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, enq.allEvents(), 5)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, true, resp["ok"])
	// Batch case: no single "the" hook_event_name, so every element's raw
	// name is echoed, comma-joined, in submission order (handler.go's
	// echoedEventName doc).
	require.Equal(t, strings.Join(names, ","), resp["event"])
}

// --- AC: mixed-array (one invalid element) fails whole, as one 400 ---

func TestHandler_ArrayWithOneMissingSessionID_FailsWholeRequest(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	payload := []byte(`[
		{"session_id":"sess-1","hook_event_name":"PreToolUse"},
		{"hook_event_name":"PostToolUse"}
	]`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, payload))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, enq.allEvents(), "no partial 202: one invalid element rejects the whole batch")
}

// --- AC: response body never contains a decision/permission field (observe-only) ---

func TestHandler_ResponseNeverContainsADecisionOrPermissionField(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	// PreToolUse is exactly the hook_event_name a real permission decision
	// would attach to (SPEC §1.5.2) — this is the payload most likely to
	// tempt a future change into adding a decision-shaped response field.
	payload := []byte(`{"session_id":"sess-1","hook_event_name":"PreToolUse","tool_name":"Bash","permission_mode":"default"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 2, "the 202 body must contain exactly {ok, event} and nothing else")
	require.Contains(t, resp, "ok")
	require.Contains(t, resp, "event")
	require.NotContains(t, resp, "decision")
	require.NotContains(t, resp, "permission")
	require.NotContains(t, resp, "permission_decision")
	require.NotContains(t, resp, "block")
}

// --- AC: a 2 MiB body over a small configured cap -> 413 ---

func TestHandler_BodyOverCap_Returns413(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	const cap13 = 1024 // small injected cap (lead decision #5): the test needs no 8MiB default
	h := hooks.NewHandler(enq, norm, cap13, hooks.WithRegisterer(reg))

	// A 2MiB body, matching the ticket's literal AC wording, against the
	// small 1KiB cap configured above.
	padding := strings.Repeat("x", 2<<20)
	payload := []byte(`{"session_id":"sess-1","hook_event_name":"SessionEnd","note":"` + padding + `"}`)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, payload))

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Empty(t, enq.allEvents())
}

// --- AC: queue full -> 429 + Retry-After, and the drop counter is incremented ---
//
// This wires the real ingest.Pipeline (not captureEnqueuer) behind the
// handler, because the drop counter the AC asks about
// (argus_ingest_dropped_total{source="hook"}) lives inside
// internal/ingest.Pipeline.dropEvents, not in this package — see handler.go's
// EnqueueEvents error-path comment for why this handler adds no counter of
// its own. The blocking-writer / require.Eventually technique mirrors
// internal/ingest/pipeline_test.go's own
// TestEnqueueEvents_QueueFull_ReturnsErrQueueFullWithoutBlocking exactly,
// so queue-fullness here is deterministic, not timing-dependent.
func TestHandler_QueueFull_Returns429WithRetryAfterAndDropCounter(t *testing.T) {
	block := make(chan struct{})
	sw := &spyWriter{writeBatch: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		<-block // never returns during this test: ties up the sole worker permanently
		return store.BatchResult{}, nil
	}}
	pipelineReg := prometheus.NewRegistry()
	p := ingest.New(sw, ingest.PipelineConfig{QueueCap: 1, Workers: 1, BatchSize: 1, FlushInterval: time.Hour},
		ingest.WithRegisterer(pipelineReg), ingest.WithLogger(discardLogger()))
	// Defers run LIFO: close(block) must run BEFORE Close, or Close would
	// block forever waiting for a worker that will never return (mirrors
	// internal/ingest/pipeline_test.go's identical fixture).
	defer func() { _ = p.Close(context.Background()) }()
	defer close(block)

	reg := prometheus.NewRegistry()
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(p, norm, 1<<20, hooks.WithRegisterer(reg), hooks.WithLogger(discardLogger()))

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newHookRequest(t, []byte(sessionEndPayload)))
		return rec
	}

	// Request 1: the sole worker takes it off the channel immediately and,
	// since BatchSize=1, flushes right away -- which blocks forever on
	// `block`. The channel is empty again but nothing will ever drain it
	// further.
	rec1 := post()
	require.Equal(t, http.StatusAccepted, rec1.Code)
	require.Eventually(t, func() bool { calls, _ := sw.calls(); return calls >= 1 }, time.Second, 2*time.Millisecond)

	// Request 2: fills the 1-slot channel, since the worker is stuck.
	rec2 := post()
	require.Equal(t, http.StatusAccepted, rec2.Code)

	// Request 3: the queue is now genuinely full and permanently so.
	rec3 := post()
	require.Equal(t, http.StatusTooManyRequests, rec3.Code)
	require.Equal(t, "1", rec3.Header().Get("Retry-After"))
	require.Equal(t, "application/problem+json", rec3.Header().Get("Content-Type"))

	require.InDelta(t, float64(1), metricValue(t, p.Metrics().Dropped.WithLabelValues(string(model.SourceHook))), 0,
		"argus_ingest_dropped_total{source=\"hook\"} must be incremented on queue-full (SPEC §3.5)")
}

// --- AC: the handler makes zero store calls (a store-spy proves it) ---
//
// See handler.go's Enqueuer doc and this file's spyWriter doc for what this
// actually proves: the Handler's only dependency is the narrow Enqueuer
// port, which has no store-shaped method at all, so "zero store calls" is
// true by construction; this test additionally wires the spy behind a real
// ingest.Pipeline, configured so its flush conditions (BatchSize,
// FlushInterval) cannot fire during the test's lifetime, and asserts zero
// calls immediately after ServeHTTP returns -- deterministically, not by
// racing a background flush.
func TestHandler_MakesZeroStoreCallsEvenThroughARealPipeline(t *testing.T) {
	sw := &spyWriter{}
	p := ingest.New(sw, ingest.PipelineConfig{
		QueueCap:      16,
		Workers:       1,
		BatchSize:     10_000, // structurally unreachable within this test
		FlushInterval: time.Hour,
	}, ingest.WithRegisterer(prometheus.NewRegistry()), ingest.WithLogger(discardLogger()))
	defer func() { _ = p.Close(context.Background()) }()

	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(p, norm, 1<<20, hooks.WithRegisterer(prometheus.NewRegistry()))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newHookRequest(t, []byte(sessionEndPayload)))
	require.Equal(t, http.StatusAccepted, rec.Code)

	batches, metrics := sw.calls()
	require.Zero(t, batches, "the request handler must never cause a synchronous WriteBatch call")
	require.Zero(t, metrics, "the request handler must never cause a synchronous WriteMetrics call")
}

// --- Mount wiring: the real seam a chi.Router mounts, including auth ---

func TestMounter_Mount_AttachesRouteWithAuthMiddleware(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	const token = "s3cret"
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	m := hooks.NewMounter(h, auth)
	r := chi.NewRouter()
	m.Mount(r)

	// No token -> the auth middleware, not the handler, answers.
	unauth := httptest.NewRecorder()
	r.ServeHTTP(unauth, newHookRequest(t, []byte(sessionEndPayload)))
	require.Equal(t, http.StatusUnauthorized, unauth.Code)
	require.Empty(t, enq.allEvents())

	// Correct bearer token -> request reaches the real handler.
	authed := httptest.NewRecorder()
	req := newHookRequest(t, []byte(sessionEndPayload))
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(authed, req)
	require.Equal(t, http.StatusAccepted, authed.Code)
	require.Len(t, enq.allEvents(), 1)
}

// A nil auth (SPEC §3.5: ARGUS_INGEST_TOKEN unset by default) mounts the
// handler unwrapped, matching RequireIngestToken's own no-op-when-empty
// behaviour.
func TestMounter_Mount_NilAuthIsANoOp(t *testing.T) {
	reg := prometheus.NewRegistry()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	h := hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(reg))

	m := hooks.NewMounter(h, nil)
	r := chi.NewRouter()
	m.Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newHookRequest(t, []byte(sessionEndPayload)))
	require.Equal(t, http.StatusAccepted, rec.Code)
}
