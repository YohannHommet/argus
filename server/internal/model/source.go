package model

// Source is the provenance of an event — one of the four closed vocabularies
// SPEC §0 permits (`kind`, `source`, `correlation`, `status`). Unlike Kind it
// has no "unknown" escape hatch because every event Argus ingests arrives
// through exactly one of these three pipelines (SPEC §0's architecture
// diagram) plus the simulator; there is no fourth transport to misclassify
// into.
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
	// CorrelationHeuristic means a hook event without tool_use_id was
	// attached to an OTel call by fallback match (session+prompt+tool+
	// nearest open call within 60s, one-to-one). No v1 feature is
	// load-bearing on this value (SPEC §1.6).
	CorrelationHeuristic Correlation = "heuristic"
)

// SessionStatus is the sessions.status column (SPEC §1.7, §2.1). It is
// Argus-computed state, not vendor vocabulary, so — unlike query_source,
// decision_source, etc. — it is one of the taxonomies SPEC §0 permits to be
// closed.
type SessionStatus string

// SessionStatus constants (SPEC §1.7).
const (
	// SessionStatusUnknown is the stub-on-reference state: a session row
	// exists (referenced by an event) but no session.start has been seen.
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
