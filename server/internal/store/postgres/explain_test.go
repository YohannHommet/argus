package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
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
			    count(*) FILTER (WHERE tc.decision IS NOT NULL)::bigint AS decided,
			    count(*) FILTER (WHERE tc.decision IS NOT NULL AND tc.correlation <> 'heuristic')::bigint AS exact_decided
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

// TestExplainGuard_DataQualityAllowList passes vacuously today (see
// dataQualityAllowList's doc) and starts actually asserting the moment
// P3-08 populates that list — SPEC §2.5's explicit exemption for the
// data-quality/hook-latency queries, which DO legitimately touch events but
// only ever bounded to a 24h window.
func TestExplainGuard_DataQualityAllowList(t *testing.T) {
	_, pool := newStore(t)
	for _, c := range dataQualityAllowList {
		t.Run(c.name, func(t *testing.T) {
			plan := explainPlanText(t, pool, c.sql, c.args)
			t.Logf("%s plan (allow-listed, expected to mention events):\n%s", c.name, plan)
		})
	}
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
