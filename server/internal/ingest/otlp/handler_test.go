package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// fixedNow freezes the clock every test uses, mirroring
// internal/ingest/normalize's own test convention (otel_logs_test.go), so
// IngestedAt/clamp behaviour is deterministic and the two-wire-format
// "byte-identical events" comparison (TestHandleLogs_ProtobufAndJSONAgree)
// never flakes on a timestamp.
var fixedNow = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

const testRetention = 90 * 24 * time.Hour
const testMaxBodyBytes = 4 << 20 // 4 MiB, small enough for fast oversized-body tests

func newTestNormalizer() *normalize.Normalizer {
	return normalize.NewNormalizer(func() time.Time { return fixedNow }, testRetention)
}

// fakeEnqueuer is the Enqueuer test double every handler test constructs a
// Handler with: it records every batch handed to it (so an AC like "2
// events enqueued" is assertable) and, when err is set, fails exactly like
// ingest.Pipeline does on backpressure (ingest.ErrQueueFull).
type fakeEnqueuer struct {
	mu      sync.Mutex
	events  []model.Event
	samples []model.MetricSample
	err     error
}

func (f *fakeEnqueuer) EnqueueEvents(batch []model.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, batch...)
	return nil
}

func (f *fakeEnqueuer) EnqueueMetrics(batch []model.MetricSample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.samples = append(f.samples, batch...)
	return nil
}

func (f *fakeEnqueuer) eventCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// newTestHandler builds a Handler wired to a fresh fakeEnqueuer and a fresh
// prometheus.NewRegistry() (never the process-global DefaultRegisterer, per
// lead note 5 — otherwise two Handlers in this one test binary would panic
// on a duplicate "argus_otlp_traces_discarded_total" registration).
func newTestHandler(t *testing.T, auth func(http.Handler) http.Handler) (*Handler, *fakeEnqueuer) {
	t.Helper()
	fe := &fakeEnqueuer{}
	h := New(fe, newTestNormalizer(), testMaxBodyBytes, auth, nil, prometheus.NewRegistry())
	return h, fe
}

// newTestServer mounts h on a fresh chi.Router and serves it over
// httptest.NewServer (a random port, never :8080 — the host's port 8080 is
// occupied by an unrelated stack per this ticket's environment notes).
//
// This is the ticket's "end-to-end-ish test through httpapi.New" (lead note
// 6), adapted to what this package may actually import: depguard (SPEC
// §3.1, internal/ingest/**) forbids internal/ingest/otlp — including this
// _test.go file, which is not exempt from the glob — from importing
// internal/httpapi at all, so a literal httpapi.New(...) call cannot live
// here. Routing through chi.Router + h.Mount, exactly as internal/app will
// do when it wires httpapi.Deps.OTLPMounter, exercises the identical mount
// seam, auth middleware, and HTTP round trip; only the surrounding
// ops/API/UI routes httpapi.New also attaches are absent. A true
// httpapi.New-based integration belongs in internal/app (which may import
// both httpapi and this package) — outside this ticket's file ownership.
func newTestServer(t *testing.T, h *Handler) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// --- fixture builders ---------------------------------------------------

func strAttr(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

// mkLogsData builds one LogsData with a single ResourceLogs/ScopeLogs
// containing recs, wire-shape-identical to what a real
// ExportLogsServiceRequest carries (both messages define the same field 1,
// resource_logs/resourceLogs) — see codec.go's decodeExportRequest doc for
// why this equivalence is exploited throughout this file instead of a
// collector-package type.
func mkLogsData(recs ...*logspb.LogRecord) *logspb.LogsData {
	return &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
		}},
	}
}

func mkLogRecord(eventName, sessionID string, extra ...*commonpb.KeyValue) *logspb.LogRecord {
	attrs := append([]*commonpb.KeyValue{strAttr("session.id", sessionID)}, extra...)
	return &logspb.LogRecord{
		TimeUnixNano: uint64(fixedNow.UnixNano()),
		EventName:    eventName,
		Attributes:   attrs,
	}
}

// httpResult is doRequest's return shape: the response fully drained and
// closed before doRequest returns, so no caller ever needs to (and
// bodyclose has nothing to flag) — a cleaner fit for these tests than
// handing back a live *http.Response none of them stream from.
type httpResult struct {
	status int
	header http.Header
	body   []byte
}

func doRequest(t *testing.T, srv *httptest.Server, path, contentType string, body []byte, headers map[string]string) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return httpResult{status: resp.StatusCode, header: resp.Header, body: respBody}
}

// decodeStatus reverse-engineers the google.rpc.Status-shaped body
// writeStatus produces (statusMessage/statusJSON), for assertions — see
// codec.go's writeStatus doc for why there is no generated Go type to
// unmarshal into instead.
func decodeStatus(t *testing.T, format string, body []byte) (code int32, message string) {
	t.Helper()
	if format == contentTypeJSON {
		var s statusJSON
		require.NoError(t, json.Unmarshal(body, &s))
		return s.Code, s.Message
	}
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		require.Positive(t, n)
		body = body[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(body)
			require.Positive(t, n)
			body = body[n:]
			code = int32(v) //nolint:gosec // test-only decode of a value this file itself encoded as int32
		case num == 2 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(body)
			require.Positive(t, n)
			body = body[n:]
			message = string(v)
		default:
			n := protowire.ConsumeFieldValue(num, typ, body)
			require.Positive(t, n)
			body = body[n:]
		}
	}
	return code, message
}

// --- AC: byte-identical normalized events across wire formats -----------

// TestHandleLogs_ProtobufAndJSONAgree covers the AC "a protobuf
// ExportLogsServiceRequest and the equivalent JSON body produce
// byte-identical normalized events": the same in-memory LogsData is
// marshalled two ways and posted to two independently-constructed Handlers
// (same frozen clock), and the events each one handed to its Enqueuer must
// be equal.
func TestHandleLogs_ProtobufAndJSONAgree(t *testing.T) {
	t.Parallel()

	data := mkLogsData(mkLogRecord("user_prompt", "session-abc", strAttr("prompt.id", "prompt-1")))

	protoBody, err := proto.Marshal(data)
	require.NoError(t, err)
	jsonBody, err := protojson.Marshal(data)
	require.NoError(t, err)

	hProto, feProto := newTestHandler(t, nil)
	srvProto := newTestServer(t, hProto)
	respProto := doRequest(t, srvProto, "/v1/logs", contentTypeProtobuf, protoBody, nil)
	require.Equal(t, http.StatusOK, respProto.status)

	hJSON, feJSON := newTestHandler(t, nil)
	srvJSON := newTestServer(t, hJSON)
	respJSON := doRequest(t, srvJSON, "/v1/logs", contentTypeJSON, jsonBody, nil)
	require.Equal(t, http.StatusOK, respJSON.status)

	require.Len(t, feProto.events, 1)
	require.Len(t, feJSON.events, 1)
	require.Equal(t, feProto.events[0], feJSON.events[0], "protobuf- and JSON-decoded events must be identical")
	require.Equal(t, "session-abc", feProto.events[0].SessionID)
}

// TestHandleLogs_ResponseUnmarshalsAsExportLogsServiceResponse covers the AC
// "the response unmarshals as ExportLogsServiceResponse": full success ->
// zero-length protobuf body (a fully-default/empty message, per protobuf's
// own encoding rules) and "{}" JSON with no partial_success key.
func TestHandleLogs_ResponseUnmarshalsAsExportLogsServiceResponse(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	data := mkLogsData(mkLogRecord("user_prompt", "session-xyz"))
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusOK, resp.status)
	require.Equal(t, contentTypeProtobuf, resp.header.Get("Content-Type"))
	require.Empty(t, resp.body, "full-success ExportLogsServiceResponse must be the empty message")

	jsonResp := doRequest(t, srv, "/v1/logs", contentTypeJSON, mustJSON(t, data), nil)
	require.Equal(t, http.StatusOK, jsonResp.status)
	require.JSONEq(t, `{}`, string(jsonResp.body))
}

// --- AC: partial_success on a session-less record ------------------------

// TestHandleLogs_PartialSuccessOnMissingSessionID covers the AC "3 records
// where 1 lacks session.id -> 200 with partial_success.rejected_log_records
// = 1 and 2 events enqueued".
func TestHandleLogs_PartialSuccessOnMissingSessionID(t *testing.T) {
	t.Parallel()

	h, fe := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	noSession := &logspb.LogRecord{
		TimeUnixNano: uint64(fixedNow.UnixNano()),
		EventName:    "user_prompt",
		// deliberately no session.id attribute
	}
	data := mkLogsData(
		mkLogRecord("user_prompt", "session-1"),
		noSession,
		mkLogRecord("user_prompt", "session-2"),
	)
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusOK, resp.status)

	rejected, _ := decodePartialSuccess(t, resp.body)
	require.Equal(t, int64(1), rejected)
	require.Equal(t, 2, fe.eventCount())
}

// --- AC: oversized body -> 413 -------------------------------------------

// TestHandleLogs_OversizedBody covers the AC "a 20 MiB body -> 413".
func TestHandleLogs_OversizedBody(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	oversized := make([]byte, 20<<20)
	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, oversized, nil)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.status)
}

// --- AC: gzip bomb -> 413 with bounded memory -----------------------------

// buildGzipBomb gzip-compresses decompressedSize zero bytes at
// gzip.BestCompression, producing a payload whose compressed size stays
// tiny relative to decompressedSize no matter how large decompressedSize is
// — the point of the test below. A single large all-zero input (rather than
// many small Write calls) keeps fixture construction itself fast; this
// allocation happens once in the test process to build the wire payload,
// distinct from what the server under test is allowed to allocate decoding
// it.
func buildGzipBomb(t *testing.T, decompressedSize int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	require.NoError(t, err)
	_, err = gz.Write(make([]byte, decompressedSize))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// TestHandleLogs_GzipBomb covers the AC "a gzip bomb decompressing past the
// cap -> 413 with bounded memory": lead note 2 requires proving *both*
// halves. The 413 half is a direct status assertion; the bounded-memory
// half is proven by measuring live heap growth across the request and
// asserting it stays orders of magnitude below the payload's nominal
// decompressed size (readBody's io.LimitReader caps actual decompression to
// maxBodyBytes+1 bytes — see codec.go's readBody doc — so heap growth
// should track that cap, not decompressedSize).
//
// m17 (major): this asserts runtime.MemStats.TotalAlloc, a monotonically
// increasing counter of every byte ever allocated, rather than HeapAlloc (the
// *live* heap at the last GC). HeapAlloc is vacuous here for two independent
// reasons the finding calls out: (1) an implementation that fully
// decompressed the bomb and then dropped every reference to it before the
// post-request runtime.GC() would show no HeapAlloc growth at all — GC
// reclaims exactly what was allocated-then-freed, so a bug that allocates and
// discards 32 MiB is invisible to a live-heap comparison; (2) the `if after >
// before` guard silently skips the assertion entirely whenever GC happens to
// net the heap smaller across the request (unrelated background allocation
// freed, e.g.), which is exactly the "assertion never runs" failure mode a
// flaky/no-op check produces. TotalAlloc has neither problem: it only grows,
// never shrinks, so a decompressed-then-freed bomb still shows up, and the
// comparison is safe unconditionally. t.Parallel() is deliberately dropped
// (unlike every other test in this file) because a global allocation counter
// is not a safe measurement while sibling subtests are concurrently
// allocating on other goroutines.
func TestHandleLogs_GzipBomb(t *testing.T) {
	const maxBodyBytes = 64 * 1024    // small and distinct from testMaxBodyBytes, to keep the bomb setup itself fast
	const decompressedSize = 32 << 20 // 32 MiB nominal (500x maxBodyBytes), compresses to well under 1 MiB of all-zero runs

	fe := &fakeEnqueuer{}
	h := New(fe, newTestNormalizer(), maxBodyBytes, nil, nil, prometheus.NewRegistry())
	srv := newTestServer(t, h)

	bomb := buildGzipBomb(t, decompressedSize)
	require.Less(t, len(bomb), decompressedSize/64, "the whole point of a gzip bomb is a compressed size far smaller than its decompressed size")

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, bomb, map[string]string{"Content-Encoding": "gzip"})
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.status)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// A buggy implementation that fully decompressed the bomb before
	// checking its size would push TotalAlloc up by roughly decompressedSize
	// (32 MiB), on top of whatever the request's other allocations cost.
	// Bounded decompression should stay within a few MiB of maxBodyBytes;
	// 8 MiB gives ample slack over the observed ~450-504 KiB baseline
	// (5 runs with -race -count=5, see report) while still failing loudly —
	// comfortably below the 32 MiB a full decompression would add — if the
	// cap stopped being enforced. Unlike HeapAlloc, TotalAlloc only grows, so
	// this comparison needs no "if it grew at all" guard.
	const totalAllocGrowthCeiling = 8 << 20
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(totalAllocGrowthCeiling),
		"decompressing the gzip bomb allocated far more than the configured cap")
}

// --- AC: malformed protobuf -> 400 with a Status body ---------------------

// TestHandleLogs_MalformedProtobuf covers the AC "malformed protobuf -> 400
// with an application/x-protobuf Status".
func TestHandleLogs_MalformedProtobuf(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	garbage := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, garbage, nil)
	require.Equal(t, http.StatusBadRequest, resp.status)
	require.Equal(t, contentTypeProtobuf, resp.header.Get("Content-Type"))

	code, message := decodeStatus(t, contentTypeProtobuf, resp.body)
	require.Equal(t, grpcCodeInvalidArgument, code)
	require.NotEmpty(t, message)
}

// TestHandleLogs_MalformedProtobuf_InnerElement covers the
// decodeExportRequestProto branch a top-level-only garbage payload cannot
// reach: a syntactically valid envelope (field 1, correctly length-prefixed)
// whose *inner* ResourceLogs bytes are themselves malformed.
func TestHandleLogs_MalformedProtobuf_InnerElement(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	inner := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	envelope := protowire.AppendTag(nil, 1, protowire.BytesType)
	envelope = protowire.AppendBytes(envelope, inner)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, envelope, nil)
	require.Equal(t, http.StatusBadRequest, resp.status)
}

// TestHandleLogs_MalformedJSON covers the JSON half of "malformed body ->
// 400 with a Status", both for writeStatus's JSON branch and
// decodeExportRequestJSON's top-level json.Unmarshal error path.
func TestHandleLogs_MalformedJSON(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	resp := doRequest(t, srv, "/v1/logs", contentTypeJSON, []byte(`{not valid json`), nil)
	require.Equal(t, http.StatusBadRequest, resp.status)
	require.Equal(t, "application/json", resp.header.Get("Content-Type"))

	code, message := decodeStatus(t, contentTypeJSON, resp.body)
	require.Equal(t, grpcCodeInvalidArgument, code)
	require.NotEmpty(t, message)
}

// TestHandleLogs_MalformedJSON_InnerElement covers
// decodeExportRequestJSON's per-element protojson.Unmarshal error path: the
// envelope itself is valid JSON, but one element has a field of the wrong
// JSON type for its proto definition.
func TestHandleLogs_MalformedJSON_InnerElement(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	resp := doRequest(t, srv, "/v1/logs", contentTypeJSON, []byte(`{"resourceLogs":[{"scopeLogs":"not-an-array"}]}`), nil)
	require.Equal(t, http.StatusBadRequest, resp.status)
}

// TestHandleLogs_JSONSnakeCaseEnvelopeKey covers decodeExportRequestJSON's
// fallback to the proto field's original snake_case name (snakeCase),
// mirroring protojson's own leniency about accepting either spelling.
func TestHandleLogs_JSONSnakeCaseEnvelopeKey(t *testing.T) {
	t.Parallel()

	h, fe := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	data := mkLogsData(mkLogRecord("user_prompt", "session-snake"))
	camel, err := protojson.Marshal(data)
	require.NoError(t, err)

	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(camel, &envelope))
	snakeBody, err := json.Marshal(map[string]json.RawMessage{"resource_logs": envelope["resourceLogs"]})
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/logs", contentTypeJSON, snakeBody, nil)
	require.Equal(t, http.StatusOK, resp.status)
	require.Len(t, fe.events, 1)
	require.Equal(t, "session-snake", fe.events[0].SessionID)
}

// TestHandleLogs_EnvelopeLevelUnknownField covers decodeExportRequestProto's
// "skip a top-level field other than 1" branch: a field number the envelope
// message does not define, alongside the real resource_logs field.
func TestHandleLogs_EnvelopeLevelUnknownField(t *testing.T) {
	t.Parallel()

	h, fe := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	data := mkLogsData(mkLogRecord("user_prompt", "session-envelope-unknown"))
	realField, err := proto.Marshal(data)
	require.NoError(t, err)

	extra := protowire.AppendTag(nil, 99, protowire.VarintType)
	extra = protowire.AppendVarint(extra, 7)
	extra = append(extra, realField...)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, extra, nil)
	require.Equal(t, http.StatusOK, resp.status)
	require.Len(t, fe.events, 1)
	require.Equal(t, "session-envelope-unknown", fe.events[0].SessionID)
}

// --- AC: /v1/traces accepts and discards ----------------------------------

// TestHandleTraces_AcceptAndDiscard covers the AC "/v1/traces -> 200 empty +
// discard counter".
func TestHandleTraces_AcceptAndDiscard(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	fe := &fakeEnqueuer{}
	h := New(fe, newTestNormalizer(), testMaxBodyBytes, nil, nil, reg)
	srv := newTestServer(t, h)

	data := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{TraceId: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}, {TraceId: []byte{1}}},
			}},
		}},
	}
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/traces", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusOK, resp.status)
	require.Empty(t, resp.body, "ExportTraceServiceResponse must be the empty message")

	require.InDelta(t, float64(2), metricValue(t, h.metrics.TracesDiscarded), 0)
	require.Zero(t, fe.eventCount(), "traces must never reach the event lane")
}

// TestHandleTraces_MalformedProtobuf covers handleTraces' decode-error
// branch: /v1/traces applies the same content-negotiation/decode contract
// as /v1/logs and /v1/metrics (handleTraces' doc), including on malformed
// input.
func TestHandleTraces_MalformedProtobuf(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	resp := doRequest(t, srv, "/v1/traces", contentTypeProtobuf, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, nil)
	require.Equal(t, http.StatusBadRequest, resp.status)
}

// --- AC: queue-full -> 503 with Retry-After: 1 ----------------------------

// TestHandleLogs_QueueFull covers the AC "queue-full -> 503 with
// Retry-After: 1".
func TestHandleLogs_QueueFull(t *testing.T) {
	t.Parallel()

	fe := &fakeEnqueuer{err: ingest.ErrQueueFull}
	h := New(fe, newTestNormalizer(), testMaxBodyBytes, nil, nil, prometheus.NewRegistry())
	srv := newTestServer(t, h)

	data := mkLogsData(mkLogRecord("user_prompt", "session-1"))
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.status)
	require.Equal(t, "1", resp.header.Get("Retry-After"))
}

// TestHandleMetrics_QueueFull is TestHandleLogs_QueueFull's metrics-lane
// counterpart, and additionally uses an error *other* than
// ingest.ErrQueueFull to exercise writeQueueFull's "unexpected error" log
// branch (still a 503: EnqueueMetrics documents no other error, so anything
// else is treated as the same backpressure signal rather than surfaced as a
// distinct, undocumented contract).
func TestHandleMetrics_QueueFull(t *testing.T) {
	t.Parallel()

	fe := &fakeEnqueuer{err: errors.New("synthetic enqueue failure")}
	h := New(fe, newTestNormalizer(), testMaxBodyBytes, nil, nil, prometheus.NewRegistry())
	srv := newTestServer(t, h)

	data := &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "argus.test.gauge",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 1}}},
			}},
		}}}},
	}}}
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/metrics", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.status)
	require.Equal(t, "1", resp.header.Get("Retry-After"))
}

// TestHandleMetrics_MalformedProtobuf covers handleMetrics' decode-error
// branch, the metrics-lane counterpart of TestHandleLogs_MalformedProtobuf.
func TestHandleMetrics_MalformedProtobuf(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	resp := doRequest(t, srv, "/v1/metrics", contentTypeProtobuf, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, nil)
	require.Equal(t, http.StatusBadRequest, resp.status)
}

// --- AC: unknown protobuf field is accepted (both codecs) -----------------

// TestHandleLogs_UnknownFieldAccepted_JSON covers the JSON half of the AC "a
// payload containing an unknown protobuf field ... is accepted": an extra
// top-level JSON key a future OTLP version might add.
func TestHandleLogs_UnknownFieldAccepted_JSON(t *testing.T) {
	t.Parallel()

	h, fe := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	data := mkLogsData(mkLogRecord("user_prompt", "session-future"))
	body, err := protojson.Marshal(data)
	require.NoError(t, err)

	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &envelope))
	envelope["futureField"] = json.RawMessage(`"claude-code-added-this"`)
	body, err = json.Marshal(envelope)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/logs", contentTypeJSON, body, nil)
	require.Equal(t, http.StatusOK, resp.status)
	require.Len(t, fe.events, 1)
	require.Equal(t, "session-future", fe.events[0].SessionID)
}

// TestHandleLogs_UnknownFieldAccepted_Protobuf covers the binary half of the
// same AC (lead note 1: "construct a payload with a genuinely unknown field
// number for the binary case ... rather than only testing the JSON path").
// It marshals one ResourceLogs, appends a hand-encoded field with a field
// number no version of this message defines, and wraps the result exactly
// as the top-level envelope would — reproducing what an ExportLogsServiceRequest
// carrying a future OTLP addition inside one of its resource_logs elements
// looks like on the wire.
func TestHandleLogs_UnknownFieldAccepted_Protobuf(t *testing.T) {
	t.Parallel()

	h, fe := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	resourceLogs := &logspb.ResourceLogs{
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			mkLogRecord("user_prompt", "session-unknown-field"),
		}}},
	}
	rlBytes, err := proto.Marshal(resourceLogs)
	require.NoError(t, err)

	// Append a field number (99999) no version of ResourceLogs defines.
	// Appending a fully-formed field to the end of an already-encoded
	// message is valid protobuf: field order is never significant.
	unknown := protowire.AppendTag(nil, 99999, protowire.VarintType)
	unknown = protowire.AppendVarint(unknown, 42)
	rlBytes = append(rlBytes, unknown...)

	envelope := protowire.AppendTag(nil, 1, protowire.BytesType)
	envelope = protowire.AppendBytes(envelope, rlBytes)

	resp := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, envelope, nil)
	require.Equal(t, http.StatusOK, resp.status)
	require.Len(t, fe.events, 1)
	require.Equal(t, "session-unknown-field", fe.events[0].SessionID)
}

// --- AC: RequireIngestToken-shaped auth wiring ----------------------------

// TestHandler_AuthMiddlewareApplied proves the auth middleware New receives
// actually guards all three routes, exercising the Mount seam (Handler
// cannot import httpapi.RequireIngestToken directly — depguard — so this
// stands in for it with an equivalent bearer-token check).
func TestHandler_AuthMiddlewareApplied(t *testing.T) {
	t.Parallel()

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
	h, fe := newTestHandler(t, auth)
	srv := newTestServer(t, h)

	data := mkLogsData(mkLogRecord("user_prompt", "session-1"))
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	unauthed := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusUnauthorized, unauthed.status)
	require.Zero(t, fe.eventCount())

	authed := doRequest(t, srv, "/v1/logs", contentTypeProtobuf, body, map[string]string{"Authorization": "Bearer " + token})
	require.Equal(t, http.StatusOK, authed.status)
	require.Len(t, fe.events, 1)
}

// --- AC: unsupported content type -> 415 ----------------------------------

// TestHandleLogs_UnsupportedContentType covers SPEC §3.4's "Other types ->
// 415" content-negotiation rule.
func TestHandleLogs_UnsupportedContentType(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	resp := doRequest(t, srv, "/v1/logs", "text/plain", []byte("not otlp"), nil)
	require.Equal(t, http.StatusUnsupportedMediaType, resp.status)
}

// TestUnsupportedContentType_AnswersInJSON covers m17 (minor): a 415 must
// answer in a format the client can actually read. A request whose
// Content-Type negotiation failed has, by definition, not declared it speaks
// protobuf — readBody previously fell back to the zero wireFormat value
// (wireProtobuf) on that path (codec.go's readBody, pre-fix), so a client
// like this one got a binary google.rpc.Status body it could not parse.
// Table-driven across all three routes because the finding calls out that
// "all three handlers pass [the zero value] to writeStatus".
func TestUnsupportedContentType_AnswersInJSON(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/v1/logs", "/v1/metrics", "/v1/traces"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			h, _ := newTestHandler(t, nil)
			srv := newTestServer(t, h)

			resp := doRequest(t, srv, path, "text/plain", []byte("not otlp"), nil)
			require.Equal(t, http.StatusUnsupportedMediaType, resp.status)
			require.Equal(t, "application/json", resp.header.Get("Content-Type"))

			// The body must parse as the JSON google.rpc.Status mapping
			// (statusJSON), not protobuf bytes, and carry a readable message.
			var s statusJSON
			require.NoError(t, json.Unmarshal(resp.body, &s), "415 body must be valid JSON, not protobuf bytes")
			require.Equal(t, grpcCodeInvalidArgument, s.Code)
			require.NotEmpty(t, s.Message)
		})
	}
}

// --- AC: metrics rejections (unsupported aggregation type) ----------------

// TestHandleMetrics_PartialSuccess covers the metrics-lane analogue of
// TestHandleLogs_PartialSuccessOnMissingSessionID: a metric with an
// aggregation type this ticket's normalizer does not decode (Summary — see
// normalize/otel_metrics.go's rejection policy) is reported via
// partial_success.rejected_data_points, and a decodable Gauge sample
// alongside it still reaches the Enqueuer.
func TestHandleMetrics_PartialSuccess(t *testing.T) {
	t.Parallel()

	h, fe := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	data := &metricspb.MetricsData{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{
					{
						Name: "argus.test.gauge",
						Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
							DataPoints: []*metricspb.NumberDataPoint{{
								TimeUnixNano: uint64(fixedNow.UnixNano()),
								Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 1.5},
							}},
						}},
					},
					{
						Name: "argus.test.summary",
						Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{{TimeUnixNano: uint64(fixedNow.UnixNano())}},
						}},
					},
				},
			}},
		}},
	}
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/metrics", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusOK, resp.status)

	rejected, _ := decodePartialSuccess(t, resp.body)
	require.Equal(t, int64(1), rejected)
	require.Len(t, fe.samples, 1)
	require.Equal(t, "argus.test.gauge", fe.samples[0].Name)
}

// --- shared helpers --------------------------------------------------------

func mustJSON(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	b, err := protojson.Marshal(msg)
	require.NoError(t, err)
	return b
}

// metricValue reads a counter's current value straight off its Write(*dto.Metric)
// method — the same technique internal/ingest/pipeline_test.go and
// internal/ingest/hooks/handler_test.go use instead of
// prometheus/client_golang/prometheus/testutil.ToFloat64: testutil pulls in
// a transitive dependency this module's go.sum does not declare, and this
// ticket may not add one (`go get`/`go mod tidy` are both off the table).
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var pb dto.Metric
	require.NoError(t, m.Write(&pb))
	if pb.Counter == nil {
		t.Fatal("metricValue: metric is not a counter")
	}
	return pb.Counter.GetValue()
}

// decodePartialSuccess extracts field 1 (rejected count) / field 2 (error
// message) from a writeExportResult protobuf body's partial_success
// submessage (field 1 of the outer response) — the response-side
// counterpart of decodeStatus, for the same collector-package-avoidance
// reason documented on codec.go's writeExportResult.
func decodePartialSuccess(t *testing.T, body []byte) (rejected int64, message string) {
	t.Helper()
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		require.Positive(t, n)
		body = body[n:]
		if num != 1 || typ != protowire.BytesType {
			skip := protowire.ConsumeFieldValue(num, typ, body)
			require.Positive(t, skip)
			body = body[skip:]
			continue
		}
		ps, n := protowire.ConsumeBytes(body)
		require.Positive(t, n)
		body = body[n:]

		for len(ps) > 0 {
			pnum, ptyp, pn := protowire.ConsumeTag(ps)
			require.Positive(t, pn)
			ps = ps[pn:]
			switch {
			case pnum == 1 && ptyp == protowire.VarintType:
				v, vn := protowire.ConsumeVarint(ps)
				require.Positive(t, vn)
				ps = ps[vn:]
				rejected = int64(v) //nolint:gosec // test-only decode of a value this package itself encoded from a slice length
			case pnum == 2 && ptyp == protowire.BytesType:
				v, vn := protowire.ConsumeBytes(ps)
				require.Positive(t, vn)
				ps = ps[vn:]
				message = string(v)
			default:
				skip := protowire.ConsumeFieldValue(pnum, ptyp, ps)
				require.Positive(t, skip)
				ps = ps[skip:]
			}
		}
	}
	return rejected, message
}

// TestHandleMetrics_PartialSuccess_CountsEveryRejectedDataPoint pins the
// handler half of audit finding m14. FromOTLPMetrics emits exactly ONE
// Rejection for a metric whose aggregation type is unsupported, discarding
// all of that metric's data points with it — so reporting len(rejections)
// as rejectedDataPoints told an operator "1 point rejected" while three
// were thrown away. A Summary with three data points is the smallest case
// that distinguishes summing Rejection.Count from counting rejections.
func TestHandleMetrics_PartialSuccess_CountsEveryRejectedDataPoint(t *testing.T) {
	t.Parallel()

	h, _ := newTestHandler(t, nil)
	srv := newTestServer(t, h)

	data := &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "argus.test.summary",
			// Summary is not an aggregation Argus stores, so the whole
			// metric is rejected as a single Rejection carrying all three
			// of its data points.
			Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
				DataPoints: []*metricspb.SummaryDataPoint{{}, {}, {}},
			}},
		}}}},
	}}}
	body, err := proto.Marshal(data)
	require.NoError(t, err)

	resp := doRequest(t, srv, "/v1/metrics", contentTypeProtobuf, body, nil)
	require.Equal(t, http.StatusOK, resp.status)

	rejected, message := decodePartialSuccess(t, resp.body)
	require.Equal(t, int64(3), rejected,
		"rejectedDataPoints must sum Rejection.Count, not count Rejection values: one unsupported-aggregation rejection stands for every data point it discarded")
	require.NotEmpty(t, message)
}
