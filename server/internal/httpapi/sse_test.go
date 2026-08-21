package httpapi_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	storetest "github.com/YohannHommet/argus/server/internal/store/testing"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// --- shared fixtures/helpers -------------------------------------------

// shortStreamConfig is every test's ARGUS_STREAM_* config: short enough
// that no test sleeps for real seconds (the ticket's own instruction), but
// StreamReplayWindow is deliberately a few seconds, not milliseconds — a
// "just inside the window" ref (e.g. TestStream_ReplayRaceDedupe's) is
// built from time.Now() and only needs to survive test setup latency
// (HTTP round trip, goroutine scheduling) before the handler evaluates it,
// which a millisecond-scale window would make flaky for no benefit: only
// TestStreamAll_OutOfWindowReplayYieldsResetFirst cares about the window
// boundary itself, and it uses a ref an hour old regardless of window size.
func shortStreamConfig() *config.Config {
	return &config.Config{
		StreamHeartbeat:    50 * time.Millisecond,
		StreamReplayWindow: 5 * time.Second,
		StreamReplayMax:    100,
	}
}

// newStreamTestServer builds a real network server (httptest.NewServer, NOT
// httptest.NewRecorder — the ticket note: a recorder cannot exercise
// flushing/streaming) running httpapi.New wired with the given hub/replay.
func newStreamTestServer(t *testing.T, streamer httpapi.Streamer, replay httpapi.Replayer, cfg *config.Config) *httptest.Server {
	t.Helper()
	h := httpapi.New(httpapi.Deps{Stream: streamer, Replay: replay, Config: cfg, Assets: testAssets(t)})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// newTestHub builds a *stream.Hub against a fresh, private Prometheus
// registry (mirrors internal/stream/hub_test.go's own convention): New's
// default registerer is process-global and panics on a duplicate metric
// name if more than one Hub is ever constructed in the same test binary.
func newTestHub(opts ...stream.Option) *stream.Hub {
	return stream.New(append([]stream.Option{stream.WithRegisterer(prometheus.NewRegistry())}, opts...)...)
}

// testEventAt builds a minimal, valid model.Event for one session at a
// given (ts, seq) — every field newTimelineEvent/the wire format need a
// concrete value for, nothing more.
func testEventAt(sessionID string, ts time.Time, seq int64) model.Event {
	return model.Event{
		Seq:       seq,
		ID:        fmt.Sprintf("0192abcd-0000-0000-0000-%012d", seq),
		TS:        ts,
		SessionID: sessionID,
		Vendor:    "claude_code",
		Source:    model.SourceOTelLog,
		Kind:      model.KindToolResult,
		EventName: "tool_result",
	}
}

// testEvent is testEventAt with a deterministic, seq-derived timestamp —
// the common case where a test only cares about relative ordering.
func testEvent(sessionID string, seq int64) model.Event {
	base := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	return testEventAt(sessionID, base.Add(time.Duration(seq)*time.Millisecond), seq)
}

func eventRefOf(e model.Event) string {
	return (model.EventRef{TS: e.TS, Seq: e.Seq}).Encode()
}

// sseFrame is one parsed SSE message off the wire.
type sseFrame struct {
	ID, Event, Data, Comment, Retry string
}

// sseReader parses SPEC §5.1's wire grammar: `id:`/`event:`/`data:`/
// `retry:` lines accumulate into one frame, a line starting with `:` is a
// comment (the heartbeat), and a blank line ends the frame.
type sseReader struct {
	r *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader { return &sseReader{r: bufio.NewReader(r)} }

func (s *sseReader) next(t *testing.T) sseFrame {
	t.Helper()
	var f sseFrame
	for {
		line, err := s.r.ReadString('\n')
		require.NoError(t, err, "reading SSE frame off the wire")
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return f
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			f.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.Data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "retry: "):
			f.Retry = strings.TrimPrefix(line, "retry: ")
		case strings.HasPrefix(line, ":"):
			f.Comment = line
		default:
			t.Fatalf("unrecognized SSE line: %q", line)
		}
	}
}

// nextNamed reads frames until one carries a non-empty Event name, silently
// skipping `: heartbeat` comments along the way. shortStreamConfig's
// heartbeat is short enough (50ms) to legitimately interleave with the
// frames a test is asserting on when the suite runs under heavy parallel
// load (observed in practice: a fixed-count read loop that did not skip
// heartbeats flaked under `-race` with the full package running
// concurrently) — every test except the heartbeat-specific one itself only
// cares about named frames, so this is the one place that skip is decided.
func (s *sseReader) nextNamed(t *testing.T) sseFrame {
	t.Helper()
	for {
		f := s.next(t)
		if f.Event != "" {
			return f
		}
	}
}

// openStream issues a GET against url with ctx and returns the live
// response — callers must close resp.Body (t.Cleanup does it here).
func openStream(ctx context.Context, t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// openAndSkipRetry opens the stream and consumes the always-first retry
// line, returning a reader positioned right after it.
func openAndSkipRetry(ctx context.Context, t *testing.T, url string) (*http.Response, *sseReader) {
	t.Helper()
	resp := openStream(ctx, t, url)
	sr := newSSEReader(resp.Body)
	first := sr.next(t)
	require.Equal(t, "3000", first.Retry, "retry: must be the very first frame written (SPEC §5.3)")
	return resp, sr
}

func waitForSubscribers(t *testing.T, hub *stream.Hub, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return hub.Subscribers() == n }, time.Second, time.Millisecond,
		"hub.Subscribers() never reached %d", n)
}

// erroringStreamer is a fixed-error httpapi.Streamer, for exercising
// Subscribe's failure mapping without needing to actually exhaust a real
// hub's subscriber cap.
type erroringStreamer struct{ err error }

func (e erroringStreamer) Subscribe(stream.Topic, stream.Filter) (*stream.Subscription, error) {
	return nil, e.err
}

// recordingStreamer wraps a real hub, recording the last topic/filter
// Subscribe was called with — the clean way (per the ticket) to assert what
// the firehose's param binding actually asked the hub to subscribe to,
// while still behaving like a normal, working subscription.
type recordingStreamer struct {
	hub *stream.Hub

	mu     sync.Mutex
	topic  stream.Topic
	filter stream.Filter
}

func (r *recordingStreamer) Subscribe(topic stream.Topic, filter stream.Filter) (*stream.Subscription, error) {
	r.mu.Lock()
	r.topic, r.filter = topic, filter
	r.mu.Unlock()
	return r.hub.Subscribe(topic, filter)
}

func (r *recordingStreamer) last() (stream.Topic, stream.Filter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.topic, r.filter
}

// --- AC 1: headers + retry -----------------------------------------------

func TestStreamAll_HeadersAndRetryLine(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	resp := openStream(ctx, t, srv.URL+"/api/v1/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	require.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"))
	require.Equal(t, "keep-alive", resp.Header.Get("Connection"))

	sr := newSSEReader(resp.Body)
	first := sr.next(t)
	require.Equal(t, "3000", first.Retry, "retry: must arrive first, before any named frame")
	require.Empty(t, first.Event)
}

// --- AC 2: published events arrive as id:/event: event/data: in order ----

func TestStreamAll_PublishedEventsArriveInOrder(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	_, sr := openAndSkipRetry(ctx, t, srv.URL+"/api/v1/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	waitForSubscribers(t, hub, 1)

	e1 := testEvent("sess-order", 1)
	e2 := testEvent("sess-order", 2)
	hub.Publish([]stream.Envelope{{Event: e1}, {Event: e2}}, nil)

	f1 := sr.nextNamed(t)
	require.Equal(t, "event", f1.Event)
	require.Equal(t, eventRefOf(e1), f1.ID)
	require.Contains(t, f1.Data, `"event_ref":"`+eventRefOf(e1)+`"`)

	f2 := sr.nextNamed(t)
	require.Equal(t, "event", f2.Event)
	require.Equal(t, eventRefOf(e2), f2.ID)
	require.Contains(t, f2.Data, `"event_ref":"`+eventRefOf(e2)+`"`)
}

// --- AC 3: session/stats/lag frames carry no id: line ---------------------

func TestStreamAll_SessionAndStatsFramesCarryNoID(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	_, sr := openAndSkipRetry(ctx, t, srv.URL+"/api/v1/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	waitForSubscribers(t, hub, 1)

	hub.Publish(nil, []model.SessionSummary{{ID: "sess-clean", Status: model.SessionStatusActive}})
	sessionFrame := sr.nextNamed(t)
	require.Equal(t, "session", sessionFrame.Event)
	require.Empty(t, sessionFrame.ID, "session frame must carry no id: line (SPEC §5.1)")
	require.Contains(t, sessionFrame.Data, `"id":"sess-clean"`)

	hub.PublishStats(stream.Stats{EventsPerSec: 1, ActiveSessions: 1})
	statsFrame := sr.nextNamed(t)
	require.Equal(t, "stats", statsFrame.Event)
	require.Empty(t, statsFrame.ID, "stats frame must carry no id: line (SPEC §5.1)")
	require.Contains(t, statsFrame.Data, `"events_per_sec":1`)
}

// TestStreamAll_LagFrameCarriesNoID is deliberately its own test, isolated
// from the session/stats one above: it needs a buffer=1 hub to force an
// actual drop, and mixing that overflow with a session/stats publish in the
// same channel would make which message type survives the overflow
// (rather than getting evicted) undeterminable — see the burst comment
// below for why the overflow itself IS deterministic.
func TestStreamAll_LagFrameCarriesNoID(t *testing.T) {
	// buffer=1 makes a burst deterministically overflow the subscriber's
	// channel: hub.Publish below enqueues `burst` messages synchronously,
	// in-process (pure memory operations, no syscalls), which is far faster
	// than the SSE handler's writer goroutine can drain — each of its
	// iterations costs a channel receive plus a JSON marshal plus a write
	// syscall plus a flush syscall. The client also does not read the
	// response body until after the whole burst is published, so even a
	// generously large kernel socket buffer cannot let the handler race
	// ahead indefinitely. This is what makes "at least one drop occurs"
	// reliable rather than a sleep-based guess.
	hub := newTestHub(stream.WithBuffer(1))
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	waitForSubscribers(t, hub, 1)

	const burst = 5000
	envs := make([]stream.Envelope, burst)
	for i := range envs {
		envs[i] = stream.Envelope{Event: testEvent("sess-lag", int64(i+1))}
	}
	hub.Publish(envs, nil)

	sr := newSSEReader(resp.Body)
	first := sr.next(t)
	require.Equal(t, "3000", first.Retry)

	var sawLag bool
	// Bounded generously beyond `burst`: nextNamed already discards any
	// `: heartbeat` comment interleaved among the lag/event frames — under
	// heavy parallel test-suite load the 50ms heartbeat ticker can
	// legitimately fire while this loop is still draining the burst.
	for i := 0; i < burst*2 && !sawLag; i++ {
		f := sr.nextNamed(t)
		switch f.Event {
		case "lag":
			sawLag = true
			require.Empty(t, f.ID, "lag frame must carry no id: line (SPEC §5.1)")
			require.Contains(t, f.Data, `"dropped":`)
		case "event":
			require.NotEmpty(t, f.ID, "event frame must carry an id: line")
		default:
			t.Fatalf("unexpected frame kind %q while draining the overflow burst", f.Event)
		}
	}

	require.True(t, sawLag, "expected at least one lag frame from the buffer=1 overflow burst")
}

// --- AC 4: heartbeat within the configured interval -----------------------

func TestStreamAll_HeartbeatArrivesWithinInterval(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	cfg := shortStreamConfig()
	srv := newStreamTestServer(t, hub, nil, cfg)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	_, sr := openAndSkipRetry(ctx, t, srv.URL+"/api/v1/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	start := time.Now()
	f := sr.next(t)
	elapsed := time.Since(start)

	require.NotEmpty(t, f.Comment, "expected a heartbeat comment frame")
	require.Empty(t, f.Event)
	require.Less(t, elapsed, 3*cfg.StreamHeartbeat, "heartbeat did not arrive within the configured interval")
}

// --- AC 5: the replay race -------------------------------------------------

// TestStream_ReplayRaceDedupe is the ticket's centerpiece AC: with a replay
// position set, the backlog replays once, and any event published WHILE
// the replay query is running is delivered exactly once — never lost
// (attach-before-query), never duplicated (event_ref dedupe on flush).
//
// The race is real, not simulated with a sleep: the fake's EventsSinceFunc
// blocks on a channel until this test explicitly unblocks it; while
// blocked, the test publishes two events directly through the (already
// subscribed) hub — one, evC, is the SAME event the blocked query will
// independently return in its backlog once unblocked, and the other, evD,
// is live-only. If attach-before-query or the dedupe were broken, evC would
// arrive twice; if replayBacklog computed windowStart wrong or dropped its
// buffered live messages, evD would be lost.
func TestStream_ReplayRaceDedupe(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)

	const sessionID = "sess-race"
	// base is anchored to the real clock, not a fabricated date: the
	// replay-window check compares afterRef.TS against time.Now() minus
	// StreamReplayWindow, so a hardcoded past date would make this test's
	// "just inside the window" ref look arbitrarily out-of-window instead,
	// depending entirely on when the suite happens to run.
	base := time.Now().UTC()
	evA := testEventAt(sessionID, base, 1)
	evB := testEventAt(sessionID, base.Add(time.Millisecond), 2)
	evC := testEventAt(sessionID, base.Add(2*time.Millisecond), 3)
	evD := testEventAt(sessionID, base.Add(3*time.Millisecond), 4)

	entered := make(chan struct{})
	unblock := make(chan struct{})
	fake := &storetest.Fake{
		EventsSinceFunc: func(_ context.Context, _ model.EventRef, _ time.Time, _ int) ([]model.Event, error) {
			close(entered)
			<-unblock
			return []model.Event{evA, evB, evC}, nil
		},
	}

	srv := newStreamTestServer(t, hub, fake, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	afterRef := model.EventRef{TS: base.Add(-time.Second), Seq: 0}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/sessions/"+sessionID+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Last-Event-ID", afterRef.Encode())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("replay query (EventsSinceFunc) was never called")
	}

	// The handler is now blocked inside EventsSince, but it already
	// Subscribed before calling it (attach-before-query) — this Publish
	// exercises exactly the race SPEC §5.2 requires the handler to win.
	hub.Publish([]stream.Envelope{{Event: evC}, {Event: evD}}, nil)

	close(unblock)

	sr := newSSEReader(resp.Body)
	first := sr.next(t)
	require.Equal(t, "3000", first.Retry)

	var refs []string
	for i := 0; i < 4; i++ {
		f := sr.nextNamed(t)
		require.Equal(t, "event", f.Event, "frame %d", i)
		refs = append(refs, f.ID)
	}
	require.Equal(t, []string{eventRefOf(evA), eventRefOf(evB), eventRefOf(evC), eventRefOf(evD)}, refs,
		"replay delivers A, B, C once each in order, then the live-only D — C must not be duplicated by the buffered live publish")

	// Confirm there is no 5th, stray "event" frame hiding behind the next
	// heartbeat: nothing else was published, so the only thing that can
	// legitimately arrive next is a heartbeat comment.
	next := sr.next(t)
	require.Empty(t, next.Event, "no further named frame should follow — got %q (a duplicate?)", next.Event)
	require.NotEmpty(t, next.Comment)
}

// --- AC 6: out-of-window ref yields reset first, no replay ----------------

func TestStreamAll_OutOfWindowReplayYieldsResetFirst(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	cfg := shortStreamConfig()

	var calledEventsSince bool
	fake := &storetest.Fake{
		EventsSinceFunc: func(context.Context, model.EventRef, time.Time, int) ([]model.Event, error) {
			calledEventsSince = true
			return nil, nil
		},
	}
	srv := newStreamTestServer(t, hub, fake, cfg)

	oldRef := model.EventRef{TS: time.Now().Add(-time.Hour), Seq: 1}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	resp := openStream(ctx, t, srv.URL+"/api/v1/stream?after="+oldRef.Encode()) //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	sr := newSSEReader(resp.Body)
	first := sr.next(t)
	require.Equal(t, "3000", first.Retry)

	f := sr.next(t)
	require.Equal(t, "reset", f.Event)
	require.Empty(t, f.ID, "reset frame must carry no id: line")
	require.Contains(t, f.Data, `"reason":"replay_window_exceeded"`)
	require.False(t, calledEventsSince, "an out-of-window ref must not run the replay query at all")
}

func TestStreamAll_NilReplayerAlwaysResets(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ref := model.EventRef{TS: time.Now(), Seq: 1}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	resp := openStream(ctx, t, srv.URL+"/api/v1/stream?after="+ref.Encode()) //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	sr := newSSEReader(resp.Body)
	require.Equal(t, "3000", sr.next(t).Retry)

	f := sr.next(t)
	require.Equal(t, "reset", f.Event, "a nil Replayer must degrade to reset, never panic or hang")
}

// --- AC 7: cancelling the request context unsubscribes --------------------

func TestStreamAll_ContextCancelUnsubscribes(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ctx, cancel := context.WithCancel(t.Context())
	_, sr := openAndSkipRetry(ctx, t, srv.URL+"/api/v1/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream
	_ = sr

	waitForSubscribers(t, hub, 1)

	cancel()

	require.Eventually(t, func() bool { return hub.Subscribers() == 0 }, time.Second, time.Millisecond,
		"cancelling the request context must unsubscribe via serveStream's deferred sub.Close()")
}

// --- AC 8: ErrTooManySubscribers -> 503 problem+json ----------------------

func TestStreamAll_TooManySubscribers503(t *testing.T) {
	srv := newStreamTestServer(t, erroringStreamer{err: stream.ErrTooManySubscribers}, nil, shortStreamConfig())

	resp, err := http.Get(srv.URL + "/api/v1/stream") //nolint:noctx // deliberately no context: this test only cares about status/body, not cancellation
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))
	require.NotEqual(t, "text/event-stream", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "urn:argus:error:too-many-subscribers")
}

func TestStreamSession_ClosedHub503(t *testing.T) {
	srv := newStreamTestServer(t, erroringStreamer{err: stream.ErrClosed}, nil, shortStreamConfig())

	resp, err := http.Get(srv.URL + "/api/v1/sessions/abc/stream") //nolint:noctx // see TestStreamAll_TooManySubscribers503
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))
}

// --- AC 9: MessageShutdown -> shutdown frame, then the handler returns ----

func TestStreamAll_ShutdownFrameThenHandlerReturns(t *testing.T) {
	hub := newTestHub()

	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	resp, sr := openAndSkipRetry(ctx, t, srv.URL+"/api/v1/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	waitForSubscribers(t, hub, 1)

	hub.Shutdown()

	f := sr.nextNamed(t)
	require.Equal(t, "shutdown", f.Event)
	require.Empty(t, f.ID, "shutdown frame must carry no id: line")
	require.JSONEq(t, "{}", f.Data)

	// The handler must actually return (not just send the frame and keep
	// looping): the connection closes, so the next read hits EOF.
	buf := make([]byte, 1)
	_, err := resp.Body.Read(buf)
	require.ErrorIs(t, err, io.EOF)
}

// --- AC 10: undecodable replay ref -> 400 problem+json --------------------

func TestStreamAll_InvalidReplayRefIs400(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	srv := newStreamTestServer(t, hub, nil, shortStreamConfig())

	t.Run("bad ?after=", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/stream?after=not-a-valid-ref") //nolint:noctx // status/body only
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "urn:argus:error:invalid-event-ref")
	})

	t.Run("bad Last-Event-ID header", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/stream", nil)
		require.NoError(t, err)
		req.Header.Set("Last-Event-ID", "!!!not-base64!!!")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "urn:argus:error:invalid-event-ref")
	})

	t.Run("?after= wins over Last-Event-ID when both are present", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			srv.URL+"/api/v1/stream?after=not-a-valid-ref", nil)
		require.NoError(t, err)
		req.Header.Set("Last-Event-ID", model.EventRef{TS: time.Now(), Seq: 1}.Encode()) // well-formed, must be ignored
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode,
			"a malformed ?after= must win (and fail) even though Last-Event-ID alone would have been valid")
	})
}

// --- AC 11: firehose kinds/project/vendor build the hub Filter ------------

func TestStreamAll_BuildsFilterFromParams(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	rec := &recordingStreamer{hub: hub}
	srv := newStreamTestServer(t, rec, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	url := srv.URL + "/api/v1/stream?kinds=tool.result&kinds=tool.decision&project=argus&vendor=claude_code"
	openAndSkipRetry(ctx, t, url) //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	topic, filter := rec.last()
	require.Equal(t, stream.AllTopic(), topic)
	require.Equal(t, []model.Kind{model.KindToolResult, model.KindToolDecision}, filter.Kinds)
	require.Equal(t, "argus", filter.Project)
	require.Equal(t, "claude_code", filter.Vendor)
}

func TestStreamSession_TopicScopesToTheIDPathParam(t *testing.T) {
	hub := newTestHub()
	t.Cleanup(hub.Shutdown)
	rec := &recordingStreamer{hub: hub}
	srv := newStreamTestServer(t, rec, nil, shortStreamConfig())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	openAndSkipRetry(ctx, t, srv.URL+"/api/v1/sessions/sess-42/stream") //nolint:bodyclose // resp.Body is closed via t.Cleanup in openStream

	topic, filter := rec.last()
	require.Equal(t, stream.SessionTopic("sess-42"), topic)
	require.Equal(t, stream.Filter{}, filter, "openapi declares no filter params on streamSession")
}
