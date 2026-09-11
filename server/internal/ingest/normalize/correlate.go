// Package normalize — correlate.go implements SPEC §1.6's tool_calls
// projection logic purely in Go (no store import): ToolCallID, ExtractContribution,
// and AssignKeylessContributions (one-to-one nearest-match heuristic).
// It does NOT decide which calls are "open" — that is I/O (the caller's job).
package normalize

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/YohannHommet/argus/server/internal/model"
)

// ToolCallNamespace is the fixed UUIDv5 namespace for tool_calls.id (SPEC §1.6).
// MUST NEVER CHANGE: changing it would silently recompute every tool_calls.id
// ever written, breaking P3-10's "rebuild produces identical rows" guarantee.
var ToolCallNamespace = uuid.MustParse("2e1a9c9e-9c1d-5f2a-8b0a-6e1f5b7c9a3d")

// ToolCallID computes the deterministic UUIDv5 tool_calls.id (SPEC §1.6).
// Hash is over (ToolCallNamespace, "session_id|tool_use_id") when toolUseID
// is non-empty, else "session_id|prompt_id|tool_name|ordinal".
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

// ToolCallContribution is what one event contributes to a tool_calls row
// (SPEC §1.6, §1.5.3). The two *_size_bytes fields are read from Attrs
// (not promoted onto events per SPEC §1.3) — this is their only reader.
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

// toolCallKinds is the SPEC §1.6 "built from" set: tool.pre / tool.decision
// / tool.permission_request / tool.result only.
var toolCallKinds = map[model.Kind]bool{
	model.KindToolPre:               true,
	model.KindToolDecision:          true,
	model.KindToolPermissionRequest: true,
	model.KindToolResult:            true,
}

// ExtractContribution reads e's tool_calls-relevant fields. ok is false
// when e's Kind does not feed this projection (SPEC §1.6).
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
	// tool_input_size_bytes / tool_result_size_bytes are attrs-only (SPEC §1.3).
	c.InputSizeBytes = int64PtrToIntPtr(Int64(e.Attrs, "tool_input_size_bytes"))
	c.ResultSizeBytes = int64PtrToIntPtr(Int64(e.Attrs, "tool_result_size_bytes"))
	return c, true
}

// HeuristicWindow is SPEC §1.6's "nearest open call within 60 s".
const HeuristicWindow = 60 * time.Second

// OpenCall is a candidate existing tool_calls row a keyless contribution
// might attach to (either OTel-sourced or hook-only). PromptID is "" for null.
type OpenCall struct {
	ID          uuid.UUID
	SessionID   string
	PromptID    string
	ToolName    string
	StartedAt   time.Time
	Correlation model.Correlation
}

// KeylessAssignment is the verdict for one contribution: which tool_calls
// row (id), that row's resulting correlation, and IsNewCall (minted now vs
// attaching to an existing open call).
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

// AssignKeylessContributions is the SPEC §1.6 heuristic: pure decision logic,
// no I/O. contribs are keyless (no tool_use_id) tool.* contributions;
// this function orders them (ts, seq) internally. open is every existing
// tool_calls row callable (caller's job to decide "open"). nextOrdinal
// is called exactly once per newly-minted call in (ts, seq) order, must
// return distinct increasing ordinals per key.
//
// Matching is greedy in (ts, seq) order; each claims the nearest unclaimed
// open call within HeuristicWindow. This enforces one-to-one: the first to
// claim a call removes it from the pool (SPEC §1.6, lead note 2).
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
		// audit finding m13: Event.Seq is a Postgres identity assigned by
		// the events INSERT; this function runs on the pre-insert
		// candidates slice, where every Seq is still its zero value, so a
		// same-ts tiebreak on Seq silently degrades to (ts, dedup_key)
		// lexicographic order — which sorts vendor_seq 10 before 9. Tiebreak
		// on VendorSeq instead (NULLS LAST, i.e. an absent VendorSeq sorts
		// after any present one — a hook contribution has none), then
		// DedupKey for a fully deterministic order when even VendorSeq ties
		// or is absent on both sides. This documents the tiebreak as
		// submission order (VendorSeq when the source supplies one,
		// otherwise arrival order via DedupKey), not the originally
		// intended-but-inoperative Seq-based order.
		va, vb := contribs[ia].Event.VendorSeq, contribs[ib].Event.VendorSeq
		switch {
		case va != nil && vb != nil && *va != *vb:
			return *va < *vb
		case va != nil && vb == nil:
			return true
		case va == nil && vb != nil:
			return false
		}
		return contribs[ia].Event.DedupKey < contribs[ib].Event.DedupKey
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
			// Attaching keyless hook to OTel call → heuristic (SPEC §1.6).
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
		// Leave newly-minted call unclaimed in pool: a later contribution
		// (e.g. PostToolUse after PreToolUse) must be able to match it.
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
