package model

import "time"

// ToolCall mirrors the tool_calls table (SPEC §2.3), joining decision, provenance, outcome, and timing.
// Decision/DecisionSource/ToolSource/PermissionMode are unconstrained (SPEC §0, §1.9); Correlation is closed.
type ToolCall struct {
	ID        string // deterministic UUIDv5 computed in Go (§1.6)
	SessionID string
	PromptID  *string
	ToolUseID *string
	ToolName  string

	ToolSource     *string
	AgentID        *string // hook-sourced only (§1.9)
	Decision       *string
	DecisionSource *string
	PermissionMode *string

	StartedAt time.Time
	DecidedAt *time.Time
	EndedAt   *time.Time

	DurationMS *int
	WaitMS     *int // decided_at - started_at: human/permission latency (§2.3)

	Success   *bool
	ErrorType *string
	FilePath  *string

	InputSizeBytes  *int
	ResultSizeBytes *int

	Correlation Correlation
	EventCount  int
}
