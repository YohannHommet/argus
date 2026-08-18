package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// explainCase names one read_analytics.go-backed query (SPEC §2.5's "every
// analytics read hits rollup_hourly/rollup_daily ... no v1 endpoint
// aggregates over events at request time") plus the literal parameter
// values EXPLAIN needs to plan it. SQL is copied verbatim from
// internal/store/postgres/gen/read_analytics.sql.go's generated $N-form
// constants (sqlc v1.31.1's compiled output of db/queries/read_analytics.sql
// — see that file's own `-- name: X` queries for the human-authored
// source), NOT re-derived from the `sqlc.arg(...)` source form, because
// Postgres has no idea what `sqlc.arg` means: only sqlc's compiled $N SQL is
// valid to EXPLAIN directly. This file cannot import gen's unexported
// consts (different package, and P3-10's file ownership does not extend to
// touching gen/*.go or refactoring read_analytics.go to expose them), so the
// ten queries are copied here as literal strings instead — a deliberate,
// documented duplication kept in exact sync with gen/read_analytics.sql.go
// by naming which sqlc query each one mirrors.
type explainCase struct {
	name string
	sql  string
	args []any
}

// analyticsExplainCases enumerates EVERY query read_analytics.go's
// AnalyticsSummary/AnalyticsSeries/AnalyticsBreakdown/AnalyticsDecisions
// issue (SPEC §3.3, §4.3) — SummaryAttributable/SummaryNonAttributable/
// MetricsOnlyProjects/SeriesHourly/SeriesDaily/BreakdownRollup/
// BreakdownToolCalls/BreakdownQuerySource/DecisionCounts/DecisionBySource.
// Argument values are arbitrary but well-typed placeholders (EXPLAIN plans a
// query the same way regardless of which values are bound, since these
// statements have no value-dependent partial index or CHECK-constraint
// exclusion to trigger) — a fixed one-hour window, a non-empty enum value
// for the group_by/dimension CASE arguments, and empty filter arrays (SPEC
// §4.1's "empty means no restriction").
func analyticsExplainCases(now time.Time) []explainCase {
	from, to := now.Add(-time.Hour), now
	empty := []string{}

	return []explainCase{
		{"SummaryAttributable", `
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
			WHERE bucket >= $1::timestamptz AND bucket < $2::timestamptz
			  AND source = $3::text
			  AND (cardinality($4::text[]) = 0 OR project = ANY($4::text[]))
			  AND (cardinality($5::text[]) = 0 OR vendor = ANY($5::text[]))
			  AND (cardinality($6::text[]) = 0 OR model = ANY($6::text[]))`,
			[]any{from, to, "event", empty, empty, empty}},

		{"SummaryNonAttributable", `
			SELECT
			    COALESCE(SUM(sessions_started), 0)::bigint AS sessions_started,
			    COALESCE(SUM(turns), 0)::bigint AS turns,
			    COALESCE(SUM(tool_calls), 0)::bigint AS tool_calls,
			    COALESCE(SUM(tool_rejects), 0)::bigint AS tool_rejects,
			    COALESCE(SUM(loc_added), 0)::bigint AS loc_added,
			    COALESCE(SUM(loc_removed), 0)::bigint AS loc_removed,
			    COALESCE(SUM(active_seconds), 0)::bigint AS active_seconds
			FROM rollup_hourly
			WHERE bucket >= $1::timestamptz AND bucket < $2::timestamptz
			  AND source = $3::text
			  AND (cardinality($4::text[]) = 0 OR project = ANY($4::text[]))
			  AND (cardinality($5::text[]) = 0 OR vendor = ANY($5::text[]))`,
			[]any{from, to, "event", empty, empty}},

		{"MetricsOnlyProjects", `
			SELECT DISTINCT m.project::text AS project
			FROM rollup_hourly m
			WHERE m.bucket >= $1::timestamptz AND m.bucket < $2::timestamptz
			  AND m.source = 'metric' AND m.project <> ''
			  AND (cardinality($3::text[]) = 0 OR m.project = ANY($3::text[]))
			  AND (cardinality($4::text[]) = 0 OR m.vendor = ANY($4::text[]))
			  AND NOT EXISTS (
			      SELECT 1 FROM rollup_hourly e
			      WHERE e.project = m.project AND e.source = 'event'
			        AND e.bucket >= $1::timestamptz AND e.bucket < $2::timestamptz
			  )
			ORDER BY m.project`,
			[]any{from, to, empty, empty}},

		{"SeriesHourly", `
			SELECT
			    bucket,
			    (CASE $1::text
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
			WHERE bucket >= $2::timestamptz AND bucket < $3::timestamptz
			  AND source = $4::text
			  AND (cardinality($5::text[]) = 0 OR project = ANY($5::text[]))
			  AND (cardinality($6::text[]) = 0 OR vendor = ANY($6::text[]))
			  AND (cardinality($7::text[]) = 0 OR model = ANY($7::text[]))
			GROUP BY bucket, group_key
			ORDER BY bucket, group_key`,
			[]any{"project", from, to, "event", empty, empty, empty}},

		{"SeriesDaily", `
			SELECT
			    bucket,
			    (CASE $1::text
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
			WHERE bucket >= $2::timestamptz AND bucket < $3::timestamptz
			  AND source = $4::text
			  AND (cardinality($5::text[]) = 0 OR project = ANY($5::text[]))
			  AND (cardinality($6::text[]) = 0 OR vendor = ANY($6::text[]))
			  AND (cardinality($7::text[]) = 0 OR model = ANY($7::text[]))
			GROUP BY bucket, group_key
			ORDER BY bucket, group_key`,
			[]any{"project", from, to, "event", empty, empty, empty}},

		{"BreakdownRollup", `
			SELECT
			    (CASE $1::text
			        WHEN 'project' THEN project
			        WHEN 'model' THEN model
			        ELSE ''
			    END)::text AS key,
			    COALESCE(SUM(api_requests), 0)::bigint AS calls,
			    COALESCE(SUM(cost_reported_usd + cost_estimated_usd), 0)::numeric AS cost,
			    COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_creation_tokens), 0)::bigint AS tokens
			FROM rollup_hourly
			WHERE bucket >= $2::timestamptz AND bucket < $3::timestamptz
			  AND source = $4::text
			  AND (cardinality($5::text[]) = 0 OR project = ANY($5::text[]))
			  AND (cardinality($6::text[]) = 0 OR vendor = ANY($6::text[]))
			  AND (cardinality($7::text[]) = 0 OR model = ANY($7::text[]))
			GROUP BY key`,
			[]any{"project", from, to, "event", empty, empty, empty}},

		// BreakdownToolCalls/DecisionCounts/DecisionBySource legitimately read
		// tool_calls, not events (SPEC §2.5's documented decisions/drill-down
		// exception) — the guard below asserts NEITHER "events" appears, which
		// tool_calls/sessions plans satisfy trivially since neither name
		// contains that substring.
		{"BreakdownToolCalls", `
			SELECT
			    (CASE $1::text
			        WHEN 'tool' THEN tc.tool_name
			        WHEN 'decision_source' THEN COALESCE(tc.decision_source, '')
			        WHEN 'error_type' THEN COALESCE(tc.error_type, '')
			        ELSE ''
			    END)::text AS key,
			    count(*)::bigint AS calls
			FROM tool_calls tc
			WHERE tc.started_at >= $2::timestamptz AND tc.started_at < $3::timestamptz
			  AND ($1::text <> 'decision_source' OR tc.decision_source IS NOT NULL)
			  AND ($1::text <> 'error_type' OR tc.error_type IS NOT NULL)
			  AND (cardinality($4::text[]) = 0
			       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY($4::text[])))
			GROUP BY key`,
			[]any{"tool", from, to, empty}},

		{"BreakdownQuerySource", `
			SELECT kv.key::text AS key, SUM(kv.value::numeric)::float8 AS cost
			FROM sessions s, LATERAL jsonb_each_text(s.cost_by_query_source) AS kv(key, value)
			WHERE s.last_event_at >= $1::timestamptz AND s.last_event_at < $2::timestamptz
			  AND (cardinality($3::text[]) = 0 OR s.project = ANY($3::text[]))
			  AND (cardinality($4::text[]) = 0 OR s.vendor = ANY($4::text[]))
			  AND (cardinality($5::text[]) = 0 OR s.models && $5::text[])
			GROUP BY kv.key`,
			[]any{from, to, empty, empty, empty}},

		{"DecisionCounts", `
			SELECT tc.tool_name,
			    count(*) FILTER (WHERE tc.decision = 'accept')::bigint AS accept,
			    count(*) FILTER (WHERE tc.decision = 'reject')::bigint AS reject,
			    count(*) FILTER (WHERE tc.decision IN ('accept', 'reject'))::bigint AS decided,
			    count(*) FILTER (WHERE tc.decision IN ('accept', 'reject') AND tc.correlation <> 'heuristic')::bigint AS exact_decided
			FROM tool_calls tc
			WHERE tc.started_at >= $1::timestamptz AND tc.started_at < $2::timestamptz
			  AND (cardinality($3::text[]) = 0
			       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY($3::text[])))
			GROUP BY tc.tool_name
			ORDER BY tc.tool_name`,
			[]any{from, to, empty}},

		{"DecisionBySource", `
			SELECT tc.tool_name, COALESCE(tc.decision_source, '')::text AS decision_source, count(*)::bigint AS n
			FROM tool_calls tc
			WHERE tc.started_at >= $1::timestamptz AND tc.started_at < $2::timestamptz
			  AND tc.decision_source IS NOT NULL
			  AND (cardinality($3::text[]) = 0
			       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY($3::text[])))
			GROUP BY tc.tool_name, tc.decision_source
			ORDER BY tc.tool_name, tc.decision_source`,
			[]any{from, to, empty}},

		// decisionWaitPercentiles (read_analytics.go): hand-written pgx, not
		// sqlc (percentile_cont's NOT-NULL mis-inference, per that function's
		// own doc comment) — still an analytics query the guard must cover.
		{"DecisionWaitPercentiles", `
			SELECT tc.tool_name,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY tc.wait_ms) AS p50_ms,
			       percentile_cont(0.95) WITHIN GROUP (ORDER BY tc.wait_ms) AS p95_ms
			FROM tool_calls tc
			WHERE tc.started_at >= $1 AND tc.started_at < $2
			  AND (cardinality($3::text[]) = 0
			       OR EXISTS (SELECT 1 FROM sessions s WHERE s.id = tc.session_id AND s.project = ANY($3::text[])))
			GROUP BY tc.tool_name`,
			[]any{from, to, empty}},
	}
}

// explainPlanText runs EXPLAIN on sql (with args bound exactly as a real
// call would bind them) and returns the concatenated plan text.
func explainPlanText(t *testing.T, pool *pgxpool.Pool, sql string, args []any) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "EXPLAIN "+sql, args...)
	require.NoError(t, err)
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	return sb.String()
}

// planMentionsEvents reports whether an EXPLAIN plan references the events
// relation. A plain substring check is safe here: none of Argus's other
// table/index names (rollup_hourly, rollup_daily, tool_calls, sessions,
// subagents, metric_samples, metric_series_state, ingest_dedup, job_state,
// model_prices, rollup_dirty) contain "events" as a substring, so this
// cannot false-positive on an unrelated relation name, and a monthly
// `events_YYYY_MM` partition name (were one ever to leak into a plan
// despite this test never touching that table) would trip it too.
func planMentionsEvents(plan string) bool {
	return strings.Contains(plan, "events")
}

// --- The permanent architectural gate (SPEC §2.5, P3-10). ------------------
//
// Enumerates every query read_analytics.go issues and fails if any plan
// mentions `events`. The allow-list below covers the SPEC §2.5-exempted
// data-quality and hook-latency queries (events(kind, ts DESC), bounded to
// the requested window): UnknownKinds.UnknownKindGroups and HookLatency's
// hand-written statement (P3-08, internal/store/postgres/read_quality.go).
// DataQuality is intentionally absent — it never touches `events` at all
// (see read_quality.go's package doc), so it has no query to allow-list.
var dataQualityAllowList = []explainCase{
	{"UnknownKindGroups", `
		SELECT
		    event_name::text AS event_name,
		    source::text AS source,
		    count(*)::bigint AS count,
		    min(ts)::timestamptz AS first_seen,
		    max(ts)::timestamptz AS last_seen,
		    (array_agg(attrs ORDER BY ts))[1]::jsonb AS sample
		FROM events
		WHERE kind = 'unknown' AND ts >= $1::timestamptz
		GROUP BY event_name, source
		ORDER BY count DESC, event_name
		LIMIT $2`,
		[]any{time.Now().UTC().Add(-24 * time.Hour), int32(500)}},

	{"HookLatency", `
		SELECT COALESCE(attrs->>'hook_event', '') AS hook_event,
		       count(*)::bigint AS executions,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95_ms,
		       percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99_ms,
		       count(*) FILTER (WHERE success = false)::bigint AS errors,
		       count(*) FILTER (WHERE COALESCE((attrs->>'num_cancelled')::int, 0) > 0)::bigint AS cancelled
		FROM events
		WHERE kind = 'hook.execution_end' AND ts >= $1 AND ts < $2
		GROUP BY 1
		ORDER BY 1`,
		[]any{time.Now().UTC().Add(-24 * time.Hour), time.Now().UTC()}},
}

func TestExplainGuard_AnalyticsQueriesNeverTouchEvents(t *testing.T) {
	_, pool := newStore(t)
	cases := analyticsExplainCases(time.Now().UTC())
	require.NotEmpty(t, cases, "the guard must enumerate at least one analytics query, or it silently proves nothing")

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := explainPlanText(t, pool, c.sql, c.args)
			require.False(t, planMentionsEvents(plan), "%s's plan must never mention events (SPEC §2.5):\n%s", c.name, plan)
		})
	}
}

// TestExplainGuard_DataQualityAllowList asserts the two conditions SPEC §2.5
// attaches to its exemption, rather than merely printing the plans.
//
// The exemption is conditional — those queries "are exempt and explicitly
// listed in that test's allow-list, **bounded to their windows**" — so an
// allow-listed entry that stopped being window-bounded would be exactly the
// unbounded scan over `events` the gate exists to prevent, while sitting on
// the list that excuses it. Two things are therefore checked per entry:
//
//  1. the plan really does touch `events` — an entry that does not belong on
//     this list at all should be caught, not silently carried; and
//  2. the plan applies a `ts` bound, which is what keeps the scan
//     proportional to the requested window instead of to all of retention.
//
// This test was log-only when first written (t.Logf and no assertion), which
// made a populated allow-list indistinguishable from an empty one.
func TestExplainGuard_DataQualityAllowList(t *testing.T) {
	st, pool := newStore(t)
	require.NotEmpty(t, dataQualityAllowList,
		"an empty allow-list makes this test vacuous: the SPEC §2.5-exempted queries must be listed here so their window-bounding stays asserted")

	// Partitions covering the queries' 24h window must exist first. Without
	// them Postgres prunes every partition away and plans the whole statement
	// as `Result / One-Time Filter: false` — a plan that scans nothing, in
	// which neither the events relation nor a ts bound appears, so both
	// assertions below would be measuring an empty plan rather than the query.
	// (The first assertion still passed on such a plan, because a Group Key
	// mentions `events.event_name` even when no partition is read — which is
	// precisely how a plan-text guard can look green while proving nothing.)
	now := time.Now().UTC()
	require.NoError(t, st.EnsurePartitions(context.Background(), now.Add(-24*time.Hour), now))

	for _, c := range dataQualityAllowList {
		t.Run(c.name, func(t *testing.T) {
			plan := explainPlanText(t, pool, c.sql, c.args)
			require.True(t, planMentionsEvents(plan),
				"%s is allow-listed as an events-touching query but its plan does not mention events — it either does not belong on this list, or the query changed:\n%s", c.name, plan)
			require.True(t, planBoundsTS(plan),
				"%s is exempt from the no-events rule ONLY because it is bounded to the requested window (SPEC §2.5), but its plan shows no ts bound — an unbounded scan over every partition:\n%s", c.name, plan)
		})
	}
}

// planBoundsTS reports whether an EXPLAIN plan constrains `ts`, whether the
// bound shows up as an index condition, a filter, or partition pruning. It
// deliberately looks for the column in a comparison rather than for a
// specific plan node: which of those forms Postgres picks depends on row
// counts and available indexes, and this assertion is about the query being
// window-bounded at all, not about how the planner chose to apply it.
func planBoundsTS(plan string) bool {
	for _, form := range []string{"ts >=", "ts >", "ts <=", "ts <", "ts = "} {
		if strings.Contains(plan, form) {
			return true
		}
	}
	return false
}

// TestExplainGuard_DetectsEventsInPlan is the guard's own regression test:
// it proves planMentionsEvents actually catches a plan that touches events,
// using a synthetic query (never one of read_analytics.go's real
// statements) that deliberately scans the events table. This is the
// permanent, always-run form of the ticket's "prove it FAILS when pointed
// at a deliberately events-touching query" ask — during implementation this
// was additionally verified by hand against a real analytics query (see
// this ticket's report): BreakdownRollup's `FROM rollup_hourly` was
// temporarily changed to read from `events` directly, which made
// TestExplainGuard_AnalyticsQueriesNeverTouchEvents fail exactly as
// expected, then reverted. That manual check is not repeatable as a
// standing test (it would require mutating read_analytics.go's real SQL),
// so this synthetic canary is what stays in the suite to catch a future
// regression in planMentionsEvents itself.
// --- M9 (pre-Phase-4 audit wave, ticket W3): AggregateEventRollup's new
// [from_ts, to_ts) range predicate must let the planner prune partitions
// outside the claimed bucket window, instead of Append-scanning every
// retained `events` partition on every 60s rollup tick. ------------------

// aggregateEventRollupSQL is copied verbatim from sqlc's compiled
// aggregateEventRollup const (internal/store/postgres/gen/rollups.sql.go),
// for the same reason analyticsExplainCases above copies read_analytics.go's
// compiled queries: Postgres can only EXPLAIN sqlc's compiled $N-form SQL,
// not the sqlc.arg(...) source form, and this package cannot import gen's
// unexported consts from a different (_test) package.
const aggregateEventRollupSQL = `
	SELECT
	    date_trunc('hour', e.ts, 'UTC')::timestamptz                        AS bucket,
	    COALESCE(s.project, '')::text                                AS project,
	    e.vendor::text                                                AS vendor,
	    COALESCE(e.model, '')::text                                  AS model,
	    count(*) FILTER (WHERE e.kind = 'session.start')::int         AS sessions_started,
	    count(*) FILTER (WHERE e.kind = 'turn.start')::int            AS turns,
	    count(*) FILTER (WHERE e.kind = 'llm.request')::int           AS api_requests,
	    count(*) FILTER (WHERE e.kind = 'llm.error')::int             AS api_errors,
	    COALESCE(sum(e.input_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint          AS input_tokens,
	    COALESCE(sum(e.output_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint          AS output_tokens,
	    COALESCE(sum(e.cache_read_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint      AS cache_read_tokens,
	    COALESCE(sum(e.cache_creation_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint  AS cache_creation_tokens,
	    COALESCE(sum(e.cost_usd) FILTER (WHERE e.cost_source = 'reported'), 0)::numeric          AS cost_reported_usd,
	    COALESCE(sum(e.input_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint          AS uncosted_input_tokens,
	    COALESCE(sum(e.output_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint         AS uncosted_output_tokens,
	    COALESCE(sum(e.cache_read_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint     AS uncosted_cache_read_tokens,
	    COALESCE(sum(e.cache_creation_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint AS uncosted_cache_creation_tokens
	FROM events e
	LEFT JOIN sessions s ON s.id = e.session_id
	WHERE e.ts >= $1::timestamptz AND e.ts < $2::timestamptz
	  AND date_trunc('hour', e.ts, 'UTC') = ANY($3::timestamptz[])
	GROUP BY 1, 2, 3, 4`

// fetchMetricRowsForRollupSQL is the same kind of verbatim copy, of
// FetchMetricRowsForRollup (metric_samples, the other RANGE-partitioned
// table M9 covers).
const fetchMetricRowsForRollupSQL = `
	SELECT
	    ms.ts, ms.series_hash, ms.name, ms.vendor,
	    COALESCE(s.project, '')::text AS project,
	    ms.value, ms.temporality, ms.dedup_key,
	    COALESCE(ms.attrs->>'type', '')::text      AS attr_type,
	    COALESCE(ms.attrs->>'decision', '')::text  AS attr_decision,
	    COALESCE(ms.attrs->>'model', '')::text     AS attr_model
	FROM metric_samples ms
	LEFT JOIN sessions s ON s.id = ms.session_id
	WHERE ms.ts >= $1::timestamptz AND ms.ts < $2::timestamptz
	  AND date_trunc('hour', ms.ts, 'UTC') = ANY($3::timestamptz[])
	ORDER BY ms.series_hash, ms.ts`

// prunedToSinglePartition reports whether an EXPLAIN plan shows structural
// evidence that the planner eliminated every candidate partition but the
// ones named in want, given the full universe of partition names present
// (all). This covers both shapes Postgres's planner can produce for a
// range predicate narrow enough to match only some partitions: an Append
// node listing "Subplans Removed: N" (when it still considers pruning worth
// showing explicitly), or — for a range this narrow, matching exactly one
// partition — collapsing the Append away entirely and planning directly
// against that partition, which is a *stronger* pruning result than
// "Subplans Removed" but leaves no such node to look for. Either shape is
// valid proof the M9 range predicate is sargable against the partition key
// (unlike the pre-fix STABLE `date_trunc(...) = ANY(...)` alone, which
// cannot prune at all — the audit's own repro: "Append -> Seq Scan" on
// every one of six partitions, no "Subplans Removed"): what matters is that
// none of the excluded partitions' names appear in the plan at all.
func prunedToSinglePartition(t *testing.T, plan string, all, want []string) bool {
	t.Helper()
	if strings.Contains(plan, "Subplans Removed") {
		return true
	}
	for _, name := range all {
		excluded := true
		for _, w := range want {
			if name == w {
				excluded = false
			}
		}
		if excluded && strings.Contains(plan, name) {
			return false
		}
	}
	return true
}

// seedMonthOfEvents writes n LLM-request events, one per minute starting at
// month, into whatever `events` partition already covers that range
// (EnsurePartitions must be called first) — enough real rows per partition
// that ANALYZE gives the planner honest per-partition statistics, so the
// Append plan this test measures is the same shape a production-sized
// `events` table would produce, not the degenerate "Result / One-Time
// Filter: false" plan explain_test.go's own doc comment on
// TestExplainGuard_DataQualityAllowList warns an empty schema would give.
func seedMonthOfEvents(t *testing.T, st *postgres.Store, month time.Time, n int, sessionIDPrefix string) {
	t.Helper()
	ctx := context.Background()
	sessionID := sessionIDPrefix + "-" + month.Format("2006-01")
	events := make([]model.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, month.Add(time.Duration(i)*time.Minute), withModel("claude-prune")))
	}
	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)
}

// TestAggregateEventRollup_PrunesPartitions and
// TestFetchMetricRowsForRollup_PrunesPartitions are M9's EXPLAIN proof: six
// consecutive monthly `events`/`metric_samples` partitions, each with real
// rows, ANALYZEd, then EXPLAIN on a single target hour's claimed bucket.
// Before the fix (no range predicate, only the STABLE
// `date_trunc(...) = ANY(buckets)` equality), every partition is Append-
// scanned regardless of which single hour is claimed — this is the "fires
// every 60s tick" defect. After the fix, the added [from_ts, to_ts) range
// lets the planner eliminate every partition but the one the target hour
// actually falls in.
func TestAggregateEventRollup_PrunesPartitions(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.EnsurePartitions(ctx, start, end))

	const monthsCovered = 6
	const rowsPerMonth = 25
	for m := 0; m < monthsCovered; m++ {
		seedMonthOfEvents(t, st, start.AddDate(0, m, 0), rowsPerMonth, "session-prune-event")
	}
	_, err := pool.Exec(ctx, `ANALYZE events`)
	require.NoError(t, err)

	targetBucket := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	from, to := targetBucket, targetBucket.Add(time.Hour)

	plan := explainPlanText(t, pool, aggregateEventRollupSQL, []any{from, to, []time.Time{targetBucket}})
	require.True(t, prunedToSinglePartition(t, plan, monthlyPartitionNames("events", start, monthsCovered), []string{"events_2026_03"}),
		"AggregateEventRollup must prune partitions outside [from_ts, to_ts) — %d months of partitions exist but only one hour was claimed:\n%s",
		monthsCovered, plan)
}

func TestFetchMetricRowsForRollup_PrunesPartitions(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.EnsurePartitions(ctx, start, end))

	const monthsCovered = 6
	const rowsPerMonth = 25
	for m := 0; m < monthsCovered; m++ {
		month := start.AddDate(0, m, 0)
		samples := make([]model.MetricSample, 0, rowsPerMonth)
		for i := 0; i < rowsPerMonth; i++ {
			samples = append(samples, mkMetricSample(t, month.Add(time.Duration(i)*time.Minute), "claude_code.cost.usage", nil, float64(i), "delta", []byte(fmt.Sprintf("series-prune-%d-%d", m, i)), map[string]any{}))
		}
		_, err := st.WriteMetrics(ctx, samples)
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, `ANALYZE metric_samples`)
	require.NoError(t, err)

	targetBucket := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	from, to := targetBucket, targetBucket.Add(time.Hour)

	plan := explainPlanText(t, pool, fetchMetricRowsForRollupSQL, []any{from, to, []time.Time{targetBucket}})
	require.True(t, prunedToSinglePartition(t, plan, monthlyPartitionNames("metric_samples", start, monthsCovered), []string{"metric_samples_2026_03"}),
		"FetchMetricRowsForRollup must prune partitions outside [from_ts, to_ts) — %d months of partitions exist but only one hour was claimed:\n%s",
		monthsCovered, plan)
}

// TestEventsSince_IndexScanWithPartitionPruning is P5-01a's EXPLAIN AC (SPEC
// §5.2): EventsSince's real predicate — `ts >= $windowStart AND (ts, seq) >
// ($ts, $seq) ORDER BY ts, seq LIMIT $n` — must use an Index Scan on the
// (ts, seq) primary key and prune partitions, not Append-scan every
// retained `events` partition on every SSE reconnect (SPEC §5.2's own
// stated failure mode for the bare `seq > $n` alternative it rejects).
//
// The SQL here is copied from read_events.go's unexported eventsSinceSQL
// constant, trimmed to a minimal SELECT list (same convention
// TestGetEvent_IndexScanOnSinglePartition and
// TestListEvents_SessionTimeline_IndexScanWithPartitionPruning already use
// in read_events_test.go): this package cannot import an unexported
// identifier from the sibling postgres package, and which columns are
// selected does not change the planner's choice between an Index Scan and
// a Seq Scan — only WHERE/ORDER BY/LIMIT do.
//
// Four consecutive monthly partitions are seeded so pruning has something
// to prove: windowStart falls inside the third month (December). The `ts >=
// $windowStart` bound alone (no upper ts bound exists in this predicate) is
// what SPEC §5.2 calls pruning "to at most two partitions" — December
// (windowStart's own month) and January (nothing above bounds it) — while
// October and November, both strictly before windowStart, must be excluded
// entirely. This is a different case from GetEvent's own EXPLAIN AC
// (equality on both ts and seq, pruning to exactly one partition).
func TestEventsSince_IndexScanWithPartitionPruning(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("explain-eventssince")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	months := []time.Time{
		time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, st.EnsurePartitions(ctx, months[0], months[len(months)-1]))

	const perMonth = 4000
	seq := int64(30000)
	var seeds []eventSeed
	for _, month := range months {
		for i := 0; i < perMonth; i++ {
			seq++
			seeds = append(seeds, eventSeed{Seq: seq, SessionID: sessionID, TS: month.Add(time.Duration(i) * time.Second), Kind: "tool.pre"})
		}
	}
	seedFullEventsBulk(t, pool, seeds)
	_, err := pool.Exec(ctx, "ANALYZE events")
	require.NoError(t, err)

	windowStart := months[2].Add(30 * time.Second)
	afterTS, afterSeq := windowStart, int64(0)

	plan := explainPlanText(t, pool,
		"SELECT e.seq, e.id, e.ts FROM events e WHERE e.ts >= $1 AND (e.ts, e.seq) > ($2, $3) ORDER BY e.ts, e.seq LIMIT $4",
		[]any{windowStart, afterTS, afterSeq, int32(2000)})

	require.Contains(t, plan, "Index Scan", "EventsSince's (ts, seq) row-comparison predicate must use an Index Scan, not a Seq Scan")

	allPartitions := monthlyPartitionNames("events", months[0], len(months))
	wantPartitions := monthlyPartitionNames("events", months[2], 2) // December, January
	require.True(t, prunedToSinglePartition(t, plan, allPartitions, wantPartitions),
		"EventsSince must prune partitions outside the replay window — %d months of partitions exist but at most two (December, January) should be touched:\n%s", len(months), plan)
}

// monthlyPartitionNames returns the parent_YYYY_MM partition names
// EnsurePartitions creates for n consecutive months starting at start —
// matching partitions.go's ensureMonthlyPartition naming.
func monthlyPartitionNames(parent string, start time.Time, n int) []string {
	names := make([]string, n)
	for i := 0; i < n; i++ {
		m := start.AddDate(0, i, 0)
		names[i] = fmt.Sprintf("%s_%04d_%02d", parent, m.Year(), m.Month())
	}
	return names
}

func TestExplainGuard_DetectsEventsInPlan(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	// A synthetic events-touching statement. A real partition is not
	// strictly required for EXPLAIN to succeed against a partitioned parent,
	// but ensuring one exists keeps this a realistic plan rather than
	// depending on planner behaviour for a table with zero partitions.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.EnsurePartitions(ctx, base, base))

	plan := explainPlanText(t, pool, `SELECT count(*) FROM events WHERE ts >= $1 AND ts < $2`, []any{base, base.Add(time.Hour)})
	require.True(t, planMentionsEvents(plan), "the detector must flag a plan that genuinely scans events:\n%s", plan)
}
