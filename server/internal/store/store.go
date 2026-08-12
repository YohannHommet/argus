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

// Writer is what ingest needs. One method: batches are the unit of work.
type Writer interface {
	// WriteBatch gates on ingest_dedup, inserts events, and updates all projections plus
	// rollup_dirty in ONE transaction, honouring the lock-ordering invariant (§1.6). Returns
	// per-event outcomes so ingest can count dedup suppressions and fan out to stream only the
	// events that were actually persisted.
	WriteBatch(ctx context.Context, b []model.Event) (BatchResult, error)
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

// SessionFilter narrows Reader.ListSessions. Full filter set arrives with
// the sessions list feature.
type SessionFilter struct{}

// EventFilter narrows Reader.ListEvents.
type EventFilter struct{}

// ToolCallFilter narrows Reader.ListToolCalls.
type ToolCallFilter struct{}

// AnalyticsFilter narrows the Reader.Analytics* methods and HookLatency.
type AnalyticsFilter struct{}

// Grouping selects the time-bucketing for Reader.AnalyticsSeries.
type Grouping struct{}

// Dimension selects the breakdown dimension for Reader.AnalyticsBreakdown.
type Dimension struct{}

// Page is a keyset pagination request: an opaque cursor plus a page size.
type Page struct {
	Cursor Cursor
	Limit  int
}

// Cursor is an opaque keyset pagination cursor. internal/httpapi owns its
// wire codec; store only passes it through.
type Cursor string

// BatchResult reports Writer.WriteBatch's per-event outcomes so ingest can
// count dedup suppressions and fan out only persisted events to the stream
// hub.
type BatchResult struct {
	Written   int
	Deduped   int
	EventRefs []model.EventRef
}

// RollupStats reports what Maintenance.RunRollups did in one pass.
type RollupStats struct {
	BucketsClaimed    int
	BucketsRecomputed int
}
