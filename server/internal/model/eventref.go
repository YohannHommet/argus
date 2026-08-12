package model

import "time"

// EventRef identifies one row of the (future) append-only events table by
// its primary key, (ts, seq) — SPEC §3.3's GetEvent and EventsSince key off
// it. Kept to the bare pair the Reader signatures need; the full events
// schema and the Event struct's field set land in P2-01.
type EventRef struct {
	TS  time.Time
	Seq int64
}

// Event is a placeholder for the eventual append-only, normalized event
// record (SPEC §0, §3.1's model.Event). P2-01 owns its real shape,
// including the Kind taxonomy; this stub exists only so Writer.WriteBatch
// and the Reader event methods compile in P1-04.
type Event struct {
	Ref EventRef
}

// ToolCall is a placeholder for Reader.ListToolCalls; full shape in a later
// phase.
type ToolCall struct {
	ID string
}

// SubagentTree is a placeholder for Reader.SubagentTree; full shape in a
// later phase.
type SubagentTree struct{}

// Summary is a placeholder for Reader.AnalyticsSummary; full shape in a
// later phase.
type Summary struct{}

// Series is a placeholder for Reader.AnalyticsSeries; full shape in a later
// phase.
type Series struct{}

// Breakdown is a placeholder for Reader.AnalyticsBreakdown; full shape in a
// later phase.
type Breakdown struct{}

// DecisionMatrix is a placeholder for Reader.AnalyticsDecisions; full shape
// in a later phase.
type DecisionMatrix struct{}

// Facets is a placeholder for Reader.Facets; full shape in a later phase.
type Facets struct{}

// DataQuality is a placeholder for Reader.DataQuality; full shape in a
// later phase.
type DataQuality struct{}

// UnknownKindGroup is a placeholder for Reader.UnknownKinds; full shape in
// a later phase.
type UnknownKindGroup struct{}

// HookLatency is a placeholder for Reader.HookLatency; full shape in a
// later phase.
type HookLatency struct{}
