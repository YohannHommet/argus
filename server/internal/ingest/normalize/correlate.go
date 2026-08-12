// Package normalize — correlate.go implements SPEC §1.6's "tool_calls"
// projection logic that must stay pure Go with no store dependency
// (depguard: normalize must not import internal/store): the deterministic
// UUIDv5 id, extracting what one candidate event contributes to a
// tool_calls row, and the one-to-one nearest-open-call heuristic
// (SPEC §1.6: "confined to one function, ingest/normalize.correlateToolCall").
//
// # The normalize/store split (lead note 1)
//
// This file computes three things purely, with no I/O:
//  1. ToolCallID — the deterministic id (SPEC §1.6), a pure hash.
//  2. ExtractContribution — what one event contributes to a tool_calls row
//     (which fields, at what rank, from which source/kind), read entirely
//     off model.Event's already-promoted columns plus the two
//     attrs-only size fields (SPEC §1.3).
//  3. AssignKeylessContributions — the heuristic's actual matching decision:
//     given a slice of keyless (no tool_use_id) contributions and a slice of
//     already-open candidate calls, decide which contribution attaches to
//     which open call, one-to-one, nearest-in-time-first, within 60s
//     (SPEC §1.6).
//
// It does NOT decide *which* calls are "open" — that requires a database
// read (existing tool_calls rows not yet ended), which is necessarily I/O.
// The caller — postgres.upsertToolCalls — is responsible for: querying the
// candidate open calls from Postgres, supplying a per-key ordinal
// allocator for newly-minted keyless calls (also necessarily backed by a
// read of already-persisted rows, SPEC §1.6's "ordinal is the 0-based index
// … in (ts, seq) order"), folding contributions (both keyed and the
// results of this file's assignment) into per-row deltas, and issuing the
// actual upsert SQL. No SQL string appears in this file.
package normalize

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/YohannHommet/argus/server/internal/model"
)

// ToolCallNamespace is the fixed Argus namespace UUID for tool_calls.id's
// UUIDv5 derivation (SPEC §1.6). It MUST NEVER CHANGE: every stored
// tool_calls.id is a hash of (this namespace, a content key), so changing
// this constant would silently change the id of every tool call ever
// written and break the "rebuild produces identical rows" guarantee
// (P3-10) for all of them, with no error to signal it happened.
//
// Generated once via uuid.NewSHA1(uuid.NameSpaceOID, []byte("argus.tool_calls"))
// and frozen as a literal so no code path can regenerate it differently.
var ToolCallNamespace = uuid.MustParse("2e1a9c9e-9c1d-5f2a-8b0a-6e1f5b7c9a3d")

// ToolCallID computes the deterministic UUIDv5 tool_calls.id (SPEC §1.6):
// UUIDv5 over ToolCallNamespace and "session_id|tool_use_id" when
// toolUseID is non-empty, or "session_id|prompt_id|tool_name|ordinal"
// otherwise. ordinal is meaningless (and ignored by convention — callers
// pass 0) when toolUseID is non-empty, since the tool_use_id already
// uniquely keys the row.
func ToolCallID(sessionID string, toolUseID *string, promptID *string, toolName string, ordinal int) uuid.UUID {
	if toolUseID != nil && *toolUseID != "" {
		return uuid.NewSHA1(ToolCallNamespace, []byte(sessionID+"|"+*toolUseID))
	}
	pid := ""
	if promptID != nil {
		pid = *promptID
	}
	key := fmt.Sprintf("%s|%s|%s|%d", sessionID, pid, toolName, ordinal)
	return uuid.NewSHA1(ToolCallNamespace, []byte(key))
}

// ToolCallContribution is what one candidate event contributes to a
// tool_calls row (SPEC §1.6, §1.5.3), extracted from model.Event's
// already-promoted columns. The two *_size_bytes fields are the sole
// exception: SPEC §1.3 deliberately does not promote them onto events, so
// they are read out of Attrs here — this is their only reader (SPEC §1.3,
// §2.3).
type ToolCallContribution struct {
	Event model.Event // kept for TS/Seq tie-break and Attrs access

	SessionID string
	PromptID  *string
	ToolUseID *string
	ToolName  string
	Source    model.Source
	Kind      model.Kind
	TS        time.Time

	Decision       *string
	DecisionSource *string
	ToolSource     *string
	PermissionMode *string
	AgentID        *string
	Success        *bool
	ErrorType      *string
	FilePath       *string
	DurationMS     *int

	InputSizeBytes  *int
	ResultSizeBytes *int
}

// toolCallKinds is the SPEC §1.6 "built from" set for the tool_calls
// projection: tool.pre / tool.decision / tool.permission_request /
// tool.result. Every other Kind contributes nothing to this projection.
var toolCallKinds = map[model.Kind]bool{
	model.KindToolPre:               true,
	model.KindToolDecision:          true,
	model.KindToolPermissionRequest: true,
	model.KindToolResult:            true,
}

// ExtractContribution reads e's tool_calls-relevant fields into a
// ToolCallContribution. ok is false when e's Kind does not feed this
// projection at all (SPEC §1.6), in which case the caller should skip e
// entirely — this function never invents a contribution for an
// unrelated event.
func ExtractContribution(e model.Event) (ToolCallContribution, bool) {
	if !toolCallKinds[e.Kind] {
		return ToolCallContribution{}, false
	}
	toolName := ""
	if e.ToolName != nil {
		toolName = *e.ToolName
	}
	c := ToolCallContribution{
		Event:          e,
		SessionID:      e.SessionID,
		PromptID:       e.PromptID,
		ToolUseID:      e.ToolUseID,
		ToolName:       toolName,
		Source:         e.Source,
		Kind:           e.Kind,
		TS:             e.TS,
		Decision:       e.Decision,
		DecisionSource: e.DecisionSource,
		ToolSource:     e.ToolSource,
		PermissionMode: e.PermissionMode,
		AgentID:        e.AgentID,
		Success:        e.Success,
		ErrorType:      e.ErrorType,
		FilePath:       e.FilePath,
		DurationMS:     e.DurationMS,
	}
	// tool_input_size_bytes / tool_result_size_bytes: attrs-only fields
	// (SPEC §1.3), read here regardless of Kind — only tool.result events
	// carry them in practice (live capture), but a defensive read costs
	// nothing and never invents a value Attrs doesn't have.
	c.InputSizeBytes = int64PtrToIntPtr(Int64(e.Attrs, "tool_input_size_bytes"))
	c.ResultSizeBytes = int64PtrToIntPtr(Int64(e.Attrs, "tool_result_size_bytes"))
	return c, true
}

// HeuristicWindow is SPEC §1.6's "nearest open call within 60 s".
const HeuristicWindow = 60 * time.Second

// OpenCall is a candidate existing tool_calls row a keyless contribution
// (one with no tool_use_id) might attach to — either an OTel-sourced call
// awaiting hook enrichment (correlation exact/otel_only, in which case a
// match upgrades it to CorrelationHeuristic) or an already-in-progress
// hook-only call from an earlier keyless contribution in this same batch or
// a previous one (correlation hook_only, in which case a match simply
// enriches it further and correlation stays hook_only — SPEC §1.6 defines
// `heuristic` specifically as attaching a *hook* event to an *OTel* call).
// PromptID is "" for a null prompt_id, matching the common Go convention of
// comparing against ToolCallContribution.PromptID via promptOrEmpty.
type OpenCall struct {
	ID          uuid.UUID
	SessionID   string
	PromptID    string
	ToolName    string
	StartedAt   time.Time
	Correlation model.Correlation
}

// KeylessAssignment is AssignKeylessContributions's verdict for one
// contribution: which tool_calls row (by id) it belongs to, that row's
// resulting correlation, and whether the id is being minted fresh by this
// assignment (IsNewCall) as opposed to attaching to a caller-supplied
// OpenCall.
type KeylessAssignment struct {
	CallID      uuid.UUID
	Correlation model.Correlation
	IsNewCall   bool
}

func promptOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// AssignKeylessContributions is the SPEC §1.6 heuristic, and the entirety
// of what "confined to one function" means: pure decision logic, no I/O.
//
// contribs are keyless (no tool_use_id) tool.* contributions from the
// current batch, in arbitrary order — this function establishes its own
// (ts, seq) processing order internally so callers never need to
// pre-sort. open is every existing tool_calls row the caller judges a
// plausible match target (in practice: same-session rows with
// ended_at IS NULL — the caller's job, not this function's, to decide
// "open"). nextOrdinal(sessionID, promptID, toolName) is called exactly
// once per newly-minted call, in (ts, seq) order, and must return a
// distinct, increasing ordinal per key each time it is called for that key
// (the caller seeds it from a persisted count — SPEC §1.6's "0-based index
// … in (ts, seq) order", see this file's package doc on why that is a
// batch-honest approximation, not a global reordering, for late data).
//
// Matching is greedy: contributions are processed in (ts, seq) order, and
// each one claims the *nearest* (by |Δt|) still-unclaimed open call sharing
// (session_id, prompt_id, tool_name) within HeuristicWindow. Once claimed, an
// open call cannot be claimed again by a later contribution in the same
// call — this is what makes the one-to-one guarantee hold (SPEC §1.6, lead
// note 2): three concurrent same-tool OTel calls cannot have two hook
// events both matched to the one that happens to be nearest to both, because
// the first (earlier, by ts/seq) contribution to claim a call removes it
// from the pool before the second is ever considered. A contribution that
// finds no match within the window either creates a new call (if none of
// the other keyless contributions already created — and left open — a
// matching one earlier in this same pass) or, having exhausted every
// avenue, becomes its own new hook_only call via nextOrdinal.
func AssignKeylessContributions(
	contribs []ToolCallContribution,
	open []OpenCall,
	nextOrdinal func(sessionID string, promptID *string, toolName string) int,
) map[int]KeylessAssignment {
	order := make([]int, len(contribs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if !contribs[ia].TS.Equal(contribs[ib].TS) {
			return contribs[ia].TS.Before(contribs[ib].TS)
		}
		return contribs[ia].Event.Seq < contribs[ib].Event.Seq
	})

	pool := make([]OpenCall, len(open))
	copy(pool, open)
	claimed := make(map[uuid.UUID]bool, len(pool))
	result := make(map[int]KeylessAssignment, len(contribs))

	for _, i := range order {
		c := contribs[i]
		promptID := promptOrEmpty(c.PromptID)

		best := -1
		var bestDelta time.Duration
		for pi, o := range pool {
			if claimed[o.ID] || o.SessionID != c.SessionID || o.PromptID != promptID || o.ToolName != c.ToolName {
				continue
			}
			delta := c.TS.Sub(o.StartedAt)
			if delta < 0 {
				delta = -delta
			}
			if delta > HeuristicWindow {
				continue
			}
			if best == -1 || delta < bestDelta {
				best, bestDelta = pi, delta
			}
		}

		if best >= 0 {
			m := pool[best]
			claimed[m.ID] = true
			// Attaching a keyless hook to a call that OTel already named
			// downgrades the row to heuristic (SPEC §1.6): the join rested on
			// the nearest-open-call fallback rather than on a tool_use_id, and
			// the API surfaces that so the UI can caveat the decision badge.
			corr := model.CorrelationHookOnly
			switch m.Correlation {
			case model.CorrelationOTelOnly, model.CorrelationExact, model.CorrelationHeuristic:
				corr = model.CorrelationHeuristic
			case model.CorrelationHookOnly:
				// A hook-only call stitched to another keyless hook stays
				// hook-only: no OTel event ever named this call.
			}
			result[i] = KeylessAssignment{CallID: m.ID, Correlation: corr, IsNewCall: false}
			continue
		}

		ordinal := nextOrdinal(c.SessionID, c.PromptID, c.ToolName)
		id := ToolCallID(c.SessionID, nil, c.PromptID, c.ToolName, ordinal)
		// Leave the newly-minted call unclaimed in the pool: a later
		// contribution in this same pass (e.g. a PostToolUse hook event
		// following the PreToolUse that just created this call) must be
		// able to match it. It only becomes unavailable to a THIRD
		// contribution once a second one actually claims it above.
		pool = append(pool, OpenCall{
			ID:          id,
			SessionID:   c.SessionID,
			PromptID:    promptID,
			ToolName:    c.ToolName,
			StartedAt:   c.TS,
			Correlation: model.CorrelationHookOnly,
		})
		result[i] = KeylessAssignment{CallID: id, Correlation: model.CorrelationHookOnly, IsNewCall: true}
	}
	return result
}
