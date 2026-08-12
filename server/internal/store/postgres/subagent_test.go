package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// insertSessionStub creates a minimal sessions row via the real WriteBatch
// path (stub-on-reference, SPEC §1.7) so later direct SQL inserts into
// subagents satisfy its `REFERENCES sessions(id)` FK — used by the
// cycle/depth-cap tests below, which construct subagents rows raw (SQL)
// rather than through upsertSubagents, because their point is to exercise
// SubagentTree's READ-side guard against data shapes upsertSubagents itself
// would never produce (a mutual parent_agent_id cycle).
func insertSessionStub(t *testing.T, st *postgres.Store, sessionID string, ts time.Time) {
	t.Helper()
	e := mkEvent(t, sessionID, model.KindAgentSetup, model.SourceHook, ts)
	_, err := st.WriteBatch(context.Background(), []model.Event{e})
	require.NoError(t, err)
}

// insertRawSubagent inserts one subagents row directly, bypassing
// upsertSubagents entirely. Used only by tests that need a data shape the
// write path itself cannot produce (a raw parent_agent_id cycle).
func insertRawSubagent(t *testing.T, pool *pgxpool.Pool, sessionID, agentID string, parentAgentID *string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO subagents (session_id, agent_id, parent_agent_id, agent_type, depth, started_at, status)
		VALUES ($1, $2, $3, 'Explore', 1, now(), 'running')`,
		sessionID, agentID, parentAgentID)
	require.NoError(t, err)
}

func querySubagentRow(t *testing.T, pool *pgxpool.Pool, sessionID, agentID string) (status string, startedAtNull, endedAtNull bool) {
	t.Helper()
	var startedAt, endedAt *time.Time
	err := pool.QueryRow(context.Background(),
		`SELECT status, started_at, ended_at FROM subagents WHERE session_id = $1 AND agent_id = $2`,
		sessionID, agentID).Scan(&status, &startedAt, &endedAt)
	require.NoError(t, err)
	return status, startedAt == nil, endedAt == nil
}

// --- AC 1: a 2-level tree returns nested children with correct depth. ----

func TestSubagentTree_TwoLevelTreeNestsWithCorrectDepth(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-subtree-2level"

	// ag-a: direct child of the synthetic root (no parent_agent_id).
	startA := hookEvent(t, base, "SubagentStart", sessionID, map[string]any{
		"agent_id": "ag-a", "agent_type": "Explore", "prompt_id": "p1",
	})
	// ag-b: child of ag-a (depth 2).
	startB := hookEvent(t, base.Add(time.Second), "SubagentStart", sessionID, map[string]any{
		"agent_id": "ag-b", "agent_type": "general-purpose", "parent_agent_id": "ag-a", "prompt_id": "p1",
	})

	_, err := st.WriteBatch(ctx, []model.Event{startA, startB})
	require.NoError(t, err)

	tree, err := st.SubagentTree(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, tree.Nodes, 1)

	root := tree.Nodes[0]
	require.Equal(t, "root", root.AgentID)
	require.Nil(t, root.ParentAgentID)
	require.Equal(t, 0, root.Depth)
	require.Len(t, root.Children, 1)

	a := root.Children[0]
	require.Equal(t, "ag-a", a.AgentID)
	require.Equal(t, "root", *a.ParentAgentID)
	require.Equal(t, 1, a.Depth)
	require.Nil(t, a.CostUSD)
	require.Len(t, a.Children, 1)

	b := a.Children[0]
	require.Equal(t, "ag-b", b.AgentID)
	require.Equal(t, "ag-a", *b.ParentAgentID)
	require.Equal(t, 2, b.Depth)
	require.Nil(t, b.CostUSD)
	require.Empty(t, b.Children)
}

// --- AC 2: SubagentStop without a matching start -> status='unknown',
// started_at IS NULL (never inherits the DDL default of 'running').

func TestWriteBatch_Subagent_StopWithoutStartIsUnknownNotRunning(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-subagent-orphan-stop"
	stop := hookEvent(t, base, "SubagentStop", sessionID, map[string]any{
		"agent_id": "ag-orphan", "success": true,
	})

	_, err := st.WriteBatch(ctx, []model.Event{stop})
	require.NoError(t, err)

	status, startedAtNull, endedAtNull := querySubagentRow(t, pool, sessionID, "ag-orphan")
	require.Equal(t, "unknown", status, "a stop without a start must not inherit the DDL default 'running'")
	require.True(t, startedAtNull)
	require.False(t, endedAtNull)

	tree, err := st.SubagentTree(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, tree.Nodes[0].Children, 1)
	orphan := tree.Nodes[0].Children[0]
	require.Equal(t, model.SubagentStatusUnknown, orphan.Status)
	require.Nil(t, orphan.StartedAt)
}

// A subsequent SubagentStart for the same agent must correct the row away
// from 'unknown' once its true start is known.
func TestWriteBatch_Subagent_LateStartCorrectsUnknownStatus(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-subagent-late-start"
	stop := hookEvent(t, base, "SubagentStop", sessionID, map[string]any{
		"agent_id": "ag-late", "success": true,
	})
	_, err := st.WriteBatch(ctx, []model.Event{stop})
	require.NoError(t, err)

	status, _, _ := querySubagentRow(t, pool, sessionID, "ag-late")
	require.Equal(t, "unknown", status)

	start := hookEvent(t, base.Add(-time.Second), "SubagentStart", sessionID, map[string]any{
		"agent_id": "ag-late", "agent_type": "Explore",
	})
	_, err = st.WriteBatch(ctx, []model.Event{start})
	require.NoError(t, err)

	status, startedAtNull, _ := querySubagentRow(t, pool, sessionID, "ag-late")
	require.Equal(t, "complete", status)
	require.False(t, startedAtNull)
}

// --- AC 3: a parent_agent_id cycle terminates at the depth cap, logs, and
// does not hang.

// A mutual two-node cycle (A<->B) has no member with parent_agent_id IS
// NULL, so neither can ever be reached by SubagentTree's recursive CTE,
// which starts strictly from root-level (parent_agent_id IS NULL) rows and
// walks forward one hop at a time — a single-parent-per-row schema makes a
// reachable infinite loop structurally impossible (each row has exactly one
// parent value, so it can be discovered via at most one join path). This
// test proves the practical safety property the AC cares about: the query
// completes promptly and a corrupt/cyclic pair is silently excluded rather
// than hanging the request or crashing the process — never included as if
// it were valid data.
func TestSubagentTree_MutualCycleExcludedNotHung(t *testing.T) {
	st, pool := newStore(t)
	sessionID := "session-subagent-cycle"
	base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)
	insertSessionStub(t, st, sessionID, base)

	agA, agB := "ag-cycle-a", "ag-cycle-b"
	insertRawSubagent(t, pool, sessionID, agA, &agB) // A's parent is B
	insertRawSubagent(t, pool, sessionID, agB, &agA) // B's parent is A: A<->B cycle

	agRoot := "ag-real-root-child"
	insertRawSubagent(t, pool, sessionID, agRoot, nil)

	done := make(chan struct{})
	var tree model.SubagentTree
	var err error
	go func() {
		tree, err = st.SubagentTree(context.Background(), sessionID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SubagentTree hung on a parent_agent_id cycle")
	}
	require.NoError(t, err)

	require.Len(t, tree.Nodes[0].Children, 1, "the cyclic pair must be excluded, only the real root child remains")
	require.Equal(t, agRoot, tree.Nodes[0].Children[0].AgentID)
}

// A long real chain (deeper than the depth cap) must be truncated at
// subagentMaxDepth (16), demonstrating the SQL-side `WHERE t.lvl < 16`
// guard actually bounds recursion rather than merely being decorative.
func TestSubagentTree_DepthCapTruncatesLongChain(t *testing.T) {
	st, pool := newStore(t)
	sessionID := "session-subagent-deep-chain"
	base := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)
	insertSessionStub(t, st, sessionID, base)

	const chainLen = 20
	var parent *string
	for i := 0; i < chainLen; i++ {
		id := chainAgentID(i)
		insertRawSubagent(t, pool, sessionID, id, parent)
		p := id
		parent = &p
	}

	done := make(chan struct{})
	var tree model.SubagentTree
	var err error
	go func() {
		tree, err = st.SubagentTree(context.Background(), sessionID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SubagentTree hung on a long chain")
	}
	require.NoError(t, err)

	maxDepth := 0
	var walk func(n model.SubagentNode)
	walk = func(n model.SubagentNode) {
		if n.Depth > maxDepth {
			maxDepth = n.Depth
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tree.Nodes[0])
	require.Equal(t, 16, maxDepth, "the tree must be truncated exactly at the depth cap")
}

func chainAgentID(i int) string {
	return fmt.Sprintf("ag-chain-%02d", i)
}

// --- AC 4/6: every node has cost_usd IS NULL, cost_attribution.per_node_available
// is false, and by_query_source reproduces the session's map including
// unknown keys.

func TestSubagentTree_CostAttributionReproducesSessionMapVerbatim(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-subagent-cost-attribution"
	req1 := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withCost(3.9011, "reported"), withQuerySource(""), withModel("claude-opus-5"))
	req2 := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Second),
		withCost(0.35, "reported"), withQuerySource("sdk"), withModel("claude-opus-5"))
	req3 := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(2*time.Second),
		withCost(0.02, "reported"), withQuerySource("a_future_vendor_value_argus_has_no_constant_for"), withModel("claude-opus-5"))
	start := hookEvent(t, base, "SubagentStart", sessionID, map[string]any{"agent_id": "ag-cost", "agent_type": "Explore"})

	_, err := st.WriteBatch(ctx, []model.Event{req1, req2, req3, start})
	require.NoError(t, err)

	tree, err := st.SubagentTree(ctx, sessionID)
	require.NoError(t, err)

	require.False(t, tree.CostAttribution.PerNodeAvailable)
	require.Equal(t, map[string]float64{
		"":    3.9011,
		"sdk": 0.35,
		"a_future_vendor_value_argus_has_no_constant_for": 0.02,
	}, tree.CostAttribution.ByQuerySource)
	require.Empty(t, tree.CostAttribution.DominantQuerySource)
	require.InDelta(t, 0.37, tree.CostAttribution.OtherQuerySourceUSD, 0.0001)

	require.Nil(t, tree.Nodes[0].CostUSD, "root cost_usd must stay NULL (SPEC §1.9) — never session-minus-children")
	require.Nil(t, tree.Nodes[0].Children[0].CostUSD)
}

// --- AC 5: tool_call_count is NULL for a session with only OTel events,
// non-null once hook tool events with agent_id exist.

func TestWriteBatch_Subagent_ToolCallCountNullWithoutHookCoverage(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-subagent-no-hook-coverage"
	toolUseID := "toolu_no_hook_1"
	start := hookEvent(t, base, "SubagentStart", sessionID, map[string]any{"agent_id": "ag-nohook", "agent_type": "Explore"})
	// An OTel-only tool call in the same session: correlation will be
	// otel_only, which must NOT count as tool-level hook coverage.
	pre := mkEvent(t, sessionID, model.KindToolPre, model.SourceOTelLog, base.Add(time.Second),
		func(e *model.Event) { e.ToolName = ptrString("Read"); e.ToolUseID = &toolUseID })
	result := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base.Add(2*time.Second),
		func(e *model.Event) {
			e.ToolName = ptrString("Read")
			e.ToolUseID = &toolUseID
			ok := true
			e.Success = &ok
		})

	_, err := st.WriteBatch(ctx, []model.Event{start, pre, result})
	require.NoError(t, err)

	tree, err := st.SubagentTree(ctx, sessionID)
	require.NoError(t, err)
	require.Nil(t, tree.Nodes[0].Children[0].ToolCallCount, "no tool-level hook coverage in this session => NULL, never 0")
}

func TestWriteBatch_Subagent_ToolCallCountNonNullWithHookCoverage(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-subagent-hook-coverage"
	agentID := "ag-hooked"
	toolUseID := "toolu_hooked_1"

	start := hookEvent(t, base, "SubagentStart", sessionID, map[string]any{"agent_id": agentID, "agent_type": "Explore"})
	pre := hookEvent(t, base.Add(time.Second), "PreToolUse", sessionID, map[string]any{
		"agent_id": agentID, "tool_name": "Read", "tool_use_id": toolUseID,
	})
	post := hookEvent(t, base.Add(2*time.Second), "PostToolUse", sessionID, map[string]any{
		"agent_id": agentID, "tool_name": "Read", "tool_use_id": toolUseID,
	})

	_, err := st.WriteBatch(ctx, []model.Event{start, pre, post})
	require.NoError(t, err)

	tree, err := st.SubagentTree(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, tree.Nodes[0].Children[0].ToolCallCount)
	require.Equal(t, 1, *tree.Nodes[0].Children[0].ToolCallCount)
}
