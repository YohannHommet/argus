-- read_sessions.sql holds the FIXED, single-statement queries P3-02's
-- GetSession and ListTurns drive through sqlc (SPEC §3.3: "sqlc for
-- everything fixed"). ListSessions is the dynamic-filter, dynamic-sort
-- query SPEC §3.3 explicitly carves out ("the three dynamic-filter queries
-- ... are hand-built with a whitelist-based clause builder in
-- postgres/filter.go") — it stays hand-written pgx SQL in read_sessions.go,
-- exactly like write.go's own big dynamic statements, and is NOT here.

-- name: GetSessionRow :one
-- The full sessions row GetSession needs to build SessionSummary +
-- SessionDetail's own fields (first_seen_at, user, organization_id). A
-- plain PK lookup — no filter, no sort — hence sqlc rather than filter.go.
SELECT id, vendor, project, cwd, status, start_type, started_at, ended_at,
       first_seen_at, last_event_at,
       turn_count, event_count, tool_call_count, tool_reject_count,
       subagent_count, error_count,
       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
       cost_usd, cost_estimated_usd, cost_by_query_source, models,
       app_version, entrypoint, terminal_type, user_email, organization_id
FROM sessions
WHERE id = sqlc.arg(session_id);

-- name: ListTurnsBySession :many
-- Reader.ListTurns (SPEC §3.3 note: "takes no filter/page per SPEC §3.3" —
-- every turn of a session comes back in one page). Ordered to match
-- turns_session_started_idx (SPEC §2.1: "turns (session_id, started_at
-- NULLS LAST)"); NULLS LAST is explicit here even though it is Postgres's
-- default for ASC because relying on the default would silently stop
-- matching the index's declared order if that default were ever changed.
SELECT session_id, prompt_id, turn_index, started_at, ended_at,
       first_seen_at, last_event_at, duration_ms, status,
       api_request_count, tool_call_count, tool_reject_count, error_count,
       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
       cost_usd, cost_estimated_usd, models
FROM turns
WHERE session_id = sqlc.arg(session_id)
ORDER BY started_at NULLS LAST;

-- name: SessionPermissionModeHistory :many
-- SessionDetail.PermissionModeHistory (SPEC §4.3: "{ts, from, to,
-- trigger}"). Sourced from `permission.mode_changed` events (SPEC §1.5.1:
-- "permission_mode = attrs.to_mode (from_mode in attrs)"; trigger is an
-- additional attrs field per docs/research/telemetry-surfaces.md). `to` is
-- the promoted permission_mode column; `from`/`trigger` come out of attrs,
-- since neither is promoted to its own column. The explicit `::text` casts
-- on the jsonb `->>` results are load-bearing, not decorative: without
-- them sqlc cannot resolve the operator's result type from its catalog and
-- generates `interface{}` fields instead of `string` (verified against
-- sqlc v1.31.1 with this schema).
SELECT ts,
       COALESCE((attrs->>'from_mode')::text, '') AS from_mode,
       COALESCE(permission_mode, '') AS to_mode,
       COALESCE((attrs->>'trigger')::text, '') AS trigger
FROM events
WHERE session_id = sqlc.arg(session_id) AND kind = 'permission.mode_changed'
ORDER BY ts, seq;

-- Top-tools and hook-latency percentiles (SessionDetail.TopTools,
-- SessionDetail.HookLatency's p50_ms/p95_ms) are hand-written pgx queries in
-- read_sessions.go, NOT sqlc, despite being fixed single-statement queries:
-- sqlc v1.31.1 infers `percentile_cont(...) WITHIN GROUP (...)`'s result
-- column as NOT NULL (verified empirically — it generated a plain `float64`/
-- `int64` field, not a pointer), which is wrong whenever every row in a
-- group has a NULL `duration_ms` and the aggregate itself returns SQL NULL;
-- pgx then fails to Scan that NULL into the non-pointer destination sqlc
-- generated. Hand-writing these two queries with explicit `*float64`/
-- `*int64` Scan destinations sidesteps the misinference entirely rather
-- than fighting it with query-shape workarounds. The per-hook-event p50
-- breakdown (HookLatency.by_hook_event) is hand-written for the same reason.

-- name: SessionDecisionTotals :one
-- SessionDetail.DecisionSummary's accept/reject/exact_share (SPEC §4.3).
-- `decision` is unconstrained vendor vocabulary (SPEC §0); accept/reject
-- match the two values docs/research/telemetry-surfaces.md documents
-- (`decision ∈ accept|reject`) — any other value (including NULL, a call
-- never decided) counts toward neither. exact_share is the fraction of
-- decided calls (accept or reject) whose correlation is not 'heuristic'
-- (SPEC §4.3); the Go caller treats a zero-decision session as 1.0 (vacuously
-- exact — nothing to be inexact about), since dividing by zero here would
-- otherwise surface as SQL NULL.
SELECT
    count(*) FILTER (WHERE decision = 'accept')::int AS accept,
    count(*) FILTER (WHERE decision = 'reject')::int AS reject,
    count(*) FILTER (WHERE decision IN ('accept', 'reject'))::int AS decided,
    count(*) FILTER (WHERE decision IN ('accept', 'reject') AND correlation <> 'heuristic')::int AS exact_decided
FROM tool_calls
WHERE session_id = sqlc.arg(session_id);

-- name: SessionDecisionBySource :many
-- SessionDetail.DecisionSummary.by_source (SPEC §4.3): per-decision_source
-- counts of decided (accept or reject) calls. Keys are raw decision_source
-- values, unconstrained (SPEC §1.9) — grouped verbatim, never mapped onto a
-- closed set.
SELECT COALESCE(decision_source, '') AS decision_source, count(*)::int AS n
FROM tool_calls
WHERE session_id = sqlc.arg(session_id) AND decision IN ('accept', 'reject')
GROUP BY decision_source;

-- name: SessionSourcesSeen :many
-- SessionDetail.SourcesSeen (SPEC §4.3): the distinct event `source` values
-- observed for this session (otel_log/otel_metric/hook/sim, SPEC §1.3) —
-- read straight off `events`, not projected anywhere, since it changes only
-- as new sources of telemetry for the session appear.
SELECT DISTINCT source
FROM events
WHERE session_id = sqlc.arg(session_id)
ORDER BY source;

