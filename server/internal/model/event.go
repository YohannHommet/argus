package model

import "time"

// Event is the append-only, normalized event record (SPEC §0, §1.3, §2.2),
// with field set and nullability mirroring the `events` table exactly (SPEC §2.2).
//
// Several fields are deliberately plain *string to accept vendor-supplied values unconstrained (SPEC §0).
type Event struct {
	Seq        int64     // bigint identity — ordering tiebreak, cursor component (§1.2)
	ID         string    // uuidv7, opaque, not indexed (§1.2)
	TS         time.Time // agent-reported event time, clamped (§1.2)
	IngestedAt time.Time // server clock

	SessionID string
	PromptID  *string // null outside a turn (§1.1)

	// Vendor is agent-agnostic core text (`claude_code`, `codex`,
	// `gemini_cli`, `unknown`) — deliberately unconstrained (SPEC §0): it is
	// not one of the four closed taxonomies (kind, source, correlation,
	// status), only Source is.
	Vendor string
	Source Source
	Kind   Kind

	// EventName is the vendor raw event name, normalized to unprefixed form (§1.5.1) — provenance for debugging.
	EventName string

	VendorSeq *int64 // OTel event.sequence; nil ⇒ hash-fallback dedup form (§1.7 rule 2)

	ToolName  *string
	ToolUseID *string

	// Decision, DecisionSource, ToolSource, QuerySource: unconstrained
	// (SPEC §0, §1.9). No Go enum, ever.
	Decision       *string
	DecisionSource *string
	ToolSource     *string
	QuerySource    *string

	Model *string

	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64

	CostUSD    *float64
	CostSource *string // "reported" | "estimated" (DECISIONS.md §Cost) — not a Go enum, documented only

	DurationMS *int
	Success    *bool
	ErrorType  *string

	// AgentID, ParentAgentID, AgentType: hook payloads only, never on OTel
	// log events (§1.9).
	AgentID       *string
	ParentAgentID *string
	AgentType     *string

	PermissionMode *string
	FilePath       *string

	RequestID   *string
	MessageUUID *string

	ClockSkewed bool // set by ClampTimestamp when ts falls outside the clamp window (§1.2)

	// Attrs is the full flattened source payload, including the promoted
	// fields above (§1.3). map[string]any so DedupKey's canonical-JSON
	// hasher can marshal it directly.
	Attrs map[string]any

	DedupKey string // idempotency key (§1.7 rule 2)
}

// MetricSample mirrors the metric_samples table (SPEC §2.3, §1.8), storing OTLP metric data not in Event.
type MetricSample struct {
	TS         time.Time
	IngestedAt time.Time

	Name   string
	Vendor string

	SessionID *string // OTEL_METRICS_INCLUDE_SESSION_ID may be false (§1.8) — nullable

	Value float64
	Delta *float64 // filled by the rollup job for cumulative series (§1.8)

	// Temporality is delta|cumulative|gauge — recorded as-reported per OTel, not constrained (SPEC §0).
	Temporality string

	SeriesHash []byte // sha256(name + sorted attrs) — series identity (§2.3)

	Attrs map[string]any

	DedupKey string // "metric:{sha256_16(name|ts|canonical_attrs)}" (§1.7 rule 2)
}
