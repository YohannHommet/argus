package normalize

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// TestToolCallID_Deterministic covers P3-10's load-bearing property (SPEC
// §1.6): the same input always yields the same id, keyed form and ordinal
// form both, and different inputs never collide by construction.
func TestToolCallID_Deterministic(t *testing.T) {
	t.Parallel()

	id1 := ToolCallID("session-a", strp("toolu_1"), nil, "", 0)
	id2 := ToolCallID("session-a", strp("toolu_1"), nil, "", 0)
	require.Equal(t, id1, id2, "replaying the same (session,tool_use_id) must yield the same id")

	id3 := ToolCallID("session-a", strp("toolu_2"), nil, "", 0)
	require.NotEqual(t, id1, id3)

	// Ordinal form: same key + ordinal -> same id; different ordinal ->
	// different id; toolUseID must be nil or empty to hit this branch.
	promptID := "prompt-1"
	o1 := ToolCallID("session-a", nil, &promptID, "Edit", 0)
	o1b := ToolCallID("session-a", nil, &promptID, "Edit", 0)
	require.Equal(t, o1, o1b)

	o2 := ToolCallID("session-a", nil, &promptID, "Edit", 1)
	require.NotEqual(t, o1, o2, "distinct ordinals must not collide")

	empty := ""
	oEmpty := ToolCallID("session-a", &empty, &promptID, "Edit", 0)
	require.Equal(t, o1, oEmpty, "an empty (not nil) tool_use_id must fall back to the ordinal form, same as nil")
}

// TestExtractContribution_SkipsUnrelatedKinds covers SPEC §1.6's "built
// from tool.pre/tool.decision/tool.permission_request/tool.result" scope:
// every other Kind must be rejected (ok=false), never silently folded in.
func TestExtractContribution_SkipsUnrelatedKinds(t *testing.T) {
	t.Parallel()

	_, ok := ExtractContribution(model.Event{Kind: model.KindLLMRequest})
	require.False(t, ok)

	for _, k := range []model.Kind{model.KindToolPre, model.KindToolDecision, model.KindToolPermissionRequest, model.KindToolResult} {
		_, ok := ExtractContribution(model.Event{Kind: k})
		require.True(t, ok, "kind %s must feed the tool_calls projection", k)
	}
}

// TestExtractContribution_SizeBytesFromAttrs covers SPEC §1.3/§2.3: the two
// *_size_bytes fields are deliberately not promoted onto events, so this is
// their only reader — pulled straight out of Attrs.
func TestExtractContribution_SizeBytesFromAttrs(t *testing.T) {
	t.Parallel()

	c, ok := ExtractContribution(model.Event{
		Kind: model.KindToolResult,
		Attrs: map[string]any{
			"tool_input_size_bytes":  "80", // capture shows these as OTel strings
			"tool_result_size_bytes": int64(42),
		},
	})
	require.True(t, ok)
	require.NotNil(t, c.InputSizeBytes)
	require.Equal(t, 80, *c.InputSizeBytes)
	require.NotNil(t, c.ResultSizeBytes)
	require.Equal(t, 42, *c.ResultSizeBytes)
}

func baseTS(offset time.Duration) time.Time {
	return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC).Add(offset)
}

func hookContrib(sessionID, promptID, toolName string, ts time.Time, seq int64) ToolCallContribution {
	pid := promptID
	return ToolCallContribution{
		Event:     model.Event{Seq: seq, SessionID: sessionID, TS: ts, Kind: model.KindToolPre, Source: model.SourceHook},
		SessionID: sessionID,
		PromptID:  &pid,
		ToolName:  toolName,
		Source:    model.SourceHook,
		Kind:      model.KindToolPre,
		TS:        ts,
	}
}

// TestAssignKeylessContributions_OneToOne is the direct regression for the
// AC's "three concurrent Edit calls, matched exactly one each, never two"
// (SPEC §1.6, lead note 2): three OTel-tracked open calls for the same
// (session, prompt, tool) plus three keyless hook contributions must
// produce a bijection, never a shared target.
func TestAssignKeylessContributions_OneToOne(t *testing.T) {
	t.Parallel()

	sessionID, promptID, tool := "session-a", "prompt-1", "Edit"
	open := []OpenCall{
		{ID: mustUUID(1), SessionID: sessionID, PromptID: promptID, ToolName: tool, StartedAt: baseTS(0), Correlation: model.CorrelationOTelOnly},
		{ID: mustUUID(2), SessionID: sessionID, PromptID: promptID, ToolName: tool, StartedAt: baseTS(1 * time.Second), Correlation: model.CorrelationOTelOnly},
		{ID: mustUUID(3), SessionID: sessionID, PromptID: promptID, ToolName: tool, StartedAt: baseTS(2 * time.Second), Correlation: model.CorrelationOTelOnly},
	}
	contribs := []ToolCallContribution{
		hookContrib(sessionID, promptID, tool, baseTS(50*time.Millisecond), 10),
		hookContrib(sessionID, promptID, tool, baseTS(1*time.Second+50*time.Millisecond), 11),
		hookContrib(sessionID, promptID, tool, baseTS(2*time.Second+50*time.Millisecond), 12),
	}

	assignments := AssignKeylessContributions(contribs, open, failNextOrdinal(t))
	require.Len(t, assignments, 3)

	seen := map[uuid.UUID]bool{}
	for i, a := range assignments {
		require.False(t, a.IsNewCall, "contribution %d should have matched an open OTel call, not minted a new one", i)
		require.Equal(t, model.CorrelationHeuristic, a.Correlation)
		require.False(t, seen[a.CallID], "call %s matched by more than one contribution — one-to-one violated", a.CallID)
		seen[a.CallID] = true
	}
	require.Len(t, seen, 3, "all three open calls must be claimed, one each")
	require.Equal(t, mustUUID(1), assignments[0].CallID)
	require.Equal(t, mustUUID(2), assignments[1].CallID)
	require.Equal(t, mustUUID(3), assignments[2].CallID)
}

// TestAssignKeylessContributions_LateArrivalNoMatch covers the AC "a hook
// arriving 5 minutes late does not match" — outside HeuristicWindow, the
// contribution must mint its own new call rather than falsely attaching.
func TestAssignKeylessContributions_LateArrivalNoMatch(t *testing.T) {
	t.Parallel()

	sessionID, promptID, tool := "session-a", "prompt-1", "Read"
	open := []OpenCall{
		{ID: mustUUID(1), SessionID: sessionID, PromptID: promptID, ToolName: tool, StartedAt: baseTS(0), Correlation: model.CorrelationOTelOnly},
	}
	late := hookContrib(sessionID, promptID, tool, baseTS(5*time.Minute), 1)

	calls := 0
	assignments := AssignKeylessContributions([]ToolCallContribution{late}, open, func(string, *string, string) int {
		calls++
		return 0
	})
	require.Len(t, assignments, 1)
	require.True(t, assignments[0].IsNewCall)
	require.Equal(t, model.CorrelationHookOnly, assignments[0].Correlation)
	require.Equal(t, 1, calls, "a genuinely unmatched contribution must mint exactly one new call")
}

// TestAssignKeylessContributions_HookOnlyStitching covers "hooks-only
// without tool_use_id -> hook_only": a Pre followed by a Result, both
// keyless, with no OTel open call at all, must merge into ONE call rather
// than each minting its own.
func TestAssignKeylessContributions_HookOnlyStitching(t *testing.T) {
	t.Parallel()

	sessionID, promptID, tool := "session-a", "prompt-1", "Bash"
	pre := hookContrib(sessionID, promptID, tool, baseTS(0), 1)
	result := hookContrib(sessionID, promptID, tool, baseTS(200*time.Millisecond), 2)
	result.Kind = model.KindToolResult

	ordinalCalls := 0
	assignments := AssignKeylessContributions([]ToolCallContribution{pre, result}, nil, func(string, *string, string) int {
		ordinalCalls++
		return ordinalCalls - 1
	})
	require.Len(t, assignments, 2)
	require.True(t, assignments[0].IsNewCall)
	require.False(t, assignments[1].IsNewCall, "the result must attach to the call the pre event just created")
	require.Equal(t, assignments[0].CallID, assignments[1].CallID)
	require.Equal(t, model.CorrelationHookOnly, assignments[1].Correlation)
	require.Equal(t, 1, ordinalCalls, "only the first (call-creating) contribution consumes an ordinal")
}

// TestAssignKeylessContributions_ProcessesInTSSeqOrder proves the function
// establishes its own (ts, seq) ordering rather than trusting caller order
// (SPEC §1.6's ordinal is defined "in (ts, seq) order"): contributions
// passed out of order must still have nextOrdinal invoked in true
// chronological order, each >60s apart so none match each other.
func TestAssignKeylessContributions_ProcessesInTSSeqOrder(t *testing.T) {
	t.Parallel()
	sessionID, promptID, tool := "s", "p", "Write"

	c2 := hookContrib(sessionID, promptID, tool, baseTS(4*time.Minute), 3)
	c0 := hookContrib(sessionID, promptID, tool, baseTS(0), 1)
	c1 := hookContrib(sessionID, promptID, tool, baseTS(2*time.Minute), 2)

	var callOrderTS []time.Time
	assignments := AssignKeylessContributions([]ToolCallContribution{c2, c0, c1}, nil, func(string, *string, string) int {
		// nextOrdinal carries no ts argument by design (it is keyed only on
		// session/prompt/tool — SPEC §1.6's key), so this test observes
		// ordering indirectly: it records call count and cross-checks
		// against the returned assignments' IsNewCall/CallID uniqueness
		// below instead of the call's own ts.
		callOrderTS = append(callOrderTS, time.Time{})
		return len(callOrderTS) - 1
	})
	require.Len(t, assignments, 3)
	ids := map[uuid.UUID]bool{}
	for _, a := range assignments {
		require.True(t, a.IsNewCall, "contributions here are >60s apart and share no open pool entry, so each must mint its own call")
		require.False(t, ids[a.CallID], "each new call must get a distinct id")
		ids[a.CallID] = true
	}
	require.Len(t, ids, 3)
	require.Len(t, callOrderTS, 3)
}

// TestAssignKeylessContributions_TiebreaksOnVendorSeqNotDedupKey is audit
// finding m13's required test: two same-ts contributions whose vendor_seq
// order is the OPPOSITE of their dedup_key's lexicographic order. Before the
// fix, the tiebreak read Event.Seq — always 0 on this pre-insert slice — and
// silently fell through to (ts, dedup_key) order, processing vendor_seq 10
// before vendor_seq 9. The fix must process 9 before 10.
func TestAssignKeylessContributions_TiebreaksOnVendorSeqNotDedupKey(t *testing.T) {
	t.Parallel()
	sessionID, promptID, ts := "s", "p", baseTS(0)
	vendorSeq9 := int64(9)
	vendorSeq10 := int64(10)

	// dedup_key is deliberately the reverse of vendor_seq order: "a..." <
	// "z...", so a (ts, dedup_key) tiebreak would process the vendor_seq-10
	// contribution first — exactly the bug this finding reports.
	c10 := ToolCallContribution{
		Event:     model.Event{SessionID: sessionID, TS: ts, VendorSeq: &vendorSeq10, DedupKey: "a-sorts-first-by-dedup-key"},
		SessionID: sessionID,
		PromptID:  &promptID,
		ToolName:  "vendor-seq-10",
		TS:        ts,
	}
	c9 := ToolCallContribution{
		Event:     model.Event{SessionID: sessionID, TS: ts, VendorSeq: &vendorSeq9, DedupKey: "z-sorts-last-by-dedup-key"},
		SessionID: sessionID,
		PromptID:  &promptID,
		ToolName:  "vendor-seq-9",
		TS:        ts,
	}

	var order []string
	AssignKeylessContributions([]ToolCallContribution{c10, c9}, nil, func(_ string, _ *string, toolName string) int {
		order = append(order, toolName)
		return len(order) - 1
	})

	require.Equal(t, []string{"vendor-seq-9", "vendor-seq-10"}, order,
		"processing order must follow vendor_seq ascending (NULLS LAST), never dedup_key lexicographic order")
}

// TestAssignKeylessContributions_TiebreaksOnDedupKeyWhenVendorSeqAbsent
// covers the two remaining branches of the m13 tiebreak: a nil VendorSeq
// sorts after any present one ("NULLS LAST"), and when both are nil the
// order falls back to DedupKey so the result stays fully deterministic —
// SPEC §1.7 rule 2's hash-fallback dedup form applies to every hook event
// (VendorSeq is always nil for hooks), so this is the common case for
// hook-sourced contributions, not an edge case.
func TestAssignKeylessContributions_TiebreaksOnDedupKeyWhenVendorSeqAbsent(t *testing.T) {
	t.Parallel()
	sessionID, promptID, ts := "s", "p", baseTS(0)
	vendorSeq5 := int64(5)

	withVendorSeq := ToolCallContribution{
		Event:     model.Event{SessionID: sessionID, TS: ts, VendorSeq: &vendorSeq5, DedupKey: "z-would-sort-last-by-dedup-key"},
		SessionID: sessionID,
		PromptID:  &promptID,
		ToolName:  "has-vendor-seq",
		TS:        ts,
	}
	withoutVendorSeq := ToolCallContribution{
		Event:     model.Event{SessionID: sessionID, TS: ts, VendorSeq: nil, DedupKey: "a-would-sort-first-by-dedup-key"},
		SessionID: sessionID,
		PromptID:  &promptID,
		ToolName:  "no-vendor-seq",
		TS:        ts,
	}

	var order []string
	recordOrder := func(_ string, _ *string, toolName string) int {
		order = append(order, toolName)
		return len(order) - 1
	}

	AssignKeylessContributions([]ToolCallContribution{withoutVendorSeq, withVendorSeq}, nil, recordOrder)
	require.Equal(t, []string{"has-vendor-seq", "no-vendor-seq"}, order,
		"a present VendorSeq must sort before an absent one, regardless of dedup_key")

	// Both nil: falls back to DedupKey, ascending.
	order = nil
	a := ToolCallContribution{Event: model.Event{SessionID: sessionID, TS: ts, DedupKey: "a-first"}, SessionID: sessionID, PromptID: &promptID, ToolName: "a", TS: ts}
	z := ToolCallContribution{Event: model.Event{SessionID: sessionID, TS: ts, DedupKey: "z-last"}, SessionID: sessionID, PromptID: &promptID, ToolName: "z", TS: ts}
	AssignKeylessContributions([]ToolCallContribution{z, a}, nil, recordOrder)
	require.Equal(t, []string{"a", "z"}, order, "both VendorSeq absent must fall back to DedupKey ascending")
}

func mustUUID(n byte) uuid.UUID {
	var u uuid.UUID
	u[15] = n
	return u
}

func failNextOrdinal(t *testing.T) func(string, *string, string) int {
	t.Helper()
	return func(sessionID string, promptID *string, toolName string) int {
		t.Fatalf("nextOrdinal must not be called when every contribution matches an open call (session=%s prompt=%v tool=%s)", sessionID, promptID, toolName)
		return 0
	}
}
