-- subagents.sql holds the fixed, single-statement queries P2-08's
-- upsertSubagents (internal/store/postgres/upsert_subagent.go) drives
-- through sqlc (SPEC §3.3, matching toolcalls.sql's own rationale): the
-- parent-depth lookup, the tool-hook-coverage check that decides NULL vs 0
-- for tool_call_count (SPEC §1.9), and the two session/subagent counter
-- recomputes. The big unnest-driven subagents upsert itself stays
-- hand-written pgx SQL in upsert_subagent.go, exactly like
-- tool_calls/sessions already do.

-- name: GetSubagentDepths :many
-- Every existing subagents row for these sessions, feeding the write-side
-- depth-chain resolution (SPEC §2.3 "depth ... from the parent chain with a
-- cap", lead note 4): a fresh SubagentStart looks up its own parent's
-- already-stored depth here (one hop only — see upsert_subagent.go's
-- resolveDepths doc for why a single hop is sufficient and how a
-- still-unresolved ancestor self-heals via SubagentTree's read-time
-- recursive CTE instead of a write-time recursive walk).
SELECT session_id, agent_id, depth
FROM subagents
WHERE session_id = ANY(sqlc.arg(session_ids)::text[]);

-- name: SessionsWithToolHookCoverage :many
-- Sessions (among the given candidates) that have at least one tool_calls
-- row NOT sourced purely from OTel (SPEC §1.9: "only with hooks enabled").
-- correlation <> 'otel_only' is the signal that a hook-sourced tool.* event
-- reached this session at all — the same signal tool_calls.agent_id itself
-- depends on, since agent_id is hook-only (SPEC §1.5.3). A session absent
-- from this result has no tool-level hook coverage, so every subagent's
-- tool_call_count in it must stay NULL, never 0 (§1.9: "0 would be a lie").
SELECT DISTINCT session_id
FROM tool_calls
WHERE session_id = ANY(sqlc.arg(session_ids)::text[]) AND correlation <> 'otel_only';

-- name: RecomputeSubagentToolCallCounts :exec
-- subagents.tool_call_count (SPEC §1.9, §2.3): recomputed from tool_calls
-- itself, not incremented per event (same reasoning as
-- RecomputeSessionToolCallCounts) — COUNT(*) is idempotent under
-- redelivery. hook_coverage_sessions gates the NULL-vs-0 distinction
-- (§1.9): a subagent in a session with confirmed tool-level hook coverage
-- gets its real count (0 is an honest "no tool calls", not a lie, once
-- coverage is established); a subagent in a session with none gets NULL
-- ("we cannot know"), regardless of any spurious tool_calls.agent_id match
-- (which cannot exist there in practice, since agent_id is hook-only).
UPDATE subagents sa SET tool_call_count =
    CASE WHEN sa.session_id = ANY(sqlc.arg(hook_coverage_sessions)::text[])
         THEN (
             SELECT count(*)::int FROM tool_calls tc
             WHERE tc.session_id = sa.session_id AND tc.agent_id = sa.agent_id
         )
         ELSE NULL
    END
WHERE sa.session_id = ANY(sqlc.arg(session_ids)::text[]);

-- name: RecomputeSessionSubagentCount :exec
-- sessions.subagent_count (SPEC §1.6 projections table / §2.1): recomputed
-- from subagents itself, scoped by the caller to sessions that actually had
-- a subagent.start/subagent.stop contribution this batch (recomputing it
-- for every session touched by any event would be harmless but wasteful,
-- since subagent_count can only change when the subagents table itself
-- changes).
UPDATE sessions s SET subagent_count = c.total
FROM (
    SELECT session_id, count(*)::int AS total
    FROM subagents
    WHERE session_id = ANY(sqlc.arg(session_ids)::text[])
    GROUP BY session_id
) c
WHERE s.id = c.session_id;

-- The three queries below back Reader.SubagentTree (subagent_tree.go, SPEC
-- §4.3, §2.5: "subagents (session_id, parent_agent_id) + recursive CTE").
-- They are fixed, single-session-parameter statements — no dynamic filter —
-- so, per SPEC §3.3 ("sqlc for everything fixed"), they go through sqlc
-- rather than joining ListSessions/ListEvents/ListToolCalls's hand-built
-- clause-builder category.

-- name: GetSubagentTree :many
-- The subagent tree for one session, computed by walking the LIVE
-- parent_agent_id chain at read time rather than trusting the stored
-- `depth` column (see upsert_subagent.go's package doc for why the write
-- side cannot always know the correct depth at write time). The
-- `WHERE t.lvl < 16` guard in the recursive term is the actual safety
-- property AC 3 asks for: without it, a parent_agent_id CYCLE reachable
-- from a root-level (parent_agent_id IS NULL) row would make this query
-- loop forever, since a recursive CTE does not deduplicate already-visited
-- rows on its own. Capped at depth 16 (SPEC §4.3), matching
-- subagentMaxDepth in upsert_subagent.go. An orphaned cycle with NO member
-- reachable from a root-level row (every node's parent_agent_id points to
-- another non-root node) is excluded from the tree entirely rather than
-- looping — the same "never buffer, never hang" posture as the rest of the
-- ingest path (SPEC §1.7).
WITH RECURSIVE tree AS (
    SELECT session_id, agent_id, parent_agent_id, agent_type, prompt_id, spawn_tool_use_id,
           started_at, ended_at, status, tool_call_count, cost_usd, 1 AS lvl
    FROM subagents
    WHERE subagents.session_id = sqlc.arg(session_id) AND parent_agent_id IS NULL
    UNION ALL
    SELECT s.session_id, s.agent_id, s.parent_agent_id, s.agent_type, s.prompt_id, s.spawn_tool_use_id,
           s.started_at, s.ended_at, s.status, s.tool_call_count, s.cost_usd, t.lvl + 1
    FROM subagents s
    JOIN tree t ON s.session_id = t.session_id AND s.parent_agent_id = t.agent_id
    WHERE t.lvl < 16
)
SELECT agent_id, parent_agent_id, agent_type, prompt_id, spawn_tool_use_id,
       started_at, ended_at, status, tool_call_count, cost_usd, lvl
FROM tree
ORDER BY lvl, agent_id;

-- name: GetSessionForSubagentTree :one
-- The session-level facts the synthetic `root` node borrows (SPEC §4.3
-- lead note 2): root has no subagents row of its own (the main agent never
-- emits SubagentStart), so its started_at/ended_at/status are the
-- session's own lifecycle — the main agent's lifecycle IS the session's,
-- SPEC §1.1's hierarchy. cost_by_query_source feeds the sibling
-- `cost_attribution` block (SPEC §1.9), not root itself (root.cost_usd
-- stays NULL — see subagent_tree.go's doc on why that is NOT "session cost
-- minus children").
SELECT started_at, ended_at, status, cost_by_query_source
FROM sessions
WHERE id = sqlc.arg(session_id);

-- name: SubagentTreeToolCallStats :one
-- has_hook_coverage answers the same SPEC §1.9 NULL-vs-0 question
-- RecomputeSubagentToolCallCounts answers for real subagent rows, applied
-- to the synthetic root: root_tool_calls counts tool_calls with a NULL
-- agent_id (hook-attributed to no subagent, i.e. the main agent's own tool
-- use) — meaningful only when has_hook_coverage is true, per the same
-- honesty rule (§1.9: "0 would be a lie" when hooks were never on to
-- report it).
SELECT
    EXISTS(SELECT 1 FROM tool_calls WHERE tool_calls.session_id = sqlc.arg(session_id) AND correlation <> 'otel_only') AS has_hook_coverage,
    (SELECT count(*)::int FROM tool_calls WHERE tool_calls.session_id = sqlc.arg(session_id) AND agent_id IS NULL) AS root_tool_calls;
