// Package stream implements Argus's Phase 5 live-stream hub (SPEC §5.3): an
// in-process, single-binary pub/sub broker that fans out persisted events,
// session-projection snapshots, and pipeline-health stats to SSE
// subscribers. There is no Redis — every subscriber lives in this process's
// own memory — so the hub's whole job is bounded fan-out that can never let
// one slow subscriber (a stalled browser tab) become back-pressure on the
// ingest pipeline that calls Publish. That single property — Publish never
// blocks — is this package's reason to exist; every other rule here
// (per-subscriber buffering, drop-oldest, the subscriber cap) exists only
// to protect it.
//
// depguard (SPEC §3.1): stream imports only stdlib, prometheus, and
// internal/model. It knows nothing about HTTP, SSE framing, or the ingest
// pipeline that calls Publish — internal/httpapi and internal/ingest depend
// on stream, never the reverse, so this package cannot know or care how a
// subscriber's frames eventually reach a browser.
package stream

import "github.com/YohannHommet/argus/server/internal/model"

// TopicKind is the closed set of hub fan-out scopes (SPEC §5.3). Values
// start at 1, not 0: a zero-value Topic{} is therefore never a valid
// subscription target, which turns an uninitialized Topic into an error at
// Subscribe time instead of a subscription that silently lands nowhere.
type TopicKind uint8

const (
	// TopicAll is the firehose: every persisted event/session, subject
	// only to Filter (SPEC §5.3; SPEC §4.1's `?kinds=&project=&vendor=`).
	TopicAll TopicKind = iota + 1
	// TopicSession scopes fan-out to one session's events (SPEC §5.3): the
	// hub keeps these subscribers in a map keyed by session id, so
	// publishing an event for session X never walks session Y's
	// subscribers (the fan-out-cost guarantee the ticket names).
	TopicSession
)

// Topic names a Hub subscription's fan-out scope. ID is meaningful only
// when Kind == TopicSession — AllTopic and SessionTopic are the only
// sanctioned constructors, so a caller never has to know that convention to
// use it correctly.
type Topic struct {
	Kind TopicKind
	ID   string
}

// AllTopic returns the firehose topic (SPEC §5.3).
func AllTopic() Topic { return Topic{Kind: TopicAll} }

// SessionTopic returns the topic scoped to one session's events (SPEC
// §5.3).
func SessionTopic(id string) Topic { return Topic{Kind: TopicSession, ID: id} }

// Envelope carries the fields the hub filters on that model.Event itself
// does not have (SPEC §5.3, review finding m5): Project comes from the
// session-projection row the publisher just upserted, never from Event,
// because model.Event has no project field. See Filter.MatchEvent's doc for
// what an empty Project means to a `?project=` filter — the rule that makes
// the firehose's project filter implementable at all despite this gap.
type Envelope struct {
	Event   model.Event
	Project string
}

// Stats is the SPEC §5.1 `event: stats` payload, delivered by
// Hub.PublishStats every ARGUS_STREAM_STATS_INTERVAL (2s). JSON tags are
// pinned field-for-field to server/api/openapi.yaml's StreamStatsFrame
// schema; the SSE handler that owns the actual json.Marshal call is a
// sibling ticket's code, but keeping Stats' tags in sync with that schema
// is this package's responsibility, not that handler's.
type Stats struct {
	EventsPerSec   float64 `json:"events_per_sec"`
	ActiveSessions int     `json:"active_sessions"`
	QueueDepth     int     `json:"queue_depth"`
	IngestLagMS    int64   `json:"ingest_lag_ms"`
	DroppedTotal   int64   `json:"dropped_total"`
}

// MessageType discriminates Message's union. SPEC §5.1 lists six SSE frame
// types (event/session/stats/lag/reset/shutdown); only the first four map
// to a MessageType here — `lag` and `reset` are the SSE handler's own
// synthesized frames (computed from Subscription.TakeDropped and the
// replay-window check respectively), never something the hub itself
// publishes, so they have no Message representation.
type MessageType uint8

// MessageEvent is the SSE frame type for a single ingested event; the
// remaining constants in this block enumerate the other Message-carried
// frame kinds (session summary, stats, shutdown).
const (
	MessageEvent MessageType = iota + 1
	MessageSession
	MessageStats
	MessageShutdown
)

// Message is one item delivered to a subscriber's channel. Exactly one of
// Env/Session/Stats is non-nil, selected by Type (MessageShutdown carries
// none). The pointer fields are shared, read-only across every subscriber
// that receives the same Message — Publish/PublishStats hand the identical
// *Envelope/*Stats/*model.SessionSummary to every matching subscriber
// rather than copying it per-recipient, so a receiver (the SSE handler)
// must treat them as immutable: mutating one subscriber's view would
// corrupt every other subscriber's view of the same event, invisibly and
// without a data race detector able to help, since nothing here promises
// mutation is synchronized.
type Message struct {
	Type    MessageType
	Env     *Envelope
	Session *model.SessionSummary
	Stats   *Stats
}
