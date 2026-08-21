// publish_dedup_test.go pins P5-03's ticket AC "a batch of 10 new + 10
// duplicate events publishes exactly 10 frames" against the REAL dedup
// ledger — not a scripted fake store.Writer. internal/ingest/pipeline_test.go
// already has a recordingPublisher (TestPublisher_SeesOnlyPersistedEvents)
// that proves the pipeline's own contract ("Publish sees only what
// matchPersisted reports as persisted") against a fakeWriter whose dedup
// behavior is entirely hand-scripted; that test cannot also prove
// internal/store/postgres's real ingest_dedup gate (dedup.go/write.go) is
// what actually produces the right EventRefs in the first place. This file
// picks internal/app's own real-Postgres harness (storetesting.NewPool,
// following jobs_test.go's plain — not e2e-tagged — white-box convention:
// it needs a real Store and a real Pipeline, but never boots the HTTP
// server, so it does not need the e2e build tag jobs that go through
// Serve/HTTP use) precisely because internal/app is the one package allowed
// to import both internal/ingest and internal/store/postgres at once
// (package doc comment) — internal/ingest's own test package must not
// import internal/store/postgres (depguard: ingest may only import
// internal/store + internal/model + stdlib + prometheus).
package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// recordingHubTarget is ingest.HubTarget's test double for this file: it
// only ever records the envelopes it was handed, with no subscriber
// fan-out — this test's subject is "how many envelopes reach the hub port",
// not SSE delivery, which internal/stream's own suite already covers.
type recordingHubTarget struct {
	mu  sync.Mutex
	evs []stream.Envelope
}

func (r *recordingHubTarget) Publish(evs []stream.Envelope, _ []model.SessionSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, evs...)
}

func (r *recordingHubTarget) eventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.evs)
}

// dedupTestEvent builds a minimal, real-store-writable model.Event: enough
// fields for WriteBatch's whole path (sessions/turns projections, the
// ingest_dedup gate, the events insert) to succeed without error, with a
// caller-controlled DedupKey so two fixtures can share one on purpose. ID is
// deliberately left "": events.go's insertEventsSQL falls back to the
// events.id column's own uuidv7() default for an empty id (see its doc
// comment) — no normalizer ever mints one (SPEC §1.6), so a made-up
// non-UUID string here would fail with "invalid input syntax for type
// uuid" instead of exercising the real path.
func dedupTestEvent(dedupKey, sessionID string, ts time.Time) model.Event {
	return model.Event{
		DedupKey:   dedupKey,
		TS:         ts,
		IngestedAt: ts,
		SessionID:  sessionID,
		Vendor:     "claude_code",
		Source:     model.SourceHook,
		Kind:       model.KindToolResult,
		EventName:  "tool_result",
	}
}

func TestHubPublisher_RealDedupLedger_TenNewPlusTenDuplicatesPublishExactlyTen(t *testing.T) {
	ctx := context.Background()
	pool := storetesting.NewPool(t)
	st := postgres.New(pool)
	require.NoError(t, st.EnsurePartitions(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)))

	rec := &recordingHubTarget{}
	publisher := ingest.NewHubPublisher(rec, st)

	p := ingest.New(st,
		ingest.PipelineConfig{QueueCap: 64, Workers: 2, BatchSize: 20, FlushInterval: time.Hour},
		ingest.WithRegisterer(prometheus.NewRegistry()),
		ingest.WithPublisher(publisher),
	)
	defer func() { require.NoError(t, p.Close(context.Background())) }()

	const sessionID = "hubpub-dedup-session"
	now := time.Now().UTC()

	unique := make([]model.Event, 10)
	for i := 0; i < 10; i++ {
		unique[i] = dedupTestEvent(
			"dedup-"+string(rune('a'+i)),
			sessionID,
			now.Add(time.Duration(i)*time.Millisecond),
		)
	}
	// The batch's other 10 entries are EXACT duplicates (same DedupKey) of
	// the first 10 — WriteBatch's real ingest_dedup gate (dedup.go,
	// write.go) collapses each pair to a single candidate row, so only 10
	// distinct events are ever written and returned in EventRefs.
	batch := append(append([]model.Event{}, unique...), unique...)
	require.Len(t, batch, 20)

	require.NoError(t, p.EnqueueEvents(batch))

	require.Eventually(t, func() bool { return rec.eventCount() == 10 }, 5*time.Second, 10*time.Millisecond,
		"expected exactly 10 persisted-event envelopes for 10 new + 10 duplicate events; got %d", rec.eventCount())

	// Eventually only proves "reached 10 at some point" — give any stray
	// extra publish (there should be none: BatchSize=20 means this is a
	// single flush) a moment to arrive, then pin the count.
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 10, rec.eventCount(), "no further envelopes should ever be published for this batch")
}
