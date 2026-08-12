// Package model holds Argus's canonical domain types (docs/SPEC.md §3.1).
// P1-04 defines only what internal/store's Store interface needs to
// compile; the full model — the Event struct, the Kind taxonomy, dedup
// keys, and the rest — arrives in P2-01. Keep additions here minimal.
package model

import "time"

// Session mirrors the sessions table (SPEC §2.1). Note the deliberate
// absence of any Go enum or closed vocabulary for status, start_type,
// end_reason, permission_mode, terminal_type, or vendor: SPEC §0 forbids
// constraining vendor-supplied vocabulary, including in Go types that could
// reject a value.
type Session struct {
	ID                string
	Vendor            string
	Project           string
	CWD               string
	Status            string
	StartType         string
	EndReason         string
	PermissionMode    string
	AppVersion        string
	Entrypoint        string
	TerminalType      string
	UserEmail         string
	UserAccountUUID   string
	OrganizationID    string
	StartedAt         *time.Time
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
	CostByQuerySource map[string]float64
	Models            []string
	UpdatedAt         time.Time
}

// SessionSummary is the row shape for Reader.ListSessions (§3.3). Full
// field set arrives with the sessions list feature (Phase 2+); for now it
// is just enough for the interface to compile.
type SessionSummary struct {
	Session
}

// SessionDetail is the row shape for Reader.GetSession (§3.3).
type SessionDetail struct {
	Session
}
