package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// fakeHubTarget is ingest.HubTarget's test double: it only ever records what
// it was handed, with no subscriber fan-out at all — HubPublisher's own
// contract (fast, non-blocking, no I/O) is exercised against this, not a
// real *stream.Hub, so these tests stay deterministic and need no
// goroutine/channel teardown of their own.
type fakeHubTarget struct {
	mu    sync.Mutex
	calls int
	evs   []stream.Envelope
	sess  []model.SessionSummary
}

func (f *fakeHubTarget) Publish(evs []stream.Envelope, sess []model.SessionSummary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.evs = append(f.evs, evs...)
	f.sess = append(f.sess, sess...)
}

func (f *fakeHubTarget) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeHubTarget) eventCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.evs)
}

func (f *fakeHubTarget) events() []stream.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stream.Envelope(nil), f.evs...)
}

func (f *fakeHubTarget) sessionFrameCount(sessionID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sess {
		if s.ID == sessionID {
			n++
		}
	}
	return n
}

// lastEventProject returns the Project carried by the most recently
// recorded envelope for sessionID, or "" if none has been recorded — used
// by the self-correcting-project AC, which cares about the most recent
// envelope's project, not the whole history.
func (f *fakeHubTarget) lastEventProject(sessionID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.evs) - 1; i >= 0; i-- {
		if f.evs[i].Event.SessionID == sessionID {
			return f.evs[i].Project
		}
	}
	return ""
}

// fakeSessionReader is ingest.SessionReader's test double: a settable map of
// session id -> either a *model.SessionSummary or a scripted error, so a
// test can simulate "SessionStart hasn't landed yet" (an error) followed by
// "resolved" (a summary) with no database involved at all.
type fakeSessionReader struct {
	mu        sync.Mutex
	summaries map[string]*model.SessionSummary
	errs      map[string]error
}

func newFakeSessionReader() *fakeSessionReader {
	return &fakeSessionReader{summaries: map[string]*model.SessionSummary{}, errs: map[string]error{}}
}

func (f *fakeSessionReader) set(id, project string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.errs, id)
	f.summaries[id] = &model.SessionSummary{ID: id, Project: project}
}

func (f *fakeSessionReader) setErr(id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.summaries, id)
	f.errs[id] = err
}

func (f *fakeSessionReader) SessionSummary(_ context.Context, id string) (*model.SessionSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errs[id]; ok {
		return nil, err
	}
	if s, ok := f.summaries[id]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("fakeSessionReader: no summary stubbed for %q", id)
}

// --- Publish's own contract (hot path, no debounce loop involved) --------

func TestHubPublisher_Publish_CallsHubExactlyOncePerBatch(t *testing.T) {
	hub := &fakeHubTarget{}
	pub := ingest.NewHubPublisher(hub, newFakeSessionReader())

	pub.Publish([]model.Event{
		{SessionID: "s1", ID: "e1"},
		{SessionID: "s1", ID: "e2"},
		{SessionID: "s2", ID: "e3"},
	})

	require.Equal(t, 1, hub.callCount(),
		"Publish must call hub.Publish exactly once per batch, regardless of how many distinct sessions it touches")
	require.Equal(t, 3, hub.eventCount())
}

func TestHubPublisher_Publish_PreservesOrderAndUsesCachedProject(t *testing.T) {
	hub := &fakeHubTarget{}
	reader := newFakeSessionReader()
	reader.set("sess-1", "proj-a")
	pub := ingest.NewHubPublisher(hub, reader, ingest.WithSessionDebounce(5*time.Millisecond))

	// Warm the project cache via one real debounce tick before asserting
	// Publish's own (I/O-free) behavior in isolation.
	pub.Publish([]model.Event{{SessionID: "sess-1", ID: "warm"}})
	ctx, cancel := context.WithCancel(context.Background())
	go pub.Run(ctx)
	require.Eventually(t, func() bool { return hub.sessionFrameCount("sess-1") >= 1 }, time.Second, 2*time.Millisecond)
	cancel()

	events := []model.Event{
		{SessionID: "sess-1", ID: "a"},
		{SessionID: "sess-1", ID: "b"},
		{SessionID: "sess-1", ID: "c"},
	}
	pub.Publish(events)

	got := hub.events()
	require.Len(t, got, 4) // "warm" + a, b, c
	last3 := got[len(got)-3:]
	for i, env := range last3 {
		require.Equal(t, events[i].ID, env.Event.ID, "envelope order must match Publish's input order")
		require.Equal(t, "proj-a", env.Project, "a resolved session's cached project must be used")
	}
}

// --- AC: an envelope for a session whose project is unknown carries "",
// and self-corrects once the debounce loop reads the resolved projection
// (SPEC §5.3) ---

func TestHubPublisher_ProjectSelfCorrects_AfterDebounceTick(t *testing.T) {
	hub := &fakeHubTarget{}
	reader := newFakeSessionReader()
	reader.setErr("sess-1", errors.New("session not resolved yet"))
	pub := ingest.NewHubPublisher(hub, reader, ingest.WithSessionDebounce(10*time.Millisecond))

	pub.Publish([]model.Event{{SessionID: "sess-1", ID: "evt-1"}})
	require.Equal(t, "", hub.lastEventProject("sess-1"),
		"an unknown session's cache miss must publish an envelope carrying an empty project")

	// Resolve the session (as SessionStart landing would) and let the
	// debounce loop run long enough to read it at least once.
	reader.set("sess-1", "proj-real")
	ctx, cancel := context.WithCancel(context.Background())
	go pub.Run(ctx)
	require.Eventually(t, func() bool { return hub.sessionFrameCount("sess-1") >= 1 }, time.Second, 2*time.Millisecond)
	cancel()

	pub.Publish([]model.Event{{SessionID: "sess-1", ID: "evt-2"}})
	require.Equal(t, "proj-real", hub.lastEventProject("sess-1"),
		"once the debounce loop has read the resolved projection, later envelopes must carry the real project")
}

// --- AC: a session receiving 50 events in 500ms produces at most 2
// `session` frames ---

func TestHubPublisher_Debounce_BurstOfFiftyEventsProducesAtMostTwoSessionFrames(t *testing.T) {
	hub := &fakeHubTarget{}
	reader := newFakeSessionReader()
	reader.set("sess-burst", "proj-a")
	pub := ingest.NewHubPublisher(hub, reader) // production default: the AC's own 500ms

	ctx, cancel := context.WithCancel(context.Background())
	go pub.Run(ctx)
	defer cancel()

	for i := 0; i < 50; i++ {
		pub.Publish([]model.Event{{SessionID: "sess-burst", ID: fmt.Sprintf("evt-%d", i)}})
	}

	// Comfortably longer than one 500ms debounce interval, so the burst's
	// single dirty entry is guaranteed to have been flushed at least once —
	// long enough that a second tick landing right at the boundary is
	// plausible too, never a third.
	time.Sleep(1100 * time.Millisecond)
	cancel()

	frames := hub.sessionFrameCount("sess-burst")
	require.GreaterOrEqual(t, frames, 1, "the debounce loop must eventually deliver the session frame")
	require.LessOrEqual(t, frames, 2, "50 events landing inside one 500ms window must not produce more than 2 session frames")
}

// --- AC: a failing transaction publishes zero frames (the UI must never
// show an event that isn't stored) — pinned end to end through the real
// Pipeline + HubPublisher, not just through matchPersisted's own unit tests
// in pipeline_test.go ---

func TestHubPublisher_FailingWriteTransactionPublishesZeroEventFrames(t *testing.T) {
	fw := &fakeWriter{writeBatchFunc: func(_ context.Context, _ []model.Event) (store.BatchResult, error) {
		return store.BatchResult{}, errors.New("synthetic write failure")
	}}
	hub := &fakeHubTarget{}
	pub := ingest.NewHubPublisher(hub, newFakeSessionReader())

	p := ingest.New(fw,
		ingest.PipelineConfig{QueueCap: 16, Workers: 1, BatchSize: 2, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()),
		ingest.WithLogger(discardLogger()),
		ingest.WithSleep(instantSleep),
		ingest.WithPublisher(pub),
	)
	defer func() { _ = p.Close(context.Background()) }()

	require.NoError(t, p.EnqueueEvents([]model.Event{
		testEvent("fails-1", model.SourceHook),
		testEvent("fails-2", model.SourceHook),
	}))

	// The write path burns its whole retry budget (instantSleep makes every
	// backoff a no-op) and then drops the batch; give that a moment to run
	// to completion before asserting nothing was ever published.
	time.Sleep(300 * time.Millisecond)

	require.Zero(t, hub.eventCount(),
		"a failing transaction must publish zero event frames — the UI must never show an event that isn't stored")
	require.Zero(t, hub.callCount(), "hub.Publish must never be called at all for a batch that never persisted")
}
