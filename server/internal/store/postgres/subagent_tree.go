// Package postgres — subagent_tree.go implements store.Reader.SubagentTree
// (SPEC §3.3, §4.3, §2.5, §1.9): the first Reader method to get a real
// body (P1-04 left every Reader method as an ErrNotImplemented stub). Kept
// deliberately simple per the lead note ("keep it simple and honest, and
// do not build a general query framework") — three fixed sqlc queries, one
// nesting pass, no dynamic filter machinery.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// syntheticRootAgentID is the sentinel agent_id for the main-agent node
// SPEC §4.3 that has no subagents row of its own (SPEC lead note 2: the
// main agent never emits SubagentStart). It is the same literal SPEC §4.3's
// own worked example uses, so this is the SANCTIONED sentinel, not an
// invented one — a real Claude Code agent_id observed so far always looks
// like "ag_<n>" (live capture), never the bare word "root", and this value
// can never collide with a genuine subagents row because it is never
// written to that table by upsert_subagent.go.
const syntheticRootAgentID = "root"

// syntheticRootAgentType is root's `agent_type` (SPEC §4.3 example: "main").
// Real agent_type values are unconstrained vendor text (SPEC §0) fed by
// `subagent_type` on a Task-like tool call, which never names the main
// agent itself — so "main" cannot collide with a real observed value in
// practice, and even if it somehow did, root is identified by AgentID, not
// AgentType.
const syntheticRootAgentType = "main"

// SubagentTree implements store.Reader (SPEC §3.3, §4.3): the assembled
// subagent tree for one session, rooted at a synthetic "root" node
// representing the main agent, plus the session-level cost_attribution
// block that stands in for the per-node cost SPEC §1.9 says is
// unobtainable.
func (s *Store) SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error) {
	q := gen.New(s.pool)

	treeRows, err := q.GetSubagentTree(ctx, sessionID)
	if err != nil {
		return model.SubagentTree{}, fmt.Errorf("postgres: query subagent tree: %w", err)
	}
	if hitDepthCap(treeRows) {
		// SPEC lead note 3: "terminate at the depth cap, logs, and does not
		// hang". The query already terminated (that's what makes this line
		// reachable at all instead of a stuck test); this is the "logs"
		// half of the requirement — a signal an operator can act on
		// (a malformed parent_agent_id cycle in the vendor's own data).
		slog.Warn("subagent tree hit the depth cap; parent_agent_id may contain a cycle",
			"session_id", sessionID, "depth_cap", subagentMaxDepth)
	}

	sessionRow, err := q.GetSessionForSubagentTree(ctx, sessionID)
	if err != nil {
		return model.SubagentTree{}, fmt.Errorf("postgres: query session for subagent tree: %w", err)
	}

	stats, err := q.SubagentTreeToolCallStats(ctx, sessionID)
	if err != nil {
		return model.SubagentTree{}, fmt.Errorf("postgres: query subagent tree tool-call stats: %w", err)
	}

	root := buildRootNode(sessionRow, stats)
	nestSubagentRows(&root, treeRows)

	attribution, err := buildCostAttribution(sessionRow.CostByQuerySource)
	if err != nil {
		return model.SubagentTree{}, err
	}

	return model.SubagentTree{
		Nodes:           []model.SubagentNode{root},
		CostAttribution: attribution,
	}, nil
}

// hitDepthCap reports whether any row in the tree query's result reached
// subagentMaxDepth — the signal that the recursive CTE's `lvl < 16` guard
// is what stopped the recursion, rather than the tree simply ending
// naturally above the cap.
func hitDepthCap(rows []gen.GetSubagentTreeRow) bool {
	for _, r := range rows {
		if int(r.Lvl) >= subagentMaxDepth {
			return true
		}
	}
	return false
}

// buildRootNode assembles the synthetic root (SPEC §4.3, lead note 2): it
// borrows the SESSION's own lifecycle (started_at/ended_at/status) because
// the main agent's lifecycle IS the session's (SPEC §1.1's hierarchy — the
// main agent never has a subagents row to read these from itself), and its
// tool_call_count/cost_usd obey SPEC §1.9 exactly:
//
//   - cost_usd is ALWAYS nil. It is deliberately NOT computed as "session
//     cost minus subagent costs" — that computation is exactly what SPEC
//     §1.9 and the ticket's AC call out as invalid ("any test asserting
//     ... 'root aggregates = session totals minus children' is invalid"),
//     because Claude Code's telemetry cannot attribute cost to an agent at
//     all (query_source carries no agent_id), so a subtraction would only
//     be correct by accident and wrong the moment any cost is
//     unattributed to either bucket for a reason SPEC §1.9 didn't model.
//   - tool_call_count follows the identical NULL-vs-0 rule real subagent
//     nodes follow: NULL when the session has no tool-level hook coverage
//     (nobody can say whether the main agent used zero tools or the
//     question is simply unanswerable), the count of tool_calls with a
//     NULL agent_id (hook-attributed to no subagent, i.e. the main agent's
//     own use) once coverage is confirmed.
func buildRootNode(session gen.GetSessionForSubagentTreeRow, stats gen.SubagentTreeToolCallStatsRow) model.SubagentNode {
	agentType := syntheticRootAgentType
	root := model.SubagentNode{
		AgentID:       syntheticRootAgentID,
		ParentAgentID: nil,
		AgentType:     agentType,
		Depth:         0,
		Status:        model.SubagentStatus(session.Status),
		CostUSD:       nil, // SPEC §1.9 — see doc above; never derived
		Children:      []model.SubagentNode{},
	}
	if session.StartedAt.Valid {
		t := session.StartedAt.Time
		root.StartedAt = &t
	}
	if session.EndedAt.Valid {
		t := session.EndedAt.Time
		root.EndedAt = &t
	}
	if stats.HasHookCoverage {
		n := int(stats.RootToolCalls)
		root.ToolCallCount = &n
	}
	return root
}

// nestSubagentRows turns GetSubagentTree's flat, level-ordered result into
// the nested `children` shape SPEC §4.3 requires, attaching every
// parent_agent_id-IS-NULL row directly under root — including relabelling
// its ParentAgentID to the literal "root" (SPEC §4.3's own worked example:
// `"parent_agent_id": "root"` on a depth-1 node), even though the database
// column is NULL there.
//
// Assembly is two passes rather than one: first build every node plus a
// parent -> ordered-child-ids index (no nested-slice mutation yet), then
// recursively assemble the final tree by VALUE from that index. A
// single-pass approach that appends directly into a *parent's* Children
// slice while walking rows breaks under Go's slice semantics for a tree
// deeper than two levels: `append` can reallocate a slice's backing array,
// silently invalidating any pointer taken into it earlier (e.g. a pointer
// to a grandchild's parent-slot recorded before a later sibling's append
// reallocates that slot's owning slice) — see the git history of this
// function for the pointer-aliasing bug this replaced. The recursive
// `assemble` below sidesteps the whole class of bug by never taking the
// address of a slice element at all.
func nestSubagentRows(root *model.SubagentNode, rows []gen.GetSubagentTreeRow) {
	nodes := make(map[string]model.SubagentNode, len(rows))
	childIDs := make(map[string][]string, len(rows))

	for _, r := range rows {
		node := model.SubagentNode{
			AgentID:  r.AgentID,
			Depth:    int(r.Lvl),
			Status:   model.SubagentStatus(r.Status),
			Children: []model.SubagentNode{},
			CostUSD:  nil, // SPEC §1.9 — always nil in v1, see subagents.cost_usd's own DDL comment
		}
		if r.AgentType.Valid {
			node.AgentType = r.AgentType.String
		}
		if r.SpawnToolUseID.Valid {
			v := r.SpawnToolUseID.String
			node.SpawnToolUseID = &v
		}
		if r.StartedAt.Valid {
			t := r.StartedAt.Time
			node.StartedAt = &t
		}
		if r.EndedAt.Valid {
			t := r.EndedAt.Time
			node.EndedAt = &t
		}
		if r.ToolCallCount.Valid {
			n := int(r.ToolCallCount.Int32)
			node.ToolCallCount = &n
		}

		parentID := root.AgentID // NULL parent_agent_id => direct child of root (SPEC §4.3)
		if r.ParentAgentID.Valid {
			parentID = r.ParentAgentID.String
		}
		relabelled := parentID
		node.ParentAgentID = &relabelled

		nodes[r.AgentID] = node
		childIDs[parentID] = append(childIDs[parentID], r.AgentID)
	}

	// Deterministic child order (the query already sorts by (lvl,
	// agent_id), but sorting each parent's own child-id list defensively
	// here costs nothing and keeps this function's output order-stable
	// independent of that).
	for pid := range childIDs {
		sort.Strings(childIDs[pid])
	}

	var assemble func(id string) model.SubagentNode
	assemble = func(id string) model.SubagentNode {
		n := nodes[id]
		for _, childID := range childIDs[id] {
			n.Children = append(n.Children, assemble(childID))
		}
		return n
	}

	for _, childID := range childIDs[root.AgentID] {
		root.Children = append(root.Children, assemble(childID))
	}
}

// buildCostAttribution assembles the `cost_attribution` block (SPEC §4.3,
// §1.9) from sessions.cost_by_query_source verbatim, including any key
// Argus has no constant for (SPEC §1.9's "uninterpreted vocabulary" path).
// per_node_available is always false in v1 — the field exists precisely so
// the UI states the limitation instead of rendering a misleading $0.00
// (SPEC §2.3's DDL comment; ticket AC).
func buildCostAttribution(raw []byte) (model.SubagentCostAttribution, error) {
	byQuerySource := map[string]float64{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &byQuerySource); err != nil {
			return model.SubagentCostAttribution{}, fmt.Errorf("postgres: decode cost_by_query_source: %w", err)
		}
	}

	dominant := ""
	dominantCost := -1.0
	total := 0.0
	keys := make([]string, 0, len(byQuerySource))
	for k := range byQuerySource {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic dominant-key tie-break
	for _, k := range keys {
		v := byQuerySource[k]
		total += v
		if v > dominantCost {
			dominant, dominantCost = k, v
		}
	}
	other := total - dominantCost
	if dominantCost < 0 {
		other = 0
	}

	return model.SubagentCostAttribution{
		ByQuerySource:       byQuerySource,
		DominantQuerySource: dominant,
		OtherQuerySourceUSD: other,
		PerNodeAvailable:    false,
		Note:                "Claude Code does not emit per-agent cost; api_request carries query_source only.",
	}, nil
}
