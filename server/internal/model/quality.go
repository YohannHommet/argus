package model

import "time"

// Facets is the body of GET /api/v1/facets (SPEC §4.2): filter option lists
// for the session/event/tool-call list UIs. Every slice holds raw
// agent-supplied values (project names, model names, vendors, tools,
// decision_source, query_source) — unconstrained, per SPEC §0.
type Facets struct {
	Projects        []string `json:"projects"`
	Models          []string `json:"models"`
	Vendors         []string `json:"vendors"`
	Tools           []string `json:"tools"`
	DecisionSources []string `json:"decision_sources"`
	QuerySources    []string `json:"query_sources"`
}

// DataQuality is the `data_quality` block of GET /api/v1/meta and the body
// of Reader.DataQuality (SPEC §3.3, §4.2). SPEC §4.3 does not give this
// endpoint a worked JSON example; this struct carries exactly the fields
// SPEC §4.2 names explicitly ("logs_exporter_seen / metrics_exporter_seen /
// hooks_seen / tool_details_seen, data_quality block") and is otherwise
// minimal-but-honest. The owning ticket for GET /api/v1/meta fills in any
// remaining fields (e.g. too_old / dedup counters from §1.7).
type DataQuality struct {
	LogsExporterSeen    bool `json:"logs_exporter_seen"`
	MetricsExporterSeen bool `json:"metrics_exporter_seen"`
	HooksSeen           bool `json:"hooks_seen"`
	ToolDetailsSeen     bool `json:"tool_details_seen"`
}

// UnknownKindGroup is one row of GET /api/v1/quality/unknown-kinds (SPEC
// §4.3): "{event_name, source, count, first_seen, last_seen, sample}" —
// grouped unmapped event_names plus one raw sample each, so an operator can
// see what the normalizer does not yet recognize without losing the event
// (§1.4's "never dropped").
type UnknownKindGroup struct {
	EventName string         `json:"event_name"`
	Source    Source         `json:"source"`
	Count     int64          `json:"count"`
	FirstSeen time.Time      `json:"first_seen"`
	LastSeen  time.Time      `json:"last_seen"`
	Sample    map[string]any `json:"sample"`
}

// HookLatencyRow is one row of GET /api/v1/quality/hook-latency (SPEC
// §4.3): "{hook_event, executions, p50_ms, p95_ms, p99_ms, errors,
// cancelled}" — the latency Argus's own hooks webhook adds to the agent
// (§1.4 hook.execution_end).
type HookLatencyRow struct {
	HookEvent  string `json:"hook_event"`
	Executions int64  `json:"executions"`
	P50MS      int64  `json:"p50_ms"`
	P95MS      int64  `json:"p95_ms"`
	P99MS      int64  `json:"p99_ms"`
	Errors     int64  `json:"errors"`
	Cancelled  int64  `json:"cancelled"`
}

// HookLatency is the body of GET /api/v1/quality/hook-latency / Reader.
// HookLatency (SPEC §4.3).
type HookLatency struct {
	Rows []HookLatencyRow `json:"rows"`
}
