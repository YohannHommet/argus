// publish_dedup_test.go: P5-03 AC "10 new + 10 duplicates publishes 10 frames"
// against real postgres dedup ledger. Pipeline tests use fakes; only
// internal/app can import both ingest and postgres (package doc), proving the
// real ingest_dedup gate (depguard: ingest must not import postgres directly).
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

// recordingHubTarget: test double for ingest.HubTarget. Records envelopes
// without fan-out; this test's subject is dedup, not SSE delivery.
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

// dedupTestEvent: minimal real-store-writable model.Event. ID left empty;
// events.go's insertEventsSQL uses uuidv7() default (no normalizer mints ID
// per SPEC §1.6), so a non-UUID string would fail instead of exercising real path.
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
