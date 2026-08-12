package model

import "time"

// Event is the append-only, normalized event record (SPEC §0, §1.3, §2.2):
// the atom of the system, and the only table every projection is derived
// from. Field set and nullability mirror the `events` table in SPEC §2.2
// exactly, column for column — pointers for every nullable column, plain
// values for every NOT NULL one. `Attrs` carries the full flattened source
// payload verbatim (SPEC §1.3: "promotion is a copy, not a move"), so a
// projection rebuild never needs a schema migration.
//
// Several fields (QuerySource, Decision, DecisionSource, ToolSource,
// PermissionMode, Model, ErrorType, and the vendor/terminal/start/end-reason
// fields on Session/Turn) are deliberately plain *string: SPEC §0 forbids
// any Go type that could reject a vendor-supplied value, and the live
// capture (docs/research/live-capture-2026-08-11.md) found values
// (`query_source: generate_session_title`, `terminal.type: wsl-Ubuntu`) the
// documentation does not list.
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

	// EventName is the vendor raw event name, normalized to its unprefixed
	// form (§1.5.1). Unconstrained text, not a Kind: the taxonomy lives in
	// Kind, EventName is provenance for debugging and the unknown-kind
	// inspector.
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

// MetricSample mirrors the metric_samples table (SPEC §2.3, §1.8). OTLP
// metric data points are stored here, never in Event — there is no
// Kind for a metric (§1.4) — and feed only rollup series log events cannot
// produce, plus the degraded mode when the logs exporter is off.
type MetricSample struct {
	TS         time.Time
	IngestedAt time.Time

	Name   string
	Vendor string

	SessionID *string // OTEL_METRICS_INCLUDE_SESSION_ID may be false (§1.8) — nullable

	Value float64
	Delta *float64 // filled by the rollup job for cumulative series (§1.8)

	// Temporality is delta|cumulative|gauge — an OTel wire concept Argus
	// records as reported, not a Go enum: it is not one of the four
	// taxonomies SPEC §0 closes, and constraining it would require deciding
	// in Go what a future OTel temporality value means.
	Temporality string

	SeriesHash []byte // sha256(name + sorted attrs) — series identity (§2.3)

	Attrs map[string]any

	DedupKey string // "metric:{sha256_16(name|ts|canonical_attrs)}" (§1.7 rule 2)
}
