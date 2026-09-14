package model

import "time"

// TokenUsage is the token counts shape in session and analytics-summary wire formats (SPEC §4.3).
type TokenUsage struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
}

// SessionCost is the per-session cost object (SPEC §4.3),
// with ByQuerySource as raw, unconstrained query_source values (§1.9).
type SessionCost struct {
	USD                 float64            `json:"usd"`
	ReportedUSD         float64            `json:"reported_usd"`
	EstimatedUSD        float64            `json:"estimated_usd"`
	EstimatedShare      float64            `json:"estimated_share"`
	ByQuerySource       map[string]float64 `json:"by_query_source"`
	DominantQuerySource string             `json:"dominant_query_source"`
	OtherQuerySourceUSD float64            `json:"other_query_source_usd"`
}

// Session mirrors the sessions table (SPEC §2.1), with vendor-supplied fields as plain strings (SPEC §0).
type Session struct {
	ID                string
	Vendor            string
	Project           string
	CWD               string
	Status            SessionStatus
	StartType         string
	EndReason         string
	PermissionMode    string
	AppVersion        string
	Entrypoint        string
	TerminalType      string
	UserEmail         string
	UserAccountUUID   string
	OrganizationID    string
	StartedAt         *time.Time // NULL until session.start seen (§1.7)
	EndedAt           *time.Time
	FirstSeenAt       time.Time
	LastEventAt       time.Time
	EventCount        int64
	TurnCount         int
	ToolCallCount     int
	ToolRejectCount   int
	SubagentCount     int
	ErrorCount        int
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
	CostUSD           float64
	CostEstimatedUSD  float64
	CostByQuerySource map[string]float64 // raw query_source -> summed cost, uninterpreted (§1.9)
	Models            []string
	UpdatedAt         time.Time
}

// SessionSummary is the row shape for GET /api/v1/sessions (SPEC §4.3),
// with Partial reflecting stub-on-reference state (SPEC §1.7).
type SessionSummary struct {
	ID              string        `json:"id"`
	Vendor          string        `json:"vendor"`
	Project         string        `json:"project"`
	CWD             string        `json:"cwd"`
	Status          SessionStatus `json:"status"`
	StartType       string        `json:"start_type"`
	StartedAt       *time.Time    `json:"started_at"`
	EndedAt         *time.Time    `json:"ended_at"`
	LastEventAt     time.Time     `json:"last_event_at"`
	DurationMS      *int64        `json:"duration_ms"`
	TurnCount       int           `json:"turn_count"`
	EventCount      int64         `json:"event_count"`
	ToolCallCount   int           `json:"tool_call_count"`
	ToolRejectCount int           `json:"tool_reject_count"`
	SubagentCount   int           `json:"subagent_count"`
	ErrorCount      int           `json:"error_count"`
	Tokens          TokenUsage    `json:"tokens"`
	Cost            SessionCost   `json:"cost"`
	Models          []string      `json:"models"`
	Partial         bool          `json:"partial"`
	AppVersion      string        `json:"app_version"`
	Entrypoint      string        `json:"entrypoint"`
	TerminalType    string        `json:"terminal_type"`
}

// PermissionModeChange is one entry of SessionDetail.PermissionModeHistory
// (SPEC §4.3: "{ts, from, to, trigger}").
type PermissionModeChange struct {
	TS      time.Time `json:"ts"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Trigger string    `json:"trigger"`
}

// ToolUsageSummary is one entry of SessionDetail.TopTools (SPEC §4.3:
// "{tool_name, calls, rejects, p50_ms}").
type ToolUsageSummary struct {
	ToolName string `json:"tool_name"`
	Calls    int    `json:"calls"`
	Rejects  int    `json:"rejects"`
	P50MS    *int   `json:"p50_ms"`
}

// SessionDecisionSummary is SessionDetail.DecisionSummary (SPEC §4.3:
// "{accept, reject, by_source{…}, exact_share}"). BySource keys are raw
// decision_source values, unconstrained (§1.9).
type SessionDecisionSummary struct {
	Accept     int            `json:"accept"`
	Reject     int            `json:"reject"`
	BySource   map[string]int `json:"by_source"`
	ExactShare float64        `json:"exact_share"`
}

// SessionHookLatency is SessionDetail.HookLatency (SPEC §4.3: "{p50_ms,
// p95_ms, by_hook_event{…}}" or null — nil here when the session has no hook
// coverage).
type SessionHookLatency struct {
	P50MS       int64            `json:"p50_ms"`
	P95MS       int64            `json:"p95_ms"`
	ByHookEvent map[string]int64 `json:"by_hook_event"`
}

// SessionDetail is the row shape for Reader.GetSession / GET
// /api/v1/sessions/{id} (SPEC §4.3): the summary plus the detail-only
// fields that endpoint documents.
type SessionDetail struct {
	SessionSummary
	PermissionModeHistory []PermissionModeChange `json:"permission_mode_history"`
	TopTools              []ToolUsageSummary     `json:"top_tools"`
	DecisionSummary       SessionDecisionSummary `json:"decision_summary"`
	SourcesSeen           []Source               `json:"sources_seen"`
	RawEventsExpired      bool                   `json:"raw_events_expired"`
	HookLatency           *SessionHookLatency    `json:"hook_latency"`
	FirstSeenAt           time.Time              `json:"first_seen_at"`
	User                  string                 `json:"user"`
	OrganizationID        string                 `json:"organization_id"`
}
