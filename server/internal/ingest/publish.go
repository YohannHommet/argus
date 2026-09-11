// Package ingest — publish.go implements P5-03's real Publisher (HubPublisher):
// turns persisted flushes into stream.Envelopes with sessions, debounces
// session frames per SPEC §5.3. Imports internal/stream (allowed SPEC §3.1).
package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// defaultSessionDebounce is SPEC §5.3's per-session debounce (tests shorten it).
const defaultSessionDebounce = 500 * time.Millisecond

// defaultProjectCacheCap bounds session-id->project cache (100k entries ~20MB).
// Insertion-order eviction (not LRU): cheap, harmless when evicted sessions re-publish.
const defaultProjectCacheCap = 100_000

// SessionReader is the narrow store port HubPublisher needs (SessionSummary).
type SessionReader interface {
	SessionSummary(ctx context.Context, id string) (*model.SessionSummary, error)
}

// HubTarget is the narrow hub port HubPublisher needs (fan-out only).
type HubTarget interface {
	Publish(evs []stream.Envelope, sess []model.SessionSummary)
}

// hubPublisherOptions collects HubPublisherOption values.
type hubPublisherOptions struct {
	logger   *slog.Logger
	debounce time.Duration
}

// HubPublisherOption configures optional HubPublisher dependencies (zero-value production-safe).
type HubPublisherOption func(*hubPublisherOptions)

// WithHubPublisherLogger overrides the logger for failed SessionSummary reads.
func WithHubPublisherLogger(l *slog.Logger) HubPublisherOption {
	return func(o *hubPublisherOptions) { o.logger = l }
}

// WithSessionDebounce overrides the per-session frame debounce (default 500ms, SPEC §5.3).
func WithSessionDebounce(d time.Duration) HubPublisherOption {
	return func(o *hubPublisherOptions) {
		if d > 0 {
			o.debounce = d
		}
	}
}

// projectCache is HubPublisher's bounded session-id -> project map (SPEC §5.3).
// Cache miss is expected ("session project still unknown"). Eviction is harmless.
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

// set records id's project, evicting oldest-inserted if new and at capacity.
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

// HubPublisher implements ingest.Publisher (SPEC §5.3): Publish emits envelopes,
// Run debounces dirty sessions and publishes their summaries. Event has no project
// field; Project comes from projectCache, populated by debounce loop's SessionSummary
// reads (Publish never does I/O). New sessions' first events publish Project=""
// (cache miss), self-correcting when SessionStart lands and Run fills cache.
type HubPublisher struct {
	hub    HubTarget
	reader SessionReader
	logger *slog.Logger

	debounce time.Duration
	cache    *projectCache

	dirtyMu sync.Mutex
	dirty   map[string]struct{}
}

// NewHubPublisher constructs a HubPublisher. Run must be started for session frames.
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

// Publish implements ingest.Publisher (pipeline contract). Must be fast, no I/O,
// ordering preserved end-to-end. Safe concurrent with Run ticks (mutex-guarded).
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

// Run is the debounce loop (SPEC §5.3): reads dirty sessions' projections, refreshes cache,
// publishes summaries per debounce interval. Ticks immediately on entry (first pass no-op).
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

// tick drains the dirty set, reads sessions, refreshes cache, publishes summaries.
// Failed reads (swept sessions) are logged at Warn, skipped this tick only.
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
		// SPEC §5.3 self-correcting: envelopes after this carry real project.
		p.cache.set(id, summary.Project)
		summaries = append(summaries, *summary)
	}
	if len(summaries) == 0 {
		return
	}
	p.hub.Publish(nil, summaries)
}
