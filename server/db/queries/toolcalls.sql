-- toolcalls.sql holds the fixed, single-statement queries P2-07's
-- upsertToolCalls (internal/store/postgres/upsert_toolcall.go) drives
-- through sqlc (SPEC §3.3, matching write.sql's own rationale): the
-- store-side reads the heuristic needs (open call candidates, existing
-- keyless-ordinal counts) and the two session/turn counter recomputes. The
-- big unnest-driven tool_calls upsert itself stays hand-written pgx SQL in
-- upsert_toolcall.go, exactly like sessions/turns already do — sqlc
-- generates functions that call db.Query/QueryRow directly, which gains
-- nothing over hand-written SQL for a bulk unnest statement queued in a
-- pgx.Batch-adjacent *pgx.Tx (see write.go's package doc for the fuller
-- reasoning, which applies here verbatim).

-- name: GetOpenToolCalls :many
-- Candidate calls a keyless (no tool_use_id) contribution's heuristic match
-- might attach to (SPEC §1.6): any tool_calls row for these sessions not
-- yet ended, whether it is itself keyed (tool_use_id present, correlation
-- otel_only/exact, awaiting hook enrichment) or already a keyless hook-only
-- call awaiting its matching PostToolUse. "open" == ended_at IS NULL — a
-- store-side judgment call, not something the pure
-- normalize.AssignKeylessContributions function decides (see correlate.go's
-- package doc, lead note 1).
SELECT id, session_id, prompt_id, tool_name, started_at, correlation
FROM tool_calls
WHERE session_id = ANY(sqlc.arg(session_ids)::text[])
  AND ended_at IS NULL;

-- name: CountKeylessToolCalls :many
-- Per-(session, prompt, tool) count of existing keyless (tool_use_id IS
-- NULL) tool_calls rows, seeding AssignKeylessContributions's ordinal
-- allocator (SPEC §1.6: ordinal is the 0-based index of a keyless call
-- among others sharing its key, in (ts, seq) order). Counts ALL matching
-- rows, not just open ones — an already-ended call still occupies its
-- ordinal slot, and ordinals must never be reused (determinism, P3-10).
SELECT session_id, COALESCE(prompt_id, '')::text AS prompt_id, tool_name, count(*) AS n
FROM tool_calls
WHERE tool_use_id IS NULL AND session_id = ANY(sqlc.arg(session_ids)::text[])
GROUP BY session_id, prompt_id, tool_name;

-- name: RecomputeSessionToolCallCounts :exec
-- sessions.tool_call_count / tool_reject_count (SPEC §1.6 projections
-- table): recomputed from tool_calls itself, not incremented per event, so
-- a deduped/redelivered event can never double-count (lead note 4) — the
-- tool_calls upsert is already idempotent per id, so COUNT(*) over it is
-- always correct regardless of how many times the underlying events were
-- redelivered.
UPDATE sessions s SET tool_call_count = c.total, tool_reject_count = c.rejects
FROM (
    SELECT session_id, count(*)::int AS total,
           count(*) FILTER (WHERE decision = 'reject')::int AS rejects
    FROM tool_calls
    WHERE session_id = ANY(sqlc.arg(session_ids)::text[])
    GROUP BY session_id
) c
WHERE s.id = c.session_id;

-- name: RecomputeTurnToolCallCounts :exec
-- turns.tool_call_count / tool_reject_count — same recompute-not-increment
-- reasoning as RecomputeSessionToolCallCounts, scoped to turns that carry a
-- prompt_id (SPEC §2.1: turns are keyed by (session_id, prompt_id), so a
-- tool_calls row with a NULL prompt_id has no turn to update).
UPDATE turns t SET tool_call_count = c.total, tool_reject_count = c.rejects
FROM (
    SELECT session_id, prompt_id, count(*)::int AS total,
           count(*) FILTER (WHERE decision = 'reject')::int AS rejects
    FROM tool_calls
    WHERE prompt_id IS NOT NULL AND session_id = ANY(sqlc.arg(session_ids)::text[])
    GROUP BY session_id, prompt_id
) c
WHERE t.session_id = c.session_id AND t.prompt_id = c.prompt_id;
