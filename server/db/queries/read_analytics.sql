-- read_analytics.sql holds the fixed-shape queries backing
-- Reader.AnalyticsSummary/AnalyticsSeries/AnalyticsBreakdown/
-- AnalyticsDecisions (SPEC §3.3, §4.3, P3-06). SPEC §3.3: "sqlc for
-- everything fixed" — every AnalyticsFilter field (Project/Vendor/Model) is
-- an optional OR-set exactly like SessionFilter's, but unlike
-- ListSessions/ListEvents/ListToolCalls there is no dynamic sort key and no
-- keyset pagination, so the whole "OR within a field, empty means no
-- restriction" shape (SPEC §4.1) is expressible as ONE static statement per
-- query via `cardinality(...) = 0 OR column = ANY(...)` — no hand-built SQL
-- text needed. group_by/dimension pick a COLUMN, not a value, which sqlc
-- cannot parameterize directly; those use a `CASE sqlc.arg(...) WHEN ...`
-- expression instead; group_by/dimension are one of a handful of
-- Argus-invented, code-validated strings (never raw client input reaching
-- SQL text), matching the whitelist spirit of filter.go's clauseBuilder.
--
-- SPEC §2.5: every analytics read hits a rollup; no v1 endpoint aggregates
-- over `events` at request time. rollup_hourly/rollup_daily carry no
-- tool_name/decision_source/error_type/query_source columns (SPEC §2.4:
-- query_source is deliberately absent; the others simply were never in the
-- rollup schema), so BreakdownToolCalls/DecisionCounts/DecisionBySource read
-- tool_calls (not events — the same exception SPEC §2.5's index map already
-- grants the decisions drill-down: "tool_calls ... for the drill-down") and
-- BreakdownQuerySource reads sessions.cost_by_query_source directly, per the
-- ticket note that this split "lives on sessions only" (SPEC §2.4).

-- name: SummaryAttributable :one
-- The four model-attributable Summary counters (SPEC §4.3: "only
-- llm.request events carry a model, so only these counters are
-- model-attributable: api_requests, api_errors, tokens.*, cost.*"). Always
-- computed — AnalyticsSummary applies the model filter here and never on
-- SummaryNonAttributable's query, which is what makes the model-filtered
-- response's null/not_attributable split possible.
SELECT
    COALESCE(SUM(api_requests), 0)::bigint AS api_requests,
    COALESCE(SUM(api_errors), 0)::bigint AS api_errors,
    COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
    COALESCE(SUM(cost_reported_usd), 0)::numeric AS cost_reported_usd,
    COALESCE(SUM(cost_estimated_usd), 0)::numeric AS cost_estimated_usd
FROM rollup_hourly
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND source = sqlc.arg(source_kind)::text
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR vendor = ANY(sqlc.arg(vendors)::text[]))
  AND (cardinality(sqlc.arg(models)::text[]) = 0 OR model = ANY(sqlc.arg(models)::text[]));

-- name: SummaryNonAttributable :one
-- The remaining Summary counters (sessions, turns, tool_calls, tool_rejects,
-- loc, active_seconds): never restricted by model, since only model=''
-- rows carry them (SPEC §2.4's rollup pass groups every non-model-
-- attributable metric under model=""). AnalyticsSummary calls this query
-- only when no ?model= filter is active (SPEC §4.3's null-under-model-
-- filter rule); when one is, these counters are reported as nil pointers
-- instead of being (mis)computed here.
SELECT
    COALESCE(SUM(sessions_started), 0)::bigint AS sessions_started,
    COALESCE(SUM(turns), 0)::bigint AS turns,
    COALESCE(SUM(tool_calls), 0)::bigint AS tool_calls,
    COALESCE(SUM(tool_rejects), 0)::bigint AS tool_rejects,
    COALESCE(SUM(loc_added), 0)::bigint AS loc_added,
    COALESCE(SUM(loc_removed), 0)::bigint AS loc_removed,
    COALESCE(SUM(active_seconds), 0)::bigint AS active_seconds
FROM rollup_hourly
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND source = sqlc.arg(source_kind)::text
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR vendor = ANY(sqlc.arg(vendors)::text[]));

-- name: MetricsOnlyProjects :many
-- Summary.metrics_only_projects (SPEC §4.3): projects seen in this window's
-- source='metric' rollup rows with NO source='event' rows of their own —
-- projects Argus knows about only through the metrics exporter.
SELECT DISTINCT m.project::text AS project
FROM rollup_hourly m
WHERE m.bucket >= sqlc.arg(from_ts)::timestamptz AND m.bucket < sqlc.arg(to_ts)::timestamptz
  AND m.source = 'metric' AND m.project <> ''
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR m.project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR m.vendor = ANY(sqlc.arg(vendors)::text[]))
  AND NOT EXISTS (
      SELECT 1 FROM rollup_hourly e
      WHERE e.project = m.project AND e.source = 'event'
        AND e.bucket >= sqlc.arg(from_ts)::timestamptz AND e.bucket < sqlc.arg(to_ts)::timestamptz
  )
ORDER BY m.project;

-- name: SeriesHourly :many
-- One row per (hour bucket, group_key) with every raw column Series' nine
-- TimeseriesMetric values need summed; read_analytics.go picks the column
-- the request's Grouping.Metric names in Go rather than here, so this one
-- statement serves all nine metrics. group_by names which column becomes
-- group_key ('none' -> the constant '' — a single series); Grouping.GroupBy
-- is validated against this exact set in Go before the query runs, so the
-- ELSE branch is unreachable in practice, not a silent fallback for bad
-- input.
SELECT
    bucket,
    (CASE sqlc.arg(group_by)::text
        WHEN 'project' THEN project
        WHEN 'model' THEN model
        WHEN 'vendor' THEN vendor
        ELSE ''
    END)::text AS group_key,
    COALESCE(SUM(sessions_started), 0)::bigint AS sessions_started,
    COALESCE(SUM(turns), 0)::bigint AS turns,
    COALESCE(SUM(api_requests), 0)::bigint AS api_requests,
    COALESCE(SUM(api_errors), 0)::bigint AS api_errors,
    COALESCE(SUM(tool_calls), 0)::bigint AS tool_calls,
    COALESCE(SUM(tool_rejects), 0)::bigint AS tool_rejects,
    COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
    COALESCE(SUM(cost_reported_usd), 0)::numeric AS cost_reported_usd,
    COALESCE(SUM(cost_estimated_usd), 0)::numeric AS cost_estimated_usd,
    COALESCE(SUM(loc_added), 0)::bigint AS loc_added,
    COALESCE(SUM(loc_removed), 0)::bigint AS loc_removed
FROM rollup_hourly
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND source = sqlc.arg(source_kind)::text
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR vendor = ANY(sqlc.arg(vendors)::text[]))
  AND (cardinality(sqlc.arg(models)::text[]) = 0 OR model = ANY(sqlc.arg(models)::text[]))
GROUP BY bucket, group_key
ORDER BY bucket, group_key;

-- name: SeriesDaily :many
-- Same shape as SeriesHourly against rollup_daily, for windows > 7 days
-- (SPEC §4.3's bucket auto-selection).
SELECT
    bucket,
    (CASE sqlc.arg(group_by)::text
        WHEN 'project' THEN project
        WHEN 'model' THEN model
        WHEN 'vendor' THEN vendor
        ELSE ''
    END)::text AS group_key,
    COALESCE(SUM(sessions_started), 0)::bigint AS sessions_started,
    COALESCE(SUM(turns), 0)::bigint AS turns,
    COALESCE(SUM(api_requests), 0)::bigint AS api_requests,
    COALESCE(SUM(api_errors), 0)::bigint AS api_errors,
    COALESCE(SUM(tool_calls), 0)::bigint AS tool_calls,
    COALESCE(SUM(tool_rejects), 0)::bigint AS tool_rejects,
    COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
    COALESCE(SUM(cost_reported_usd), 0)::numeric AS cost_reported_usd,
    COALESCE(SUM(cost_estimated_usd), 0)::numeric AS cost_estimated_usd,
    COALESCE(SUM(loc_added), 0)::bigint AS loc_added,
    COALESCE(SUM(loc_removed), 0)::bigint AS loc_removed
FROM rollup_daily
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND source = sqlc.arg(source_kind)::text
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR vendor = ANY(sqlc.arg(vendors)::text[]))
  AND (cardinality(sqlc.arg(models)::text[]) = 0 OR model = ANY(sqlc.arg(models)::text[]))
GROUP BY bucket, group_key
ORDER BY bucket, group_key;

-- name: BreakdownRollup :many
-- Breakdown by a rollup-backed dimension (dimension=project|model — SPEC
-- §4.3's breakdown enum). "calls" is api_requests (the LLM-call count the
-- rollup actually stores); "cost" is reported+estimated; "tokens" is the
-- sum of all four token columns. All three metric columns are always
-- computed — read_analytics.go selects the one Dimension.Metric names.
SELECT
    (CASE sqlc.arg(group_by)::text
        WHEN 'project' THEN project
        WHEN 'model' THEN model
        ELSE ''
    END)::text AS key,
    COALESCE(SUM(api_requests), 0)::bigint AS calls,
    COALESCE(SUM(cost_reported_usd + cost_estimated_usd), 0)::numeric AS cost,
    COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_creation_tokens), 0)::bigint AS tokens
FROM rollup_hourly
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND source = sqlc.arg(source_kind)::text
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR vendor = ANY(sqlc.arg(vendors)::text[]))
  AND (cardinality(sqlc.arg(models)::text[]) = 0 OR model = ANY(sqlc.arg(models)::text[]))
GROUP BY key;

-- name: BreakdownToolCalls :many
-- Breakdown by a tool_calls-backed dimension (dimension=tool|decision_source
-- |error_type — none of which rollup_hourly carries a column for, SPEC
-- §2.4). "calls" is the only metric these three dimensions can answer
-- honestly (tool_calls has no cost/token columns); read_analytics.go always
-- reports calls for them regardless of Dimension.Metric, documented there.
-- A NULL decision_source/error_type row is excluded when that is the
-- dimension being broken down by (nothing meaningful to key it under), but
-- included (grouped under key '') when it is the OTHER of the two, since a
-- dimension=tool breakdown should still count every tool call.
SELECT
    (CASE sqlc.arg(dimension)::text
        WHEN 'tool' THEN tc.tool_name
        WHEN 'decision_source' THEN COALESCE(tc.decision_source, '')
        WHEN 'error_type' THEN COALESCE(tc.error_type, '')
        ELSE ''
    END)::text AS key,
    count(*)::bigint AS calls
FROM tool_calls tc
WHERE tc.started_at >= sqlc.arg(from_ts)::timestamptz AND tc.started_at < sqlc.arg(to_ts)::timestamptz
  AND (sqlc.arg(dimension)::text <> 'decision_source' OR tc.decision_source IS NOT NULL)
  AND (sqlc.arg(dimension)::text <> 'error_type' OR tc.error_type IS NOT NULL)
  AND (cardinality(sqlc.arg(projects)::text[]) = 0
       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY(sqlc.arg(projects)::text[])))
GROUP BY key;

-- name: BreakdownQuerySource :many
-- Breakdown by dimension=query_source (SPEC §2.4/ticket note: rollups
-- deliberately carry no query_source dimension, so this reads
-- sessions.cost_by_query_source directly — the only place the split
-- exists). Keys are raw, unmapped query_source strings (SPEC §1.9); "cost"
-- is the only metric this source can answer (there is no per-query_source
-- call/token count anywhere in the schema), returned regardless of
-- Dimension.Metric, documented at the call site.
SELECT kv.key::text AS key, SUM(kv.value::numeric)::float8 AS cost
FROM sessions s, LATERAL jsonb_each_text(s.cost_by_query_source) AS kv(key, value)
WHERE s.last_event_at >= sqlc.arg(from_ts)::timestamptz AND s.last_event_at < sqlc.arg(to_ts)::timestamptz
  AND (cardinality(sqlc.arg(projects)::text[]) = 0 OR s.project = ANY(sqlc.arg(projects)::text[]))
  AND (cardinality(sqlc.arg(vendors)::text[]) = 0 OR s.vendor = ANY(sqlc.arg(vendors)::text[]))
  AND (cardinality(sqlc.arg(models)::text[]) = 0 OR s.models && sqlc.arg(models)::text[])
GROUP BY kv.key;

-- name: DecisionCounts :many
-- DecisionMatrixRow's accept/reject/exact_share inputs, grouped by tool_name
-- (SPEC §4.3's decisions matrix). decided/exact_decided let
-- read_analytics.go compute exact_share = exact_decided/decided per row,
-- matching GetSession's SessionDecisionSummary.ExactShare convention
-- (read_sessions.go: vacuously 1.0 when nothing is decided yet).
--
-- m10 fix (pre-Phase-4 audit wave, ticket W3): `decided`/`exact_decided`
-- filter on `tc.decision IN ('accept', 'reject')`, not `tc.decision IS NOT
-- NULL` — `decision` is unconstrained vendor vocabulary (SPEC §0), so a
-- third value (or any future one) inflated `decided` here without moving
-- `accept`/`reject`, disagreeing with SessionDecisionTotals's
-- `decision IN ('accept', 'reject')` (read_sessions.sql:84-85, the
-- convention this query's comment already claimed to match) and making
-- accept+reject != decided. Copies read_sessions.sql's predicate verbatim.
SELECT tc.tool_name,
    count(*) FILTER (WHERE tc.decision = 'accept')::bigint AS accept,
    count(*) FILTER (WHERE tc.decision = 'reject')::bigint AS reject,
    count(*) FILTER (WHERE tc.decision IN ('accept', 'reject'))::bigint AS decided,
    count(*) FILTER (WHERE tc.decision IN ('accept', 'reject') AND tc.correlation <> 'heuristic')::bigint AS exact_decided
FROM tool_calls tc
WHERE tc.started_at >= sqlc.arg(from_ts)::timestamptz AND tc.started_at < sqlc.arg(to_ts)::timestamptz
  AND (cardinality(sqlc.arg(projects)::text[]) = 0
       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY(sqlc.arg(projects)::text[])))
GROUP BY tc.tool_name
ORDER BY tc.tool_name;

-- name: DecisionBySource :many
-- DecisionMatrixRow.by_source (SPEC §4.3): raw decision_source values,
-- unconstrained (SPEC §1.9) — an unseen value groups under its own key like
-- any other, never folded into an "other" bucket.
SELECT tc.tool_name, COALESCE(tc.decision_source, '')::text AS decision_source, count(*)::bigint AS n
FROM tool_calls tc
WHERE tc.started_at >= sqlc.arg(from_ts)::timestamptz AND tc.started_at < sqlc.arg(to_ts)::timestamptz
  AND tc.decision_source IS NOT NULL
  AND (cardinality(sqlc.arg(projects)::text[]) = 0
       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY(sqlc.arg(projects)::text[])))
GROUP BY tc.tool_name, tc.decision_source
ORDER BY tc.tool_name, tc.decision_source;
