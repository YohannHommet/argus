package model

import "time"

// Turn mirrors the turns table (SPEC §2.1). Status is deliberately a plain
// string, not a Go enum — see the note on Session.Status.
type Turn struct {
	SessionID         string
	PromptID          string
	TurnIndex         *int
	StartedAt         *time.Time
	EndedAt           *time.Time
	FirstSeenAt       time.Time
	LastEventAt       time.Time
	DurationMS        *int
	Status            string
	APIRequestCount   int
	ToolCallCount     int
	ToolRejectCount   int
	ErrorCount        int
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
	CostUSD           float64
	CostEstimatedUSD  float64
	Models            []string
}
