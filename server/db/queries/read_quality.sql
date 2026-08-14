-- read_quality.sql holds the fixed-shape queries backing Reader.Facets,
-- Reader.DataQuality, and Reader.UnknownKinds (SPEC §3.3, §4.2, §4.3, P3-08).
-- Reader.HookLatency is hand-written pgx, not sqlc, in read_quality.go
-- itself — the same percentile_cont NOT-NULL mis-inference reason
-- read_analytics.go's decisionWaitPercentiles documents.
--
-- SPEC §2.5: only UnknownKinds' query below (and HookLatency's hand-written
-- one) are allowed to touch `events`, and only bounded to the requested
-- window (§2.5's index-map row: "events (kind, ts DESC) ... bounded to
-- 24h"). FacetProjects/FacetVendors/FacetModels/FacetTools/
-- FacetDecisionSources/FacetQuerySources and DataQualitySnapshot
-- deliberately read sessions/tool_calls/turns/subagents/metric_samples
-- instead — never events — so they need no such bound and are outside the
-- P3-10 EXPLAIN guard's concern entirely. See read_quality.go's DataQuality
-- doc comment for exactly which promoted column proves each "ever seen"
-- flag without reading raw events.

-- name: FacetProjects :many
-- Facets.Projects (SPEC §4.2): distinct non-empty project names ever seen.
SELECT DISTINCT project::text AS project
FROM sessions
WHERE project IS NOT NULL AND project <> ''
ORDER BY project;

-- name: FacetVendors :many
-- Facets.Vendors (SPEC §4.2): distinct vendor values ever seen.
SELECT DISTINCT vendor::text AS vendor
FROM sessions
ORDER BY vendor;

-- name: FacetModels :many
-- Facets.Models (SPEC §4.2): distinct models ever seen, unnested from
-- sessions.models (SPEC §2.1's "text[] of every model used in the session").
SELECT DISTINCT m::text AS model
FROM sessions, unnest(models) AS m
ORDER BY m;

-- name: FacetTools :many
-- Facets.Tools (SPEC §4.2): distinct tool_name values ever seen, sourced
-- from tool_calls (not events — same exception SPEC §2.5 already grants
-- BreakdownToolCalls).
SELECT DISTINCT tool_name::text AS tool_name
FROM tool_calls
ORDER BY tool_name;

-- name: FacetDecisionSources :many
-- Facets.DecisionSources (SPEC §4.2): distinct raw decision_source values
-- ever seen, sourced from tool_calls (SPEC §1.9's "the differentiator
-- column"), matching DecisionBySource's existing precedent.
SELECT DISTINCT decision_source::text AS decision_source
FROM tool_calls
WHERE decision_source IS NOT NULL
ORDER BY decision_source;

-- name: FacetQuerySources :many
-- Facets.QuerySources (SPEC §4.2): distinct raw query_source keys ever
-- seen, sourced from sessions.cost_by_query_source (SPEC §2.4: "the split
-- lives on sessions only"), matching BreakdownQuerySource's existing
-- precedent — includes "" for the absent/main case (SPEC §1.9).
-- jsonb_object_keys errors on a non-object jsonb value; cost_by_query_source
-- is documented (SPEC §2.1) as always a `{}`-shaped map, but a defensive
-- CASE substitutes an empty object for the rare row that is not (e.g. a
-- JSON null written by a test/migration path that never set the column)
-- rather than letting one bad row 500 the whole facets response.
SELECT DISTINCT kv.key::text AS query_source
FROM sessions s, LATERAL jsonb_object_keys(
    CASE WHEN jsonb_typeof(s.cost_by_query_source) = 'object' THEN s.cost_by_query_source ELSE '{}'::jsonb END
) AS kv(key)
ORDER BY kv.key;

-- name: DataQualitySnapshot :one
-- DataQuality (SPEC §4.2's meta data_quality block): four "has Argus ever
-- received X" observations, deliberately answered without reading events
-- (see read_quality.go's DataQuality doc comment for the reasoning behind
-- each column choice below).
SELECT
    EXISTS (SELECT 1 FROM turns WHERE api_request_count > 0) AS logs_exporter_seen,
    EXISTS (SELECT 1 FROM metric_samples) AS metrics_exporter_seen,
    (EXISTS (SELECT 1 FROM tool_calls WHERE correlation IN ('exact', 'hook_only') OR agent_id IS NOT NULL)
        OR EXISTS (SELECT 1 FROM subagents)) AS hooks_seen,
    EXISTS (SELECT 1 FROM tool_calls WHERE file_path IS NOT NULL) AS tool_details_seen;

-- name: UnknownKindGroups :many
-- QualityUnknownKindsResponse.Rows (SPEC §4.3): unmapped event_names grouped
-- within [since, now), one raw attrs sample per group (the earliest, for
-- determinism). SPEC §2.5's documented exception: this is one of the two
-- v1 queries allowed to aggregate over `events`, and only because it is
-- bounded by `ts >= since` (the events (kind, ts DESC) index, per the
-- index-map row).
SELECT
    event_name::text AS event_name,
    source::text AS source,
    count(*)::bigint AS count,
    min(ts)::timestamptz AS first_seen,
    max(ts)::timestamptz AS last_seen,
    (array_agg(attrs ORDER BY ts))[1]::jsonb AS sample
FROM events
WHERE kind = 'unknown' AND ts >= sqlc.arg(since)::timestamptz
GROUP BY event_name, source
ORDER BY count DESC, event_name
LIMIT sqlc.arg(row_limit);
