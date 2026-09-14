package model

// Source is the event provenance (SPEC §0), one of four closed vocabularies.
// Unlike Kind, it has no "unknown" escape hatch (SPEC §0's architecture diagram).
type Source string

// Source constants (SPEC §1.3 `source` column).
const (
	SourceOTelLog    Source = "otel_log"
	SourceOTelMetric Source = "otel_metric"
	SourceHook       Source = "hook"
	SourceSim        Source = "sim"
)

// Correlation is how confidently a tool_calls row joins its OTel and hook
// halves (SPEC §1.6 "Tool-call correlation"). Closed per SPEC §0.
type Correlation string

// Correlation constants (SPEC §1.6).
const (
	// CorrelationExact means tool_use_id joined ≥1 OTel and ≥1 hook event.
	CorrelationExact Correlation = "exact"
	// CorrelationOTelOnly means tool_use_id is present but only OTel events
	// carry it.
	CorrelationOTelOnly Correlation = "otel_only"
	// CorrelationHookOnly means no tool_use_id exists anywhere for this call.
	CorrelationHookOnly Correlation = "hook_only"
	// CorrelationHeuristic means fallback match by session+prompt+tool (SPEC §1.6, not load-bearing).
	CorrelationHeuristic Correlation = "heuristic"
)

// SessionStatus is the sessions.status column (SPEC §1.7, §2.1), Argus-computed, one of four closed taxonomies (SPEC §0).
type SessionStatus string

// SessionStatus constants (SPEC §1.7).
const (
	// SessionStatusUnknown is the stub-on-reference state: row exists but no session.start seen.
	SessionStatusUnknown   SessionStatus = "unknown"
	SessionStatusActive    SessionStatus = "active"
	SessionStatusEnded     SessionStatus = "ended"
	SessionStatusAbandoned SessionStatus = "abandoned"
)

// TurnStatus is the turns.status column (SPEC §2.1).
type TurnStatus string

// TurnStatus constants (SPEC §2.1).
const (
	TurnStatusOpen     TurnStatus = "open"
	TurnStatusComplete TurnStatus = "complete"
	TurnStatusFailed   TurnStatus = "failed"
)

// SubagentStatus is the subagents.status column (SPEC §2.3).
type SubagentStatus string

// SubagentStatus constants (SPEC §2.3).
const (
	SubagentStatusRunning  SubagentStatus = "running"
	SubagentStatusComplete SubagentStatus = "complete"
	SubagentStatusFailed   SubagentStatus = "failed"
	SubagentStatusUnknown  SubagentStatus = "unknown"
)
