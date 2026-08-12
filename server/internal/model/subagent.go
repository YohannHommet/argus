package model

import "time"

// SubagentNode is one node of the tree GET /api/v1/sessions/{id}/subagents
// returns (SPEC §4.3). CostUSD and (when the session has no hook coverage)
// ToolCallCount are nil in v1 — SPEC §1.9 is normative that per-node cost is
// never knowable from Claude Code's telemetry, and any test asserting it
// (or "root aggregates = session totals minus children") is invalid per
// that section.
type SubagentNode struct {
	AgentID        string         `json:"agent_id"`
	ParentAgentID  *string        `json:"parent_agent_id"`
	AgentType      string         `json:"agent_type"`
	Depth          int            `json:"depth"`
	Status         SubagentStatus `json:"status"`
	StartedAt      *time.Time     `json:"started_at"`
	EndedAt        *time.Time     `json:"ended_at"`
	SpawnToolUseID *string        `json:"spawn_tool_use_id,omitempty"`
	ToolCallCount  *int           `json:"tool_call_count"` // nil = no hook coverage, never 0 (§1.9)
	CostUSD        *float64       `json:"cost_usd"`        // always nil in v1 (§1.9)
	Children       []SubagentNode `json:"children"`
}

// SubagentCostAttribution is the `cost_attribution` object alongside the
// subagent tree (SPEC §4.3 and §1.9): the only honest cost split v1 can
// offer — by raw query_source value, never mapped onto a main/subagent
// semantic.
type SubagentCostAttribution struct {
	ByQuerySource       map[string]float64 `json:"by_query_source"`
	DominantQuerySource string             `json:"dominant_query_source"`
	OtherQuerySourceUSD float64            `json:"other_query_source_usd"`
	PerNodeAvailable    bool               `json:"per_node_available"` // always false in v1
	Note                string             `json:"note"`
}

// SubagentTree is the full response body of Reader.SubagentTree / GET
// /api/v1/sessions/{id}/subagents (SPEC §4.3): the assembled tree(s) plus
// the session-level cost attribution that stands in for per-node cost.
type SubagentTree struct {
	Nodes           []SubagentNode          `json:"data"`
	CostAttribution SubagentCostAttribution `json:"cost_attribution"`
}
