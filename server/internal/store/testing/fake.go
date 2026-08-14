// Package testing — fake.go is the shared in-memory test double for
// store.Reader (SPEC §3.3, ticket P3-09), replacing the three near-identical
// local `fakeReader` doubles P3-07/P3-08 built inside internal/httpapi
// (sessions_test.go/events_test.go/toolcalls_test.go/analytics_test.go)
// before this package existed. It is also what internal/httpapi's
// conformance_test.go wires into httpapi.New so the router's real handlers
// run against a fake store instead of postgres (SPEC §4.4: "runs the real
// router over the fake store").
//
// Fake follows the same per-method, settable-func convention every existing
// consumer-owned port in this codebase already uses for its test doubles
// (e.g. internal/ingest/otlp's fakeEnqueuer, httpapi_test's fakeReader): each
// store.Reader method is backed by an exported `*Func` field a caller wires
// up individually, and calling a method whose Func is nil panics loudly
// rather than returning a silent zero value — a test that forgot to stub a
// call path is caught immediately, never mistaken for "the store legitimately
// returned nothing". This makes Fake equally usable two ways: as a
// per-test, narrowly-stubbed mock (httpapi's existing convention, preserved
// verbatim for the tests migrated onto it) and as a fully-populated,
// deterministic fixture store (conformance_test.go's own use, built by
// wiring every Func to return fixed, seeded data with no map-iteration
// order and no time.Now() — SPEC's ticket note that a conformance table must
// never flake).
package testing

import (
	"context"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// Fake is an in-memory store.Reader test double (P3-09). The zero value has
// every Func nil; calling any method before wiring its Func panics (see the
// package doc comment above for why that is the point, not an oversight).
type Fake struct {
	ListSessionsFunc       func(ctx context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error)
	GetSessionFunc         func(ctx context.Context, id string) (*model.SessionDetail, error)
	ListTurnsFunc          func(ctx context.Context, sessionID string) ([]model.Turn, error)
	ListEventsFunc         func(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error)
	GetEventFunc           func(ctx context.Context, ref model.EventRef) (*model.Event, error)
	ListToolCallsFunc      func(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error)
	SubagentTreeFunc       func(ctx context.Context, sessionID string) (model.SubagentTree, error)
	AnalyticsSummaryFunc   func(ctx context.Context, f store.AnalyticsFilter) (model.Summary, error)
	AnalyticsSeriesFunc    func(ctx context.Context, f store.AnalyticsFilter, g store.Grouping) (model.Series, error)
	AnalyticsBreakdownFunc func(ctx context.Context, f store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error)
	AnalyticsDecisionsFunc func(ctx context.Context, f store.AnalyticsFilter) (model.DecisionMatrix, error)
	EventsSinceFunc        func(ctx context.Context, after model.EventRef, windowStart time.Time, limit int) ([]model.Event, error)
	FacetsFunc             func(ctx context.Context) (model.Facets, error)
	DataQualityFunc        func(ctx context.Context) (model.DataQuality, error)
	UnknownKindsFunc       func(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error)
	HookLatencyFunc        func(ctx context.Context, f store.AnalyticsFilter) (model.HookLatency, error)
}

// var _ store.Reader = (*Fake)(nil) pins Fake to the full seam interface at
// compile time — errors.go's own doc comment names exactly this
// requirement ("storetest.Fake cannot signal 404 at all" would be a real
// problem if Fake only implemented a narrower port): a conformance test
// asking the Fake for an unknown id must be able to return
// store.ErrSessionNotFound exactly like postgres does.
var _ store.Reader = (*Fake)(nil)

// ListSessions delegates to f.ListSessionsFunc (see the type doc comment for
// the nil-panics-loudly convention every method here follows).
func (f *Fake) ListSessions(ctx context.Context, filter store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error) {
	if f.ListSessionsFunc == nil {
		panic("storetest.Fake.ListSessions not stubbed")
	}
	return f.ListSessionsFunc(ctx, filter, p)
}

// GetSession delegates to f.GetSessionFunc.
func (f *Fake) GetSession(ctx context.Context, id string) (*model.SessionDetail, error) {
	if f.GetSessionFunc == nil {
		panic("storetest.Fake.GetSession not stubbed")
	}
	return f.GetSessionFunc(ctx, id)
}

// ListTurns delegates to f.ListTurnsFunc.
func (f *Fake) ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error) {
	if f.ListTurnsFunc == nil {
		panic("storetest.Fake.ListTurns not stubbed")
	}
	return f.ListTurnsFunc(ctx, sessionID)
}

// ListEvents delegates to f.ListEventsFunc.
func (f *Fake) ListEvents(ctx context.Context, filter store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error) {
	if f.ListEventsFunc == nil {
		panic("storetest.Fake.ListEvents not stubbed")
	}
	return f.ListEventsFunc(ctx, filter, p)
}

// GetEvent delegates to f.GetEventFunc.
func (f *Fake) GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error) {
	if f.GetEventFunc == nil {
		panic("storetest.Fake.GetEvent not stubbed")
	}
	return f.GetEventFunc(ctx, ref)
}

// ListToolCalls delegates to f.ListToolCallsFunc.
func (f *Fake) ListToolCalls(ctx context.Context, filter store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error) {
	if f.ListToolCallsFunc == nil {
		panic("storetest.Fake.ListToolCalls not stubbed")
	}
	return f.ListToolCallsFunc(ctx, filter, p)
}

// SubagentTree delegates to f.SubagentTreeFunc.
func (f *Fake) SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error) {
	if f.SubagentTreeFunc == nil {
		panic("storetest.Fake.SubagentTree not stubbed")
	}
	return f.SubagentTreeFunc(ctx, sessionID)
}

// AnalyticsSummary delegates to f.AnalyticsSummaryFunc.
func (f *Fake) AnalyticsSummary(ctx context.Context, filter store.AnalyticsFilter) (model.Summary, error) {
	if f.AnalyticsSummaryFunc == nil {
		panic("storetest.Fake.AnalyticsSummary not stubbed")
	}
	return f.AnalyticsSummaryFunc(ctx, filter)
}

// AnalyticsSeries delegates to f.AnalyticsSeriesFunc.
func (f *Fake) AnalyticsSeries(ctx context.Context, filter store.AnalyticsFilter, g store.Grouping) (model.Series, error) {
	if f.AnalyticsSeriesFunc == nil {
		panic("storetest.Fake.AnalyticsSeries not stubbed")
	}
	return f.AnalyticsSeriesFunc(ctx, filter, g)
}

// AnalyticsBreakdown delegates to f.AnalyticsBreakdownFunc.
func (f *Fake) AnalyticsBreakdown(ctx context.Context, filter store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error) {
	if f.AnalyticsBreakdownFunc == nil {
		panic("storetest.Fake.AnalyticsBreakdown not stubbed")
	}
	return f.AnalyticsBreakdownFunc(ctx, filter, d)
}

// AnalyticsDecisions delegates to f.AnalyticsDecisionsFunc.
func (f *Fake) AnalyticsDecisions(ctx context.Context, filter store.AnalyticsFilter) (model.DecisionMatrix, error) {
	if f.AnalyticsDecisionsFunc == nil {
		panic("storetest.Fake.AnalyticsDecisions not stubbed")
	}
	return f.AnalyticsDecisionsFunc(ctx, filter)
}

// EventsSince delegates to f.EventsSinceFunc.
func (f *Fake) EventsSince(ctx context.Context, after model.EventRef, windowStart time.Time, limit int) ([]model.Event, error) {
	if f.EventsSinceFunc == nil {
		panic("storetest.Fake.EventsSince not stubbed")
	}
	return f.EventsSinceFunc(ctx, after, windowStart, limit)
}

// Facets delegates to f.FacetsFunc.
func (f *Fake) Facets(ctx context.Context) (model.Facets, error) {
	if f.FacetsFunc == nil {
		panic("storetest.Fake.Facets not stubbed")
	}
	return f.FacetsFunc(ctx)
}

// DataQuality delegates to f.DataQualityFunc.
func (f *Fake) DataQuality(ctx context.Context) (model.DataQuality, error) {
	if f.DataQualityFunc == nil {
		panic("storetest.Fake.DataQuality not stubbed")
	}
	return f.DataQualityFunc(ctx)
}

// UnknownKinds delegates to f.UnknownKindsFunc.
func (f *Fake) UnknownKinds(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error) {
	if f.UnknownKindsFunc == nil {
		panic("storetest.Fake.UnknownKinds not stubbed")
	}
	return f.UnknownKindsFunc(ctx, since, limit)
}

// HookLatency delegates to f.HookLatencyFunc.
func (f *Fake) HookLatency(ctx context.Context, filter store.AnalyticsFilter) (model.HookLatency, error) {
	if f.HookLatencyFunc == nil {
		panic("storetest.Fake.HookLatency not stubbed")
	}
	return f.HookLatencyFunc(ctx, filter)
}
