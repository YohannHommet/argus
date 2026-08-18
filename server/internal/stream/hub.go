package stream

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/model"
)

// defaultBuffer/defaultMaxSubscribers mirror ARGUS_STREAM_BUFFER (256) and
// ARGUS_STREAM_MAX_SUBSCRIBERS (100) (SPEC §5.3). internal/config owns
// reading those env vars; this package only owns the fallback values, so it
// stays usable (argusd sim, tests) without ever importing internal/config
// (depguard, SPEC §3.1).
const (
	defaultBuffer         = 256
	defaultMaxSubscribers = 100
)

// ErrTooManySubscribers is returned by Subscribe once Subscribers() has
// reached WithMaxSubscribers' cap (default 100, SPEC §5.3). The SSE
// handler maps it to a 503 — a cap the hub hits well before the OS runs out
// of file descriptors or memory, protecting the process ahead of time
// rather than reacting once it is already in trouble.
var ErrTooManySubscribers = errors.New("stream: too many subscribers")

// ErrClosed is returned by Subscribe once Hub.Shutdown has run. It is
// deliberately NOT returned by Publish/PublishStats after Shutdown (SPEC
// §5.3, and internal/ingest's Publisher contract at pipeline.go:142-148:
// "a hub must tolerate being called after it considers itself shut down")
// — those calls become silent no-ops instead, so a publish racing the tail
// end of shutdown never has to check an error it has no useful response to
// anyway.
var ErrClosed = errors.New("stream: hub is closed")

// hubOptions collects Option values before Hub construction, so unexported
// fields never need to be exposed just for New's sake (mirrors
// internal/ingest's options/Option pattern, pipeline.go:181-222).
type hubOptions struct {
	buffer         int
	maxSubscribers int
	registerer     prometheus.Registerer
	logger         *slog.Logger
}

// Option configures an optional Hub dependency. Every option's zero value
// is production-safe: New defaults buffer/maxSubscribers to SPEC §5.3's
// 256/100, registerer to prometheus.DefaultRegisterer, and logger to
// slog.Default().
type Option func(*hubOptions)

// WithBuffer overrides the per-subscriber channel capacity
// (ARGUS_STREAM_BUFFER, default 256). n <= 0 keeps the default rather than
// constructing a channel that can never buffer anything, which would turn
// every single Publish into a guaranteed drop for that subscriber.
func WithBuffer(n int) Option {
	return func(o *hubOptions) {
		if n > 0 {
			o.buffer = n
		}
	}
}

// WithMaxSubscribers overrides the live-subscriber cap
// (ARGUS_STREAM_MAX_SUBSCRIBERS, default 100). n <= 0 keeps the default.
func WithMaxSubscribers(n int) Option {
	return func(o *hubOptions) {
		if n > 0 {
			o.maxSubscribers = n
		}
	}
}

// WithRegisterer overrides the Prometheus registerer New registers the
// argus_stream_* metrics against. Tests that construct more than one Hub in
// the same process must pass a fresh prometheus.NewRegistry() each time
// (mirrors internal/ingest.WithRegisterer, pipeline.go:196-203) — the
// package default, prometheus.DefaultRegisterer, is a process-global and
// panics on a duplicate metric name.
func WithRegisterer(r prometheus.Registerer) Option {
	return func(o *hubOptions) { o.registerer = r }
}

// WithLogger overrides the *slog.Logger the hub reports its own
// operational events to (a subscribe call hitting the cap, Shutdown
// running) — never the per-message drop path, which is high-frequency by
// design and already observable via DroppedTotal/argus_stream_dropped_total
// without spamming logs. SPEC §5.1's `lag` frame is the per-subscriber,
// per-drop signal meant for the browser; this logger is not a substitute
// for it.
func WithLogger(l *slog.Logger) Option {
	return func(o *hubOptions) { o.logger = l }
}

// hubMetrics is the ticket-mandated argus_stream_* self-observability
// surface, registered once by New.
type hubMetrics struct {
	// subscribers is a live gauge, labeled "topic" ("all"|"session") — set
	// via Inc/Dec/Set on Subscribe/unsubscribe/Shutdown rather than
	// recomputed by walking the maps, so reading it is never on the hot
	// Publish path.
	subscribers *prometheus.GaugeVec
	// dropped is the Prometheus-side counter backing argus_stream_dropped_total.
	// A Prometheus Counter has no cheap synchronous "current value" read
	// (client_golang exposes that only via Write(*dto.Metric) or the
	// testutil package), so Hub.DroppedTotal is backed by a plain atomic
	// counter (Hub.droppedTotal) kept in lockstep with this one instead.
	dropped prometheus.Counter
	// published counts items handed to Publish/PublishStats, labeled
	// "type" ("event"|"session"|"stats").
	published *prometheus.CounterVec
}

func newHubMetrics(reg prometheus.Registerer) *hubMetrics {
	m := &hubMetrics{
		subscribers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "argus", Subsystem: "stream", Name: "subscribers",
			Help: "Live SSE subscribers, by topic kind.",
		}, []string{"topic"}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "argus", Subsystem: "stream", Name: "dropped_total",
			Help: "Messages dropped because a subscriber's own buffer was full (SPEC §5.3 drop-oldest).",
		}),
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "argus", Subsystem: "stream", Name: "published_total",
			Help: "Items handed to Publish/PublishStats, by frame type.",
		}, []string{"type"}),
	}
	reg.MustRegister(m.subscribers, m.dropped, m.published)
	// Touch every label value once so the gauge exports a "0" series
	// immediately instead of only appearing once the first subscriber of
	// that kind connects — a dashboard querying argus_stream_subscribers
	// before that would otherwise be unable to tell "zero live
	// subscribers" from "hub not wired up at all".
	m.subscribers.WithLabelValues("all")
	m.subscribers.WithLabelValues("session")
	return m
}

// Hub is the SPEC §5.3 in-process pub/sub broker. Construct with New, which
// is immediately ready to Subscribe/Publish; call Shutdown exactly once, at
// process shutdown (SPEC §3.8), to notify every subscriber and release
// them.
type Hub struct {
	// mu guards every field below, including each live Subscription's
	// `closed` flag (subscription.go) — but NOT the per-message send
	// itself. Publish/PublishStats hold only mu.RLock() while iterating
	// and sending (so many concurrent Publish calls proceed together);
	// only Subscribe/unsubscribe/Shutdown ever take the exclusive Lock,
	// and none of those blocks on a subscriber's I/O — which is what keeps
	// a slow receiver from ever contending a mutex a publish needs (rule
	// 1's "must not block on a mutex held by a slow receiver").
	mu sync.RWMutex

	buffer         int
	maxSubscribers int
	logger         *slog.Logger
	metrics        *hubMetrics

	// all holds every TopicAll subscriber; bySession holds TopicSession
	// subscribers keyed by session id, each session getting its own set —
	// so publishing an event for session X only ever ranges over
	// bySession["X"], never any other session's subscribers (SPEC §5.3's
	// fan-out-cost guarantee). Sets, not slices, so unsubscribe is an O(1)
	// delete with no linear scan under concurrent subscribe/unsubscribe
	// churn (the ticket's leak-free requirement).
	all       map[*Subscription]struct{}
	bySession map[string]map[*Subscription]struct{}

	// subscriberCount mirrors len(all) + the sum of every bySession set's
	// length, maintained incrementally so Subscribers() and the cap check
	// never have to walk every session's set to answer "how many
	// subscribers are there right now".
	subscriberCount int

	// shutdown is set once, by Shutdown, and gates Subscribe (-> ErrClosed)
	// and Publish/PublishStats (-> silent no-op) from then on.
	shutdown bool

	// droppedTotal is DroppedTotal's backing store — see hubMetrics.dropped's
	// doc for why this exists alongside the Prometheus counter.
	droppedTotal atomic.Int64
}

// New constructs a ready-to-use Hub. Options are documented on their own
// With* functions; every default matches SPEC §5.3 exactly.
func New(opts ...Option) *Hub {
	o := hubOptions{buffer: defaultBuffer, maxSubscribers: defaultMaxSubscribers}
	for _, opt := range opts {
		opt(&o)
	}
	if o.registerer == nil {
		o.registerer = prometheus.DefaultRegisterer
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return &Hub{
		buffer:         o.buffer,
		maxSubscribers: o.maxSubscribers,
		logger:         o.logger,
		metrics:        newHubMetrics(o.registerer),
		all:            make(map[*Subscription]struct{}),
		bySession:      make(map[string]map[*Subscription]struct{}),
	}
}

// Subscribe registers a new subscriber for topic, filtered by filter, and
// returns the handle to receive on (SPEC §5.3). It fails closed on both
// terminal conditions: ErrClosed once Shutdown has run, ErrTooManySubscribers
// once WithMaxSubscribers' cap is reached — the SSE handler turns the
// latter into a 503 before it ever opens a response body.
func (h *Hub) Subscribe(topic Topic, filter Filter) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.shutdown {
		return nil, ErrClosed
	}
	if h.subscriberCount >= h.maxSubscribers {
		h.logger.Warn("stream: subscriber cap reached", "cap", h.maxSubscribers, "topic_kind", topic.Kind)
		return nil, ErrTooManySubscribers
	}

	s := &Subscription{topic: topic, filter: filter, ch: make(chan Message, h.buffer), hub: h}

	switch topic.Kind {
	case TopicAll:
		h.all[s] = struct{}{}
		h.metrics.subscribers.WithLabelValues("all").Inc()
	case TopicSession:
		set, ok := h.bySession[topic.ID]
		if !ok {
			set = make(map[*Subscription]struct{})
			h.bySession[topic.ID] = set
		}
		set[s] = struct{}{}
		h.metrics.subscribers.WithLabelValues("session").Inc()
	default:
		// Topic{} zero value or any other garbage Kind: a caller bug
		// (black-box contract violation), not a runtime condition to
		// tolerate silently — see TopicKind's doc for why the enum starts
		// at 1 specifically to make this reachable and worth failing
		// loud on, rather than registering a subscription that would
		// simply never receive anything.
		return nil, fmt.Errorf("stream: subscribe: invalid topic kind %d", topic.Kind)
	}
	h.subscriberCount++
	return s, nil
}

// removeLocked deletes s from whichever topic map holds it and updates
// bookkeeping. Caller must already hold h.mu for writing — its only callers
// are unsubscribe and Shutdown.
func (h *Hub) removeLocked(s *Subscription) {
	switch s.topic.Kind {
	case TopicAll:
		delete(h.all, s)
		h.metrics.subscribers.WithLabelValues("all").Dec()
	case TopicSession:
		set := h.bySession[s.topic.ID]
		delete(set, s)
		if len(set) == 0 {
			// Leak-free (ticket AC): leaving an empty per-session set
			// behind after its last subscriber unsubscribes would grow
			// bySession unboundedly over a long-running server's
			// lifetime — one stale entry per session id ever subscribed
			// to, never freed.
			delete(h.bySession, s.topic.ID)
		}
		h.metrics.subscribers.WithLabelValues("session").Dec()
	}
	h.subscriberCount--
}

// unsubscribe implements Subscription.Close: idempotent removal that never
// sends on, or closes, a channel more than once even under concurrent
// Close/Shutdown calls (ticket rule 6). It is the only place — besides
// Shutdown, which inlines the same sequence for every subscriber at once —
// that flips a Subscription's closed flag, always while holding h.mu so
// that decision and the map removal happen as one atomic step.
func (h *Hub) unsubscribe(s *Subscription) {
	h.mu.Lock()
	if s.closed {
		h.mu.Unlock()
		return
	}
	s.closed = true
	h.removeLocked(s)
	h.mu.Unlock()
	close(s.ch)
}

// send is the non-blocking delivery primitive every Publish/PublishStats
// call uses (rule 1 — the ticket's single most important property).
// Callers must hold at least h.mu.RLock(); send never blocks and never
// takes h.mu itself.
//
// It never blocks: a full channel evicts its own oldest buffered message
// (one non-blocking receive) to make room, then retries the send once; if
// that still fails — a concurrent receiver won the race, or another
// Publish call raced into the freed slot first — the NEW message is the
// one dropped instead, never a spin, never a wait. Either branch counts
// exactly one drop, matching the ticket's AC precisely: one message in
// beyond capacity, one increment of dropped.
func (h *Hub) send(s *Subscription, msg Message) {
	select {
	case s.ch <- msg:
		return
	default:
	}
	select {
	case <-s.ch:
		s.dropped.Add(1)
		h.droppedTotal.Add(1)
		h.metrics.dropped.Inc()
	default:
		// Nothing to evict — a concurrent receiver already drained a slot
		// between our failed send above and here. Fall through and retry;
		// room may already exist because of that.
	}
	select {
	case s.ch <- msg:
	default:
		s.dropped.Add(1)
		h.droppedTotal.Add(1)
		h.metrics.dropped.Inc()
	}
}

// Publish fans out evs and sess to every matching live subscriber (SPEC
// §5.3). It is called by the ingest pipeline's Publisher seam
// (pipeline.go:108-148) after a batch's write transaction has committed,
// with only what store.Writer actually persisted — Publish has no idea what
// "persisted" means, it only ever sees events that already passed that
// gate.
//
// An event reaches: every TopicAll subscriber whose Filter.MatchEvent
// passes, plus the TopicSession subscribers for env.Event.SessionID whose
// Filter.MatchEvent passes (rule 2). A model.SessionSummary reaches: every
// TopicAll subscriber whose Filter.MatchSession passes, plus the
// TopicSession subscribers for sess.ID whose Filter.MatchSession passes —
// applying the filter symmetrically to both topic kinds for both frame
// types, exactly as Filter.MatchSession's own doc comment describes it
// ("whether a session frame reaches THIS SUBSCRIBER", not qualified by
// topic).
//
// Publish never blocks: every send to a subscriber's channel is
// non-blocking (see send), and the mutex it takes (RLock, shared with every
// other concurrent Publish/PublishStats call) is never held by anything
// that itself blocks on a subscriber — only Subscribe/unsubscribe/Shutdown
// take the exclusive Lock, and each of those is a bounded, in-memory map
// operation. A Publish call racing Shutdown either observes h.shutdown
// before Shutdown flips it (and proceeds normally and safely, because
// Shutdown cannot remove a subscriber out from under an in-flight Publish —
// sync.RWMutex's Lock() waits for every outstanding RLock() to release
// first) or observes it after (and is a no-op).
func (h *Hub) Publish(evs []Envelope, sess []model.SessionSummary) {
	h.mu.RLock()
	if h.shutdown {
		h.mu.RUnlock()
		return
	}
	for _, env := range evs {
		for s := range h.all {
			if s.filter.MatchEvent(env) {
				h.send(s, Message{Type: MessageEvent, Env: &env})
			}
		}
		if set, ok := h.bySession[env.Event.SessionID]; ok {
			for s := range set {
				if s.filter.MatchEvent(env) {
					h.send(s, Message{Type: MessageEvent, Env: &env})
				}
			}
		}
	}
	for _, summary := range sess {
		for s := range h.all {
			if s.filter.MatchSession(summary) {
				h.send(s, Message{Type: MessageSession, Session: &summary})
			}
		}
		if set, ok := h.bySession[summary.ID]; ok {
			for s := range set {
				if s.filter.MatchSession(summary) {
					h.send(s, Message{Type: MessageSession, Session: &summary})
				}
			}
		}
	}
	h.mu.RUnlock()

	if n := len(evs); n > 0 {
		h.metrics.published.WithLabelValues("event").Add(float64(n))
	}
	if n := len(sess); n > 0 {
		h.metrics.published.WithLabelValues("session").Add(float64(n))
	}
}

// PublishStats delivers s to every live subscriber regardless of topic or
// filter (rule 4): SPEC §5.1's `event: stats` frame carries pipeline
// health, not per-session or per-project data, so nothing about it is
// filterable.
func (h *Hub) PublishStats(s Stats) {
	h.mu.RLock()
	if h.shutdown {
		h.mu.RUnlock()
		return
	}
	msg := Message{Type: MessageStats, Stats: &s}
	for sub := range h.all {
		h.send(sub, msg)
	}
	for _, set := range h.bySession {
		for sub := range set {
			h.send(sub, msg)
		}
	}
	h.mu.RUnlock()

	h.metrics.published.WithLabelValues("stats").Inc()
}

// Shutdown delivers one MessageShutdown to every live subscriber — best
// effort, bypassing the drop-oldest budget so it is never the shutdown
// message itself that gets dropped (SPEC §3.8: "SSE subscribers get a
// final event: shutdown") — then closes every subscriber channel.
// Idempotent: a second call is a no-op. After it returns, Subscribe returns
// ErrClosed and Publish/PublishStats become silent no-ops (the ingest
// Publisher contract, pipeline.go:142-148, explicitly allows Publish calls
// after the pipeline — and by extension its hub — considers itself done).
func (h *Hub) Shutdown() {
	h.mu.Lock()
	if h.shutdown {
		h.mu.Unlock()
		return
	}
	h.shutdown = true

	subs := make([]*Subscription, 0, h.subscriberCount)
	for s := range h.all {
		s.closed = true
		subs = append(subs, s)
	}
	for id, set := range h.bySession {
		for s := range set {
			s.closed = true
			subs = append(subs, s)
		}
		delete(h.bySession, id)
	}
	h.all = make(map[*Subscription]struct{})
	h.subscriberCount = 0
	h.metrics.subscribers.WithLabelValues("all").Set(0)
	h.metrics.subscribers.WithLabelValues("session").Set(0)
	n := len(subs)
	h.mu.Unlock()

	h.logger.Info("stream: hub shutdown", "subscribers", n)

	// Safe without h.mu from here on: every subscriber above is already
	// marked closed and removed from the hub's maps under the lock just
	// released, so no concurrent Publish (gated by h.shutdown, checked
	// under its own RLock, which cannot interleave with the Lock section
	// above) and no concurrent Subscription.Close (gated by s.closed, same
	// reasoning) can touch these channels from here — this goroutine has
	// exclusive ownership of closing them.
	for _, s := range subs {
		sendShutdown(s)
		close(s.ch)
	}
}

// sendShutdown delivers a MessageShutdown to s, evicting one buffered
// message to make room if needed, but — unlike send — never falls back to
// dropping the shutdown message itself: that fallback is unreachable in
// practice, because by the time Shutdown calls this, h.shutdown is already
// true and every other subscriber has already been marked closed and
// removed from the hub's maps (see Shutdown's doc), so nothing else will
// ever send to s.ch again to refill the slot this function just freed. The
// fallback is kept anyway as a safe no-op — never a panic, never a block —
// in case that invariant is ever weakened by a future change.
func sendShutdown(s *Subscription) {
	msg := Message{Type: MessageShutdown}
	select {
	case s.ch <- msg:
		return
	default:
	}
	select {
	case <-s.ch:
	default:
	}
	select {
	case s.ch <- msg:
	default:
	}
}

// Subscribers reports the current live subscriber count, across every
// topic — the readyz endpoint and this package's own tests use it as the
// single source of truth for "how many are connected right now".
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.subscriberCount
}

// DroppedTotal reports the running total of messages dropped across every
// subscriber because that subscriber's own buffer was full (SPEC §5.3
// drop-oldest) — the same number argus_stream_dropped_total exports, kept
// in a plain atomic counter because a Prometheus Counter has no cheap
// synchronous read (hubMetrics.dropped's doc explains why both exist).
// This is the number SPEC §5.1's `event: stats` DroppedTotal field reports
// and P5-05's warn indicator reads.
func (h *Hub) DroppedTotal() int64 {
	return h.droppedTotal.Load()
}
