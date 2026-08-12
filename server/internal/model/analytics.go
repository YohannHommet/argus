package model

import "time"

// Cost is the fleet-level {usd, reported_usd, estimated_usd,
// estimated_share} shape used by GET /api/v1/analytics/summary (SPEC §4.3).
// Unlike SessionCost it carries no by_query_source split — SPEC §4.3 is
// explicit that split lives only on the session, not at fleet level.
type Cost struct {
	USD            float64 `json:"usd"`
	ReportedUSD    float64 `json:"reported_usd"`
	EstimatedUSD   float64 `json:"estimated_usd"`
	EstimatedShare float64 `json:"estimated_share"`
}

// LOC is the {added, removed} lines-of-code shape (SPEC §4.3 analytics
// summary), sourced from the loc metric series (§1.8) — no log-event
// equivalent exists.
type LOC struct {
	Added   int64 `json:"added"`
	Removed int64 `json:"removed"`
}

// Window is the echoed request window on GET /api/v1/analytics/summary
// (SPEC §4.3: "{from, to, bucket}").
type Window struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Bucket string    `json:"bucket"`
}

// Summary is the body of GET /api/v1/analytics/summary / Reader.
// AnalyticsSummary (SPEC §4.3). Every counter that is not attributable
// under a `?model=` filter is a nil pointer, never a zero value (SPEC §4.1
// "Null vs zero", §4.3 "Model-filtered requests") — NotAttributable lists
// which ones.
type Summary struct {
	Window Window `json:"window"`

	Sessions    *int64 `json:"sessions"`
	Turns       *int64 `json:"turns"`
	APIRequests int64  `json:"api_requests"`
	APIErrors   int64  `json:"api_errors"`

	ToolCalls   *int64   `json:"tool_calls"`
	ToolRejects *int64   `json:"tool_rejects"`
	RejectRate  *float64 `json:"reject_rate"`

	Tokens TokenUsage `json:"tokens"`
	Cost   Cost       `json:"cost"`

	LOC           *LOC   `json:"loc"`
	ActiveSeconds *int64 `json:"active_seconds"`

	Source              Source   `json:"source"`
	MetricsOnlyProjects []string `json:"metrics_only_projects"`
	NotAttributable     []string `json:"not_attributable"`
}

// SeriesPoint is one named line of GET /api/v1/analytics/timeseries (SPEC
// §4.3: "{key, values}"), dense and zero-filled server-side to match
// Series.Buckets one-for-one.
type SeriesPoint struct {
	Key    string    `json:"key"`
	Values []float64 `json:"values"`
}

// SeriesOther is the `other` fold-in bucket beyond `limit_series` (SPEC
// §4.3: "Series beyond limit_series fold into other by total desc"). Kept
// keyless since it aggregates many series, not one.
type SeriesOther struct {
	Values []float64 `json:"values"`
}

// Series is the body of GET /api/v1/analytics/timeseries / Reader.
// AnalyticsSeries (SPEC §4.3).
type Series struct {
	Bucket  string        `json:"bucket"`
	Buckets []time.Time   `json:"buckets"`
	Series  []SeriesPoint `json:"series"`
	Other   *SeriesOther  `json:"other,omitempty"`
}

// BreakdownRow is one entry of GET /api/v1/analytics/breakdown (SPEC §4.3:
// "{key, value, share}"). Key is a raw dimension value (tool name, model
// name, or an unconstrained vocabulary like decision_source/query_source),
// so it stays a string.
type BreakdownRow struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
	Share float64 `json:"share"`
}

// Breakdown is the body of GET /api/v1/analytics/breakdown / Reader.
// AnalyticsBreakdown (SPEC §4.3).
type Breakdown struct {
	Dimension string         `json:"dimension"`
	Rows      []BreakdownRow `json:"rows"`
}

// DecisionMatrixRow is one entry of GET /api/v1/analytics/decisions (SPEC
// §4.3): "the matrix the product exists for". BySource keys are raw
// decision_source values (§1.9), unconstrained.
type DecisionMatrixRow struct {
	ToolName   string           `json:"tool_name"`
	Accept     int64            `json:"accept"`
	Reject     int64            `json:"reject"`
	BySource   map[string]int64 `json:"by_source"`
	ExactShare float64          `json:"exact_share"`
	P50WaitMS  *int64           `json:"p50_wait_ms"`
	P95WaitMS  *int64           `json:"p95_wait_ms"`
}

// DecisionMatrix is the body of GET /api/v1/analytics/decisions / Reader.
// AnalyticsDecisions (SPEC §4.3).
type DecisionMatrix struct {
	Rows []DecisionMatrixRow `json:"rows"`
}
