// Package ingest — publish.go implements P5-03's real Publisher (the seam
// pipeline.go:108-148 declares): HubPublisher turns "a flush persisted these
// events" into stream.Envelopes carrying the session's project, and
// debounces the much lower-volume `session` projection frame per SPEC
// §5.3's "500ms per session" rule.
//
// depguard note: this file is the one place internal/ingest imports
// internal/stream (pipeline.go's package doc already calls this out as
// allowed and intended — SPEC §3.1: "ingest -> normalize + store + stream").
// It does NOT import internal/store/postgres: SessionReader below is a
// narrow, consumer-owned port postgres.Store satisfies structurally (the
// same convention httpapi.Reader/MigrationsChecker/QueueSaturationChecker
// already establish), so this package never has to know which backend
// implements it.
package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// defaultSessionDebounce is SPEC §5.3's "per-session 500ms debounce for
// session frames" default. WithSessionDebounce overrides it (tests shorten
// it so the debounce ACs don't have to run at real wall-clock 500ms
// multiples).
const defaultSessionDebounce = 500 * time.Millisecond

// defaultProjectCacheCap bounds HubPublisher's session-id -> project cache
// (see HubPublisher's own doc comment for the SPEC §5.3 self-correcting
// promise this cache exists to serve). Left unbounded, a long-running
// server would grow it by one entry per distinct session id it has EVER
// handled, for the life of the process — there is no cheap "this session
// will never be written to again" signal to evict on eagerly.
//
// 100_000 is a defensible round number for Argus's single-binary deployment
// shape (SPEC §0): each entry is a session id plus a short project string,
// well under 200 bytes even generously estimated, so the cap holds the
// whole cache under ~20MB even under sustained load — several orders of
// magnitude more concurrently-live sessions than a real deployment's
// ARGUS_RETENTION_RAW_DAYS window (default 90d) would plausibly produce.
// Eviction picks the oldest-INSERTED entry, not the least-recently-used
// one: cheaper to maintain (an insertion-order queue, no per-read
// bookkeeping on the hot Publish path) and just as safe, because of how
// harmless an eviction is (see projectCache's doc comment).
const defaultProjectCacheCap = 100_000

// SessionReader is the narrow, consumer-owned store port HubPublisher's
// debounce loop needs: one session's current projection, by id.
// postgres.Store.SessionSummary satisfies it structurally — the same
// convention httpapi.Reader/httpapi.MigrationsChecker/
// httpapi.QueueSaturationChecker already establish (see router.go's Reader
// doc comment for the "why a narrow port, not the whole store" reasoning;
// it applies identically here).
type SessionReader interface {
	SessionSummary(ctx context.Context, id string) (*model.SessionSummary, error)
}

// HubTarget is the narrow hub port HubPublisher needs: fan-out, nothing
// else. *stream.Hub satisfies it structurally. Depending on this instead of
// *stream.Hub's whole surface means a test can exercise HubPublisher
// against a fake that only ever records what it was handed, with no
// subscriber machinery at all.
type HubTarget interface {
	Publish(evs []stream.Envelope, sess []model.SessionSummary)
}

// hubPublisherOptions collects HubPublisherOption values before
// construction, mirroring this package's options/Option pattern
// (pipeline.go:181-222) and stream's hubOptions/Option (hub.go:41-55).
type hubPublisherOptions struct {
	logger   *slog.Logger
	debounce time.Duration
}

// HubPublisherOption configures an optional HubPublisher dependency. Every
// option's zero value is production-safe.
type HubPublisherOption func(*hubPublisherOptions)

// WithHubPublisherLogger overrides the *slog.Logger the debounce loop logs
// a failed SessionSummary read to (Run's doc comment: a session can be
// swept/retention-deleted between being marked dirty and the next tick —
// that is expected, not an error worth failing loud over).
func WithHubPublisherLogger(l *slog.Logger) HubPublisherOption {
	return func(o *hubPublisherOptions) { o.logger = l }
}

// WithSessionDebounce overrides the per-session `session` frame debounce
// interval (default 500ms, SPEC §5.3). d <= 0 keeps the default, the same
// convention stream.WithBuffer uses, rather than constructing a
// time.Ticker with a non-positive period (which panics).
func WithSessionDebounce(d time.Duration) HubPublisherOption {
	return func(o *hubPublisherOptions) {
		if d > 0 {
			o.debounce = d
		}
	}
}

// projectCache is HubPublisher's bounded session-id -> project map (SPEC
// §5.3). A cache miss (get returns "") is not an error: it is the
// documented "session's project is still unknown" state every new session's
// first few events pass through before the debounce loop's first
// SessionStart-informed read resolves it (see HubPublisher's own doc
// comment).
//
// Bounded per defaultProjectCacheCap's doc comment: order records insertion
// order so set can evict the oldest entry once the cap is hit, without a
// per-read cost (no LRU bookkeeping) on the hot Publish path. An eviction is
// harmless — the very next debounce tick that finds the evicted session
// still dirty (any session still producing events is marked dirty on every
// Publish call, cache hit or miss alike) refills it, so the only observable
// effect is a handful of envelopes carrying "" for a project the cache used
// to remember, for at most one more debounce interval: the identical gap a
// brand-new session's very first events already pass through.
type projectCache struct {
	mu    sync.RWMutex
	m     map[string]string
	order []string // insertion order, oldest first; only ever grown/trimmed by set
	cap   int
}

func newProjectCache(capacity int) *projectCache {
	return &projectCache{m: make(map[string]string), cap: capacity}
}

// get returns id's cached project, or "" on a miss.
func (c *projectCache) get(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[id]
}

// set records id's project, evicting the oldest-inserted entry first if id
// is new and the cache is already at capacity. Updating an already-cached
// id's value never touches order — only a genuinely new key consumes
// capacity, so repeated re-sets for the same active session (exactly what
// the debounce loop does every tick it stays dirty) cannot spuriously push
// other entries out.
func (c *projectCache) set(id, project string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[id]; !exists {
		if c.cap > 0 && len(c.order) >= c.cap {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.m, oldest)
		}
		c.order = append(c.order, id)
	}
	c.m[id] = project
}

// HubPublisher is the SPEC §5.3 Publisher: it implements ingest.Publisher
// (Publish) so the pipeline can hand it every persisted flush, and it runs
// its own debounce loop (Run) that turns "these sessions had events land in
// the last debounce window" into the much lower-volume `session` frame SPEC
// §5.1 documents.
//
// # Project resolution and the self-correcting "" (SPEC §5.3)
//
// model.Event carries no project — only the session row does — so every
// Envelope's Project comes from HubPublisher's own in-memory cache
// (projectCache), populated exclusively by the debounce loop's
// SessionSummary reads, never by Publish itself (Publish must never do I/O,
// see its own doc comment). A brand-new session's first events therefore
// always publish with Project == "" (a cache miss), and stay that way until
// the debounce loop's next tick reads the session-projection row — by which
// point SessionStart has very likely landed and populated it — and caches
// the real value. Every envelope published from that point on carries the
// real project. This is SPEC §5.3's documented rule ("events for a session
// whose project is still unknown carry "" ... self-correcting once the
// SessionStart hook lands"), implemented, not worked around.
type HubPublisher struct {
	hub    HubTarget
	reader SessionReader
	logger *slog.Logger

	debounce time.Duration
	cache    *projectCache

	dirtyMu sync.Mutex
	dirty   map[string]struct{}
}

// NewHubPublisher constructs a HubPublisher. hub and reader must be
// non-nil; Run must be started (by internal/app/serve.go, alongside the
// other scheduler-shaped jobs) for `session` frames to ever be emitted —
// Publish alone only ever produces `event` frames.
func NewHubPublisher(hub HubTarget, reader SessionReader, opts ...HubPublisherOption) *HubPublisher {
	o := hubPublisherOptions{debounce: defaultSessionDebounce}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return &HubPublisher{
		hub:      hub,
		reader:   reader,
		logger:   o.logger,
		debounce: o.debounce,
		cache:    newProjectCache(defaultProjectCacheCap),
		dirty:    make(map[string]struct{}),
	}
}

// Publish implements ingest.Publisher (pipeline.go:108-148's contract in
// full — see that doc comment for the two-sided guarantee this method must
// uphold). It is the pipeline's dedicated publish goroutine calling this,
// never the flushing worker itself, but the contract still binds: this must
// return quickly, must never block, and must never do I/O — every field
// this method touches is already in memory (the batch itself, the project
// cache, the dirty set), and the only "send" is HubTarget.Publish, which
// stream.Hub already guarantees never blocks on a slow subscriber.
//
// Ordering is preserved end to end: envs is built by iterating events in
// the exact order the pipeline handed them to Publish (its own within-flush
// ordering guarantee), and passed to hub.Publish in one call — never split
// across more than one call, and never reordered.
//
// Safe to call after the HubPublisher's own Run(ctx) has stopped (its ctx
// is done): events still publish exactly as before, only the dirty-set
// drain that would have turned this batch's sessions into fresh `session`
// frames no longer runs. Safe to call concurrently with itself and with a
// concurrently-running Run tick, since both projectCache and the dirty set
// are mutex-guarded.
func (p *HubPublisher) Publish(events []model.Event) {
	if len(events) == 0 {
		return
	}

	envs := make([]stream.Envelope, len(events))
	for i, e := range events {
		envs[i] = stream.Envelope{Event: e, Project: p.cache.get(e.SessionID)}
	}
	p.hub.Publish(envs, nil)

	p.dirtyMu.Lock()
	for _, e := range events {
		p.dirty[e.SessionID] = struct{}{}
	}
	p.dirtyMu.Unlock()
}

// Run is the debounce loop (SPEC §5.3): every p.debounce, it swaps out
// whatever session ids Publish marked dirty since the last tick, reads each
// one's current projection, refreshes the project cache from it (the
// self-correcting half HubPublisher's own doc comment describes), and
// publishes the batch of resolved summaries in a single hub.Publish call.
// It ticks once immediately on entry — same rationale as internal/app's
// PartitionJob/SweepJob/RollupJob.Run: a freshly started process should not
// wait a full interval before its first pass — though in practice that
// first tick almost always finds nothing dirty yet and is a no-op. Returns
// when ctx is done; there is nothing to drain or flush (unlike Pipeline.
// Close, an in-flight debounce tick is not doing anything a caller needs to
// wait for — see Publish's own doc comment on why events keep flowing fine
// without this loop running).
func (p *HubPublisher) Run(ctx context.Context) {
	p.tick(ctx)

	ticker := time.NewTicker(p.debounce)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

// tick drains the dirty set and turns it into one hub.Publish(nil, sess...)
// call. A session whose SessionSummary read fails (swept/retention-deleted
// between being marked dirty and this tick — an expected race, not an
// error worth spamming the log over, per Run's own doc comment) is logged
// at Warn and skipped for this tick only: it is simply not in `dirty`
// anymore, so nothing re-reads it until a new event marks it dirty again.
func (p *HubPublisher) tick(ctx context.Context) {
	p.dirtyMu.Lock()
	dirty := p.dirty
	p.dirty = make(map[string]struct{}, len(dirty))
	p.dirtyMu.Unlock()

	if len(dirty) == 0 {
		return
	}

	summaries := make([]model.SessionSummary, 0, len(dirty))
	for id := range dirty {
		summary, err := p.reader.SessionSummary(ctx, id)
		if err != nil {
			p.logger.Warn("ingest: hub publisher: session summary read failed, skipping this debounce tick",
				"session_id", id, "error", err)
			continue
		}
		// The self-correcting half of SPEC §5.3's rule: every event
		// envelope published for this session AFTER this line carries the
		// real project, not "" — see HubPublisher's own doc comment.
		p.cache.set(id, summary.Project)
		summaries = append(summaries, *summary)
	}
	if len(summaries) == 0 {
		return
	}
	p.hub.Publish(nil, summaries)
}
