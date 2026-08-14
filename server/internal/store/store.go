// Package store defines Argus's storage seam (docs/SPEC.md §3.3): the
// Store interface that internal/ingest writes through and internal/query
// reads through, plus the filter/pagination/result types its methods share.
// internal/store/postgres provides the production implementation;
// internal/store/testing provides an integration-test harness.
//
// P1-04 declares the complete interface — every method from SPEC §3.3 — so
// later tickets fill in bodies without ever touching this file again.
// postgres.Store implements Health, Close, and Migrate for real; every
// other method returns ErrNotImplemented until its owning ticket lands.
package store

import (
	"context"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
)

// Store is the full storage seam: everything ingest, query, and the
// background jobs need from a backend.
type Store interface {
	Writer
	Reader
	Maintenance
	Health(ctx context.Context) error
	Close()
}

// Writer is what ingest needs. Batches are the unit of work.
type Writer interface {
	// WriteBatch gates on ingest_dedup, inserts events, and updates all projections plus
	// rollup_dirty in ONE transaction, honouring the lock-ordering invariant (§1.6). Returns
	// per-event outcomes so ingest can count dedup suppressions and fan out to stream only the
	// events that were actually persisted.
	WriteBatch(ctx context.Context, b []model.Event) (BatchResult, error)

	// WriteMetrics is a P2-06 deviation, recorded here rather than silently added: SPEC §3.3's
	// Writer only lists WriteBatch, but §1.8/§2.3 require OTLP metric data points to land in
	// metric_samples, and P2-04 hands them to the store as []model.MetricSample (never
	// model.Event — there is no Kind for a metric, SPEC §1.4). WriteMetrics gates on the same
	// ingest_dedup ledger (the "metric:" key form), inserts into metric_samples, and marks
	// rollup_dirty with source='metric', all in one transaction, in the same relative lock
	// order as WriteBatch (dedup -> metric_samples -> rollup_dirty).
	WriteMetrics(ctx context.Context, samples []model.MetricSample) (BatchResult, error)
}

// Reader is what internal/query needs to serve the HTTP API.
type Reader interface {
	ListSessions(ctx context.Context, f SessionFilter, p Page) ([]model.SessionSummary, Cursor, error)
	GetSession(ctx context.Context, id string) (*model.SessionDetail, error)
	ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error)
	ListEvents(ctx context.Context, f EventFilter, p Page) ([]model.Event, Cursor, error)
	GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error) // PK lookup (ts, seq)
	ListToolCalls(ctx context.Context, f ToolCallFilter, p Page) ([]model.ToolCall, Cursor, error)
	SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error)
	AnalyticsSummary(ctx context.Context, f AnalyticsFilter) (model.Summary, error)
	AnalyticsSeries(ctx context.Context, f AnalyticsFilter, g Grouping) (model.Series, error)
	AnalyticsBreakdown(ctx context.Context, f AnalyticsFilter, d Dimension) (model.Breakdown, error)
	AnalyticsDecisions(ctx context.Context, f AnalyticsFilter) (model.DecisionMatrix, error)
	// EventsSince is bounded by ts so it rides the (ts, seq) primary key and prunes partitions.
	EventsSince(ctx context.Context, after model.EventRef, windowStart time.Time, limit int) ([]model.Event, error)
	Facets(ctx context.Context) (model.Facets, error)
	DataQuality(ctx context.Context) (model.DataQuality, error)
	UnknownKinds(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error)
	HookLatency(ctx context.Context, f AnalyticsFilter) (model.HookLatency, error)
}

// Maintenance is the seam the background jobs (partitions, rollups, sweep,
// retention) drive. It is allowed to be backend-specific in v2 (ClickHouse
// reaches it through a type assertion, per SPEC §3.3) — postgres.Store
// implements all of it, but only Migrate has a real body in P1-04.
type Maintenance interface {
	Migrate(ctx context.Context) error
	// MigrationsCurrent reports whether every migration goose knows about
	// (embedded in the running binary) has been applied to this database —
	// true iff none are pending. It backs GET /readyz's "migrations":
	// "current" condition (SPEC §3.8; Phase-1 deviation D-5 recorded that
	// /readyz previously asserted this without checking). Wired into
	// httpapi by P2-09, not here.
	MigrationsCurrent(ctx context.Context) (bool, error)
	EnsurePartitions(ctx context.Context, from, to time.Time) error
	RunRollups(ctx context.Context, maxBuckets int) (RollupStats, error)
	SweepAbandoned(ctx context.Context, idle time.Duration) (int64, error)
	ApplyRetention(ctx context.Context, cutoff time.Time, dryRun bool) ([]string, error)
	PruneDedup(ctx context.Context, cutoff time.Time) (int64, error)
	RebuildProjections(ctx context.Context, fromTS time.Time) error
}

// The types below are minimal placeholders referenced by the Reader and
// Writer signatures above. Later phases (query filters, keyset pagination,
// rollup jobs) flesh them out; P1-04's job is only to make the interface
// compile and name the right shapes.

// SessionSort is the closed set of GET /api/v1/sessions sort keys (SPEC
// §4.3), each backed by one of the §2.1 `sessions_*` composite indexes with
// `id DESC` as the keyset tiebreak. Desc-only: SPEC §4.3 offers no ascending
// option. Closed because it names Argus's own query surface, not vendor
// vocabulary (unlike the string filter fields on SessionFilter below).
type SessionSort string

// SessionSort constants (SPEC §4.3: "sort ∈ last_event_at|started_at|cost_usd|event_count").
const (
	SessionSortLastEventAt SessionSort = "last_event_at"
	SessionSortStartedAt   SessionSort = "started_at"
	SessionSortCostUSD     SessionSort = "cost_usd"
	SessionSortEventCount  SessionSort = "event_count"
)

// SessionFilter narrows Reader.ListSessions (SPEC §4.3). Every slice field
// is an OR-set within that field; non-empty fields AND together (SPEC
// §4.1: "repeated params OR within a field, AND across fields"). An empty
// slice/zero value means "no restriction" on that field.
//
//   - Project, Vendor filter the sessions row's own columns directly.
//   - Model filters sessions.models (SPEC §2.1's text[] of every model used
//     in the session) — "session used at least one of these models".
//   - Status uses the stored sessions.status column verbatim (SPEC §1.7):
//     Argus-computed state, not vendor vocabulary, despite sitting beside
//     filters that are.
//   - Tool, DecisionSource filter on sessions that have at least one
//     tool_calls row matching (an EXISTS correlation), since neither is a
//     column on sessions itself.
//   - From/To bound sessions.last_event_at. SPEC §4.1 states only that the
//     session list's default window is unbounded, not which timestamp
//     column an explicit window applies to; last_event_at is the natural
//     "session activity in this window" reading, and matches the endpoint's
//     own default sort key — an assumption worth flagging, not a spec
//     citation.
//   - Q is a substring match on id/project/cwd (SPEC §4.3).
//   - Sort selects the keyset sort key; the zero value means
//     SessionSortLastEventAt (SPEC §4.3's documented default).
type SessionFilter struct {
	Project        []string
	Vendor         []string
	Model          []string
	Status         []model.SessionStatus
	Tool           []string
	DecisionSource []string
	From           *time.Time
	To             *time.Time
	Q              string
	Sort           SessionSort
}

// SortOrder is the asc/desc toggle SPEC §4.3 exposes on `GET
// /api/v1/sessions/{id}/timeline` and `GET /api/v1/events` (`order=asc|desc`,
// default asc). Closed because it names Argus's own query surface, not
// vendor vocabulary — the same reasoning as SessionSort.
type SortOrder string

// SortOrder constants (SPEC §4.3: "order=asc|desc").
const (
	OrderAsc  SortOrder = "asc"
	OrderDesc SortOrder = "desc"
)

// Fields is the slim/full toggle SPEC §4.1/§4.3 exposes on event reads
// (`fields=slim|full`, default slim): whether the response — and, per the
// ticket note, the underlying query itself — includes `attrs`. Closed for
// the same reason as SortOrder: it is Argus's own wire concept, not vendor
// vocabulary.
type Fields string

// Fields constants (SPEC §4.1: "fields=slim (the timeline default) omits
// attrs").
const (
	FieldsSlim Fields = "slim"
	FieldsFull Fields = "full"
)

// EventFilter narrows Reader.ListEvents (SPEC §4.3's getSessionTimeline and
// listEvents shapes, which share one store-level method). Every slice field
// is an OR-set within that field; non-empty fields AND together (SPEC
// §4.1). An empty slice/zero value means "no restriction" on that field.
//
//   - SessionID scopes to one session's timeline (`GET
//     /api/v1/sessions/{id}/timeline`) when non-empty; "" is the
//     cross-session search (`GET /api/v1/events`).
//   - Kinds, Tool, DecisionSource, Vendor filter the events row's own
//     columns directly (kind, tool_name, decision_source, vendor).
//   - PromptID, AgentID are single-value equality filters (SPEC
//     openapi.yaml's PromptID/AgentID parameters are plain strings, not
//     repeated) — "" means no restriction.
//   - Project has no column on events; it filters sessions that have this
//     project, via an EXISTS correlation on session_id (SPEC §4.2's
//     cross-session `/events` exposes a `project` param, but SPEC's §2.2
//     `events` table has no project column of its own).
//   - From/To bound `ts` (SPEC §4.1's default time-param semantics).
//   - Order selects (ts, vendor_seq NULLS LAST, seq) ascending or its exact
//     reverse (SPEC §1.2, §4.3); the zero value means OrderAsc.
//   - Fields selects whether `attrs` is read out of the database at all
//     (SPEC §4.1: fields=slim omits attrs; the point is not transferring
//     it, not just hiding it on the wire); the zero value means
//     FieldsSlim.
type EventFilter struct {
	SessionID      string
	Kinds          []model.Kind
	PromptID       string
	AgentID        string
	Tool           []string
	DecisionSource []string
	Project        []string
	Vendor         []string
	From           *time.Time
	To             *time.Time
	Order          SortOrder
	Fields         Fields
}

// ToolCallFilter narrows Reader.ListToolCalls (SPEC §4.2's
// listSessionToolCalls and listToolCalls shapes, which share one
// store-level method — the latter is, per SPEC §4.2, "the decision-
// provenance drill-down" the analytics decision matrix links into). Every
// slice field is an OR-set within that field; non-empty fields AND
// together (SPEC §4.1).
//
//   - SessionID scopes to one session's tool calls (`GET
//     /api/v1/sessions/{id}/tool-calls`) when non-empty; "" is the
//     cross-session drill-down (`GET /api/v1/tool-calls`).
//   - Tool, DecisionSource filter tool_calls' own columns (tool_name,
//     decision_source).
//   - Project has no column on tool_calls; it filters sessions that have
//     this project, via an EXISTS correlation on session_id, matching
//     EventFilter.Project's reasoning.
//   - From/To bound `started_at` (SPEC §2.3's `tool_calls (session_id,
//     started_at)` / `(tool_name, started_at DESC)` indexes).
type ToolCallFilter struct {
	SessionID      string
	Project        []string
	Tool           []string
	DecisionSource []string
	From           *time.Time
	To             *time.Time
}

// AnalyticsSource is the `?source=event|metric` toggle on every analytics
// endpoint (SPEC §4.3, openapi.yaml's AnalyticsSource parameter):
// "source='event' and source='metric' rows are never summed together"
// (SPEC §2.4). Closed, unlike the vendor-vocabulary filter fields beside
// it on AnalyticsFilter — it is Argus's own request/response toggle naming
// which rollup partition to aggregate ("Argus-invented toggle, not vendor
// vocabulary", openapi's own description), the same reasoning as
// SessionSort/SortOrder/Fields.
type AnalyticsSource string

// AnalyticsSource constants (SPEC §4.3/§2.4). The zero value is treated as
// AnalyticsSourceEvent by every Analytics* method (openapi's documented
// default).
const (
	AnalyticsSourceEvent  AnalyticsSource = "event"
	AnalyticsSourceMetric AnalyticsSource = "metric"
)

// TimeseriesMetric is the closed set GET /api/v1/analytics/timeseries can
// plot (SPEC §4.3: "metric=cost|tokens|sessions|turns|api_requests|
// api_errors|tool_calls|tool_rejects|loc"). Closed for the same reason as
// AnalyticsSource: it names Argus's own query surface. Cost/Tokens/APIRequests/
// APIErrors are the model-attributable subset (SPEC §4.3's "Model-filtered
// requests" paragraph: "only llm.request events carry a model, so only
// these counters are model-attributable"); every other value is not, and
// AnalyticsSeries returns store.ErrNotAttributable for one of them under an
// active `?model=` filter rather than a silently empty series.
type TimeseriesMetric string

// TimeseriesMetric constants (SPEC §4.3).
const (
	MetricCost        TimeseriesMetric = "cost"
	MetricTokens      TimeseriesMetric = "tokens"
	MetricSessions    TimeseriesMetric = "sessions"
	MetricTurns       TimeseriesMetric = "turns"
	MetricAPIRequests TimeseriesMetric = "api_requests"
	MetricAPIErrors   TimeseriesMetric = "api_errors"
	MetricToolCalls   TimeseriesMetric = "tool_calls"
	MetricToolRejects TimeseriesMetric = "tool_rejects"
	MetricLOC         TimeseriesMetric = "loc"
)

// modelAttributableMetrics is SPEC §4.3's model-attributable subset of
// TimeseriesMetric: "only llm.request events carry a model, so only these
// counters are model-attributable: api_requests, api_errors, tokens.*,
// cost.*". Every other TimeseriesMetric is not attributable to a model.
var modelAttributableMetrics = map[TimeseriesMetric]bool{
	MetricCost:        true,
	MetricTokens:      true,
	MetricAPIRequests: true,
	MetricAPIErrors:   true,
}

// Attributable reports whether m is one of SPEC §4.3's model-attributable
// counters (SPEC §4.3: "only these counters are model-attributable:
// api_requests, api_errors, tokens.*, cost.*").
func (m TimeseriesMetric) Attributable() bool {
	return modelAttributableMetrics[m]
}

// GroupBy is GET /api/v1/analytics/timeseries's `group_by` parameter (SPEC
// §4.3: "group_by=project|model|vendor|none"). Closed, naming Argus's own
// query surface.
type GroupBy string

// GroupBy constants (SPEC §4.3). The zero value is treated as GroupByNone.
const (
	GroupByProject GroupBy = "project"
	GroupByModel   GroupBy = "model"
	GroupByVendor  GroupBy = "vendor"
	GroupByNone    GroupBy = "none"
)

// AnalyticsBucket is GET /api/v1/analytics/timeseries's `bucket` parameter
// (SPEC §4.3: "bucket=hour|day ... defaults to hour for windows <= 7 days,
// day beyond"). Closed, naming Argus's own query surface.
type AnalyticsBucket string

// AnalyticsBucket constants (SPEC §4.3). The zero value means "auto-select
// per SPEC §4.3's window-length rule" — AnalyticsSeries, not its caller,
// resolves it.
const (
	BucketHour AnalyticsBucket = "hour"
	BucketDay  AnalyticsBucket = "day"
)

// AnalyticsDimension is GET /api/v1/analytics/breakdown's `dimension`
// parameter (SPEC §4.3: "dimension=model|project|tool|decision_source|
// query_source|error_type"). Closed — per openapi.yaml's own description,
// it is "the Argus-invented selector naming which raw dimension to break
// down by — not the vendor value itself, which stays an unconstrained
// string in the response rows" (BreakdownRow.Key).
type AnalyticsDimension string

// AnalyticsDimension constants (SPEC §4.3).
const (
	DimensionModel          AnalyticsDimension = "model"
	DimensionProject        AnalyticsDimension = "project"
	DimensionTool           AnalyticsDimension = "tool"
	DimensionDecisionSource AnalyticsDimension = "decision_source"
	DimensionQuerySource    AnalyticsDimension = "query_source"
	DimensionErrorType      AnalyticsDimension = "error_type"
)

// BreakdownMetric is GET /api/v1/analytics/breakdown's `metric` parameter
// (SPEC §4.3: "metric=cost|calls|tokens"). Closed, naming Argus's own query
// surface.
type BreakdownMetric string

// BreakdownMetric constants (SPEC §4.3). The zero value is treated as
// BreakdownMetricCalls (openapi's documented default).
const (
	BreakdownMetricCost   BreakdownMetric = "cost"
	BreakdownMetricCalls  BreakdownMetric = "calls"
	BreakdownMetricTokens BreakdownMetric = "tokens"
)

// AnalyticsFilter narrows every Reader.Analytics* method plus HookLatency
// (SPEC §4.3's shared `from`/`to`/`project`/`model`/`vendor`/`source`
// query parameters). Project/Model/Vendor follow SessionFilter's own
// convention: an OR-set within the field, empty means "no restriction"
// (SPEC §4.1). From/To bound the rollup `bucket` column (AnalyticsSeries/
// AnalyticsSummary/AnalyticsBreakdown) or `tool_calls.started_at`
// (AnalyticsDecisions) — whichever table a given method reads.
//
//   - Model is the trigger for SPEC §4.3's model-attributability rule:
//     when non-empty, every non-model-attributable counter is nil/omitted
//     and named in Summary.NotAttributable, and AnalyticsSeries/
//     AnalyticsBreakdown return ErrNotAttributable for a non-attributable
//     metric/dimension rather than a silently empty result.
//   - Source selects which rollup partition to aggregate (SPEC §2.4:
//     'event' and 'metric' rows are never summed together); the zero
//     value means AnalyticsSourceEvent. AnalyticsDecisions ignores it —
//     openapi.yaml's decisions endpoint has no `source` parameter, since
//     tool_calls carries no event/metric split.
type AnalyticsFilter struct {
	From    *time.Time
	To      *time.Time
	Project []string
	Model   []string
	Vendor  []string
	Source  AnalyticsSource
}

// Grouping narrows Reader.AnalyticsSeries (SPEC §4.3's getAnalyticsTimeseries
// shape): which metric to plot, at what bucket size, split into how many
// series.
//
//   - Metric selects the single TimeseriesMetric plotted; required (openapi
//     marks `metric` required, no zero-value default).
//   - Bucket is the zero value (auto-select, SPEC §4.3's window-length
//     rule) unless the caller pins hour|day explicitly.
//   - GroupBy is the zero value (treated as GroupByNone: one series) unless
//     the caller requests a split.
//   - LimitSeries caps how many GroupBy series render individually before
//     the remainder folds into `other` by total desc (SPEC §4.3); <= 0
//     means AnalyticsSeries' own default (openapi: 8).
type Grouping struct {
	Metric      TimeseriesMetric
	Bucket      AnalyticsBucket
	GroupBy     GroupBy
	LimitSeries int
}

// Dimension narrows Reader.AnalyticsBreakdown (SPEC §4.3's
// getAnalyticsBreakdown shape): which raw dimension to group by, which
// metric to rank rows on, how many rows to return.
//
//   - Name selects the dimension; required (openapi marks `dimension`
//     required, no zero-value default).
//   - Metric is the zero value (treated as BreakdownMetricCalls, openapi's
//     documented default) unless the caller requests cost|tokens — and is
//     ignored outright for Name values with no such data at all
//     (Tool/DecisionSource/ErrorType always report call counts; QuerySource
//     always reports cost — see read_analytics.go).
//   - Limit caps the row count (openapi: default 20, max 500); <= 0 means
//     AnalyticsBreakdown's own default.
type Dimension struct {
	Name   AnalyticsDimension
	Metric BreakdownMetric
	Limit  int
}

// Page is a keyset pagination request: an opaque cursor plus a page size.
type Page struct {
	Cursor Cursor
	Limit  int
}

// Cursor is an opaque keyset pagination cursor. internal/httpapi owns its
// wire codec; store only passes it through.
type Cursor string

// BatchResult reports Writer.WriteBatch/WriteMetrics per-batch outcomes so
// ingest can count dedup suppressions, count and log too-old rejections
// separately (SPEC §1.7 rule 3: argus_ingest_too_old_total), and fan out
// only persisted events to the stream hub.
//
// Rejected is currently always equal to TooOld: too_old (no partition to
// land in) is the only rejection reason WriteBatch/WriteMetrics classify in
// P2-06. It is kept as its own field, distinct from TooOld, because a future
// rejection reason (e.g. a malformed row caught defensively) would add to
// Rejected without being a TooOld — callers that only care about "how many
// events did not make it in" should read Rejected, callers that need the
// SPEC §1.7 metric specifically should read TooOld.
type BatchResult struct {
	Written   int
	Deduped   int
	TooOld    int
	Rejected  int
	EventRefs []model.EventRef
}

// RollupStats reports what Maintenance.RunRollups did in one pass.
type RollupStats struct {
	BucketsClaimed    int
	BucketsRecomputed int
}
