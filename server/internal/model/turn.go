package model

import "time"

// Turn mirrors the turns table (SPEC §2.1) and is also the row shape for
// Reader.ListTurns / GET /api/v1/sessions/{id}/turns (SPEC §4.2). Status is
// Argus-computed and closed (SPEC §1.7's status rule extends to turns);
// every other field is a plain aggregate, never vendor vocabulary.
type Turn struct {
	SessionID         string     `json:"session_id"`
	PromptID          string     `json:"prompt_id"`
	TurnIndex         *int       `json:"turn_index"`
	StartedAt         *time.Time `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at"`
	FirstSeenAt       time.Time  `json:"first_seen_at"`
	LastEventAt       time.Time  `json:"last_event_at"`
	DurationMS        *int       `json:"duration_ms"`
	Status            TurnStatus `json:"status"`
	APIRequestCount   int        `json:"api_request_count"`
	ToolCallCount     int        `json:"tool_call_count"`
	ToolRejectCount   int        `json:"tool_reject_count"`
	ErrorCount        int        `json:"error_count"`
	InputTokens       int64      `json:"input_tokens"`
	OutputTokens      int64      `json:"output_tokens"`
	CacheReadTokens   int64      `json:"cache_read_tokens"`
	CacheCreateTokens int64      `json:"cache_creation_tokens"`
	CostUSD           float64    `json:"cost_usd"`
	CostEstimatedUSD  float64    `json:"cost_estimated_usd"`
	Models            []string   `json:"models"`
}
