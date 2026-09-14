package model

import "time"

// SubagentNode is one node of the subagent tree (SPEC §4.3),
// with CostUSD and ToolCallCount nil in v1 per SPEC §1.9 (per-node cost not knowable).
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

// SubagentCostAttribution is the cost attribution object alongside the subagent tree (SPEC §4.3, §1.9),
// split by raw query_source value (SPEC §1.9).
type SubagentCostAttribution struct {
	ByQuerySource       map[string]float64 `json:"by_query_source"`
	DominantQuerySource string             `json:"dominant_query_source"`
	OtherQuerySourceUSD float64            `json:"other_query_source_usd"`
	PerNodeAvailable    bool               `json:"per_node_available"` // always false in v1
	Note                string             `json:"note"`
}

// SubagentTree is the response body of GET /api/v1/sessions/{id}/subagents (SPEC §4.3).
type SubagentTree struct {
	Nodes           []SubagentNode          `json:"data"`
	CostAttribution SubagentCostAttribution `json:"cost_attribution"`
}
