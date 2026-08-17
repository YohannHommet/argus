// read_analytics_test.go is a black-box (package postgres_test) integration
// suite for AnalyticsSummary/AnalyticsSeries/AnalyticsBreakdown/
// AnalyticsDecisions (P3-06). Rollup rows are seeded directly via
// seedRollupHourly/seedRollupDaily — these tests exercise read_analytics.go's
// aggregation/attributability/dense-bucket logic, not the rollup job itself
// (that is rollups_test.go's job), so bypassing WriteBatch+RunRollups gives
// exact, deterministic control over the rollup rows each AC needs. Decision-
// matrix and query_source tests reuse read_sessions_test.go's
// seedSession/seedToolCall/nextTestSessionID/testUUID helpers, extended
// locally where a column those helpers don't set (wait_ms, error_type) is
// needed.
package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/store"
)

// --- rollup seeding ---------------------------------------------------

// rollupSeed is one rollup_hourly/rollup_daily row (SPEC §2.4's column
// set); zero-value fields keep the table's own DEFAULT 0/”.
type rollupSeed struct {
	Bucket                                                                 time.Time
	Project, Vendor, Model, Source                                         string
	SessionsStarted, Turns, APIRequests, APIErrors, ToolCalls, ToolRejects int
	InputTokens, OutputTokens, CacheReadTokens, CacheCreationTokens        int64
	CostReportedUSD, CostEstimatedUSD                                      float64
	LocAdded, LocRemoved, ActiveSeconds                                    int64
}

func seedRollupHourly(t *testing.T, pool *pgxpool.Pool, seed rollupSeed) {
	t.Helper()
	seedRollupInto(t, pool, "rollup_hourly", seed)
}

func seedRollupDaily(t *testing.T, pool *pgxpool.Pool, seed rollupSeed) {
	t.Helper()
	seedRollupInto(t, pool, "rollup_daily", seed)
}

func seedRollupInto(t *testing.T, pool *pgxpool.Pool, table string, seed rollupSeed) {
	t.Helper()
	if seed.Source == "" {
		seed.Source = "event"
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO `+table+` (bucket, project, vendor, model, source,
			sessions_started, turns, api_requests, api_errors, tool_calls, tool_rejects,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			cost_reported_usd, cost_estimated_usd, loc_added, loc_removed, active_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		seed.Bucket, seed.Project, seed.Vendor, seed.Model, seed.Source,
		seed.SessionsStarted, seed.Turns, seed.APIRequests, seed.APIErrors, seed.ToolCalls, seed.ToolRejects,
		seed.InputTokens, seed.OutputTokens, seed.CacheReadTokens, seed.CacheCreationTokens,
		seed.CostReportedUSD, seed.CostEstimatedUSD, seed.LocAdded, seed.LocRemoved, seed.ActiveSeconds,
	)
	require.NoError(t, err)
}

// seedToolCallFull inserts a tool_calls row with the extra columns
// read_sessions_test.go's shared seedToolCall/toolCallSeed doesn't set
// (wait_ms, error_type) — needed by the decisions-matrix and
// error_type-breakdown tests.
type toolCallFullSeed struct {
	ID                       string
	SessionID                string
	ToolName                 string
	Decision, DecisionSource *string
	Correlation              string
	StartedAt                time.Time
	WaitMS                   *int
	ErrorType                *string
}

func seedToolCallFull(t *testing.T, pool *pgxpool.Pool, seed toolCallFullSeed) {
	t.Helper()
	if seed.Correlation == "" {
		seed.Correlation = "exact"
	}
	if seed.StartedAt.IsZero() {
		seed.StartedAt = time.Now().UTC()
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tool_calls (id, session_id, tool_name, decision, decision_source, correlation, started_at, wait_ms, error_type, event_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1)`,
		seed.ID, seed.SessionID, seed.ToolName, seed.Decision, seed.DecisionSource, seed.Correlation, seed.StartedAt, seed.WaitMS, seed.ErrorType,
	)
	require.NoError(t, err)
}

func strPtr(s string) *string { return &s }

// --- AC: summary cost equals the rollup sum over the window --------------

func TestAnalyticsSummary_CostEqualsRollupSum(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "argus", Vendor: "claude_code", Model: "claude-a", CostReportedUSD: 1.5, CostEstimatedUSD: 0.5, APIRequests: 3})
	seedRollupHourly(t, pool, rollupSeed{Bucket: base.Add(time.Hour), Project: "argus", Vendor: "claude_code", Model: "claude-b", CostReportedUSD: 2.0, APIRequests: 2})
	// Outside the window: must not contribute.
	seedRollupHourly(t, pool, rollupSeed{Bucket: base.Add(-time.Hour), Project: "argus", Vendor: "claude_code", Model: "claude-a", CostReportedUSD: 100})

	from := base
	to := base.Add(2 * time.Hour)
	got, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to})
	require.NoError(t, err)

	require.InDelta(t, 4.0, got.Cost.USD, 1e-9)
	require.InDelta(t, 3.5, got.Cost.ReportedUSD, 1e-9)
	require.InDelta(t, 0.5, got.Cost.EstimatedUSD, 1e-9)
	require.Equal(t, int64(5), got.APIRequests)
}

// --- AC: estimated_share non-zero for estimated-cost data, zero otherwise -

func TestAnalyticsSummary_EstimatedShare(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "estimated-proj", CostReportedUSD: 0, CostEstimatedUSD: 2.0})
	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "reported-proj", CostReportedUSD: 5.0, CostEstimatedUSD: 0})

	from := base
	to := base.Add(time.Hour)

	estimated, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to, Project: []string{"estimated-proj"}})
	require.NoError(t, err)
	require.InDelta(t, 1.0, estimated.Cost.EstimatedShare, 1e-9)

	reported, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to, Project: []string{"reported-proj"}})
	require.NoError(t, err)
	require.InDelta(t, 0.0, reported.Cost.EstimatedShare, 1e-9)
}

// --- AC: ?model=X returns null for non-attributable counters, listed in
// not_attributable[]; attributable counters (api_requests/cost) still
// compute. --------------------------------------------------------------

func TestAnalyticsSummary_ModelFilter_NonAttributableCountersAreNil(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "p", Model: "claude-x", APIRequests: 10, CostReportedUSD: 1.0})
	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "p", Model: "", SessionsStarted: 4, Turns: 9, ToolCalls: 20, ToolRejects: 2, LocAdded: 100, LocRemoved: 10, ActiveSeconds: 3600})

	from := base
	to := base.Add(time.Hour)
	got, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to, Model: []string{"claude-x"}})
	require.NoError(t, err)

	require.Nil(t, got.Sessions)
	require.Nil(t, got.Turns)
	require.Nil(t, got.ToolCalls)
	require.Nil(t, got.ToolRejects)
	require.Nil(t, got.RejectRate)
	require.Nil(t, got.LOC)
	require.Nil(t, got.ActiveSeconds)
	require.ElementsMatch(t, []string{"sessions", "turns", "tool_calls", "tool_rejects", "reject_rate", "loc", "active_seconds"}, got.NotAttributable)

	// Attributable counters still compute, filtered to the requested model.
	require.Equal(t, int64(10), got.APIRequests)
	require.InDelta(t, 1.0, got.Cost.USD, 1e-9)

	// Without a model filter, the same window reports real numbers, not null.
	unfiltered, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to})
	require.NoError(t, err)
	require.NotNil(t, unfiltered.Sessions)
	require.Equal(t, int64(4), *unfiltered.Sessions)
	require.Empty(t, unfiltered.NotAttributable)
}

// --- AC: metrics_only_projects detection -----------------------------

func TestAnalyticsSummary_MetricsOnlyProjects(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "metrics-only", Source: "metric", APIRequests: 1})
	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "both", Source: "metric", APIRequests: 1})
	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "both", Source: "event", APIRequests: 1})

	from := base
	to := base.Add(time.Hour)
	got, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to})
	require.NoError(t, err)
	require.Equal(t, []string{"metrics-only"}, got.MetricsOnlyProjects)
}

// --- AC: empty window returns zeros, not an error ----------------------

func TestAnalyticsSummary_EmptyWindowReturnsZeros(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	got, err := st.AnalyticsSummary(ctx, store.AnalyticsFilter{From: &from, To: &to})
	require.NoError(t, err)
	require.Equal(t, int64(0), got.APIRequests)
	require.Equal(t, int64(0), got.APIErrors)
	require.InDelta(t, 0, got.Cost.USD, 1e-9)
	require.NotNil(t, got.Sessions)
	require.Equal(t, int64(0), *got.Sessions)
	require.Empty(t, got.MetricsOnlyProjects)
	require.Empty(t, got.NotAttributable)
}

// --- AC: series buckets are contiguous with zeros for empty hours; length
// matches the window. ----------------------------------------------------

func TestAnalyticsSeries_DenseZeroFilledBuckets(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)

	// Only the first and third hour have data; the second is a gap.
	seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "p", APIRequests: 3})
	seedRollupHourly(t, pool, rollupSeed{Bucket: base.Add(2 * time.Hour), Project: "p", APIRequests: 5})

	from := base
	to := base.Add(3 * time.Hour) // 3 hour buckets: base, +1h (empty), +2h
	got, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &from, To: &to}, store.Grouping{Metric: store.MetricAPIRequests})
	require.NoError(t, err)

	require.Equal(t, "hour", got.Bucket)
	require.Len(t, got.Buckets, 3)
	require.True(t, got.Buckets[0].Equal(base))
	require.True(t, got.Buckets[1].Equal(base.Add(time.Hour)))
	require.True(t, got.Buckets[2].Equal(base.Add(2*time.Hour)))

	require.Len(t, got.Series, 1)
	require.Equal(t, []float64{3, 0, 5}, got.Series[0].Values)
	require.Nil(t, got.Other)
}

// --- AC: group_by=model with 12 models and limit_series=8 yields 8 series
// plus an other closing the gap exactly. ---------------------------------

func TestAnalyticsSeries_GroupByModel_LimitSeriesFoldsOtherExactly(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)

	const nModels = 12
	for i := 0; i < nModels; i++ {
		model := modelName(i)
		// Distinct, descending totals so ranking is deterministic:
		// model 0 has the highest cost, model 11 the lowest.
		cost := float64(nModels - i)
		seedRollupHourly(t, pool, rollupSeed{Bucket: base, Project: "p", Model: model, CostReportedUSD: cost})
		seedRollupHourly(t, pool, rollupSeed{Bucket: base.Add(time.Hour), Project: "p", Model: model, CostReportedUSD: cost * 2})
	}

	from := base
	to := base.Add(2 * time.Hour)
	got, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &from, To: &to},
		store.Grouping{Metric: store.MetricCost, GroupBy: store.GroupByModel, LimitSeries: 8})
	require.NoError(t, err)

	require.Len(t, got.Series, 8)
	require.NotNil(t, got.Other)

	// The 8 kept series must be the top-8 by total cost desc: models 0..7.
	kept := map[string]bool{}
	for _, sp := range got.Series {
		kept[sp.Key] = true
	}
	for i := 0; i < 8; i++ {
		require.True(t, kept[modelName(i)], "expected top model %s to be kept", modelName(i))
	}

	// sum(series) + other == sum(all rows) per bucket, exactly.
	for bi := range got.Buckets {
		var total float64
		for _, sp := range got.Series {
			total += sp.Values[bi]
		}
		total += got.Other.Values[bi]

		var expected float64
		for i := 0; i < nModels; i++ {
			cost := float64(nModels - i)
			if bi == 0 {
				expected += cost
			} else {
				expected += cost * 2
			}
		}
		require.InDelta(t, expected, total, 1e-9, "bucket %d: series+other must equal the true total", bi)
	}
}

func modelName(i int) string {
	return fmt.Sprintf("model-%d", i)
}

// --- AC: ?model= under timeseries?metric=sessions (non-attributable) is a
// distinct sentinel error, not a silently empty series. ------------------

func TestAnalyticsSeries_NonAttributableMetricUnderModelFilter_ReturnsErrNotAttributable(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	from, to := base, base.Add(time.Hour)

	_, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &from, To: &to, Model: []string{"claude-x"}},
		store.Grouping{Metric: store.MetricSessions})
	require.ErrorIs(t, err, store.ErrNotAttributable)

	// An attributable metric under the same model filter must NOT error.
	_, err = st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &from, To: &to, Model: []string{"claude-x"}},
		store.Grouping{Metric: store.MetricCost})
	require.NoError(t, err)
}

// --- bucket auto-selection: hour for <=7d windows, day beyond -----------

func TestAnalyticsSeries_BucketAutoSelection(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	shortFrom, shortTo := base, base.Add(24*time.Hour)
	shortSeries, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &shortFrom, To: &shortTo}, store.Grouping{Metric: store.MetricCost})
	require.NoError(t, err)
	require.Equal(t, "hour", shortSeries.Bucket)

	longFrom, longTo := base, base.Add(10*24*time.Hour)
	longSeries, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &longFrom, To: &longTo}, store.Grouping{Metric: store.MetricCost})
	require.NoError(t, err)
	require.Equal(t, "day", longSeries.Bucket)
	require.Len(t, longSeries.Buckets, 10)
}

func TestAnalyticsSeries_DailyBucket_DenseZeroFilled(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seedRollupDaily(t, pool, rollupSeed{Bucket: base, Project: "p", APIRequests: 7})
	seedRollupDaily(t, pool, rollupSeed{Bucket: base.AddDate(0, 0, 2), Project: "p", APIRequests: 9})

	from, to := base, base.AddDate(0, 0, 3)
	got, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &from, To: &to}, store.Grouping{Metric: store.MetricAPIRequests, Bucket: store.BucketDay})
	require.NoError(t, err)
	require.Equal(t, "day", got.Bucket)
	require.Len(t, got.Buckets, 3)
	require.Len(t, got.Series, 1)
	require.Equal(t, []float64{7, 0, 9}, got.Series[0].Values)
}

// --- AC: breakdown?dimension=query_source returns raw keys, sourced from
// sessions.cost_by_query_source (not rollup_hourly, which has no such
// column, SPEC §2.4). ----------------------------------------------------

func TestAnalyticsBreakdown_QuerySource_RawKeysFromSessions(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedSession(t, pool, sessionSeed{
		ID: nextTestSessionID("qs-a"), Project: "argus", LastEventAt: now,
		CostByQuerySource: map[string]float64{"": 3.9, "sdk": 0.35, "generate_session_title": 0.02},
	})
	seedSession(t, pool, sessionSeed{
		ID: nextTestSessionID("qs-b"), Project: "argus", LastEventAt: now,
		CostByQuerySource: map[string]float64{"sdk": 0.10},
	})

	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	got, err := st.AnalyticsBreakdown(ctx, store.AnalyticsFilter{From: &from, To: &to}, store.Dimension{Name: store.DimensionQuerySource})
	require.NoError(t, err)
	require.Equal(t, "query_source", got.Dimension)

	byKey := map[string]float64{}
	for _, r := range got.Rows {
		byKey[r.Key] = r.Value
	}
	require.InDelta(t, 3.9, byKey[""], 1e-9)
	require.InDelta(t, 0.45, byKey["sdk"], 1e-9)
	require.InDelta(t, 0.02, byKey["generate_session_title"], 1e-9)
}

// --- decisions matrix: 6 documented sources + an unseen one, each its own
// key; exact_share = 1.0 for OTel-derived (non-heuristic) decisions. -----

func TestAnalyticsDecisions_AllSourcesIncludingUnseenOne_ExactShareForNonHeuristic(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dec")
	seedSession(t, pool, sessionSeed{ID: sessionID, Project: "argus"})

	base := time.Now().UTC().Add(-time.Hour)
	sources := []string{"config", "hook", "user_permanent", "user_temporary", "user_reject", "user_abort", "some_future_source"}
	for i, src := range sources {
		decision := "accept"
		if src == "user_reject" || src == "user_abort" {
			decision = "reject"
		}
		seedToolCallFull(t, pool, toolCallFullSeed{
			ID: testUUID(20000 + i), SessionID: sessionID, ToolName: "Edit",
			Decision: strPtr(decision), DecisionSource: strPtr(src), Correlation: "exact",
			StartedAt: base.Add(time.Duration(i) * time.Second), WaitMS: intPtr(100 * (i + 1)),
		})
	}

	from := base.Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	got, err := st.AnalyticsDecisions(ctx, store.AnalyticsFilter{From: &from, To: &to, Project: []string{"argus"}})
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)

	row := got.Rows[0]
	require.Equal(t, "Edit", row.ToolName)
	require.Equal(t, int64(5), row.Accept)
	require.Equal(t, int64(2), row.Reject)
	require.InDelta(t, 1.0, row.ExactShare, 1e-9)
	for _, src := range sources {
		require.Containsf(t, row.BySource, src, "decision_source %q must appear as its own key", src)
		require.Equal(t, int64(1), row.BySource[src])
	}
	require.NotNil(t, row.P50WaitMS)
	require.NotNil(t, row.P95WaitMS)
}

func TestAnalyticsDecisions_HeuristicLowersExactShare(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dec-heur")
	seedSession(t, pool, sessionSeed{ID: sessionID, Project: "argus2"})

	base := time.Now().UTC().Add(-time.Hour)
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(21001), SessionID: sessionID, ToolName: "Bash", Decision: strPtr("accept"), DecisionSource: strPtr("config"), Correlation: "exact", StartedAt: base})
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(21002), SessionID: sessionID, ToolName: "Bash", Decision: strPtr("accept"), DecisionSource: strPtr("config"), Correlation: "heuristic", StartedAt: base.Add(time.Second)})

	from := base.Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	got, err := st.AnalyticsDecisions(ctx, store.AnalyticsFilter{From: &from, To: &to, Project: []string{"argus2"}})
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)
	require.InDelta(t, 0.5, got.Rows[0].ExactShare, 1e-9)
}

// --- breakdown by tool/decision_source/error_type (tool_calls-backed) ---

func TestAnalyticsBreakdown_ErrorType_FromToolCalls(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("errtype")
	seedSession(t, pool, sessionSeed{ID: sessionID, Project: "argus3"})

	base := time.Now().UTC().Add(-time.Hour)
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22001), SessionID: sessionID, ToolName: "Bash", ErrorType: strPtr("timeout"), StartedAt: base})
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22002), SessionID: sessionID, ToolName: "Bash", ErrorType: strPtr("timeout"), StartedAt: base.Add(time.Second)})
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22003), SessionID: sessionID, ToolName: "Edit", ErrorType: strPtr("permission_denied"), StartedAt: base.Add(2 * time.Second)})
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22004), SessionID: sessionID, ToolName: "Edit", StartedAt: base.Add(3 * time.Second)}) // no error

	from := base.Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	got, err := st.AnalyticsBreakdown(ctx, store.AnalyticsFilter{From: &from, To: &to, Project: []string{"argus3"}}, store.Dimension{Name: store.DimensionErrorType})
	require.NoError(t, err)

	byKey := map[string]float64{}
	for _, r := range got.Rows {
		byKey[r.Key] = r.Value
	}
	require.InDelta(t, 2.0, byKey["timeout"], 1e-9)
	require.InDelta(t, 1.0, byKey["permission_denied"], 1e-9)
	require.NotContains(t, byKey, "") // the no-error row is excluded, not folded into ""
}

// TestAnalyticsBreakdown_ModelFilter_NotAttributableDimensions is the
// regression test for a silent-wrong-answer defect found in review: the
// tool_calls- and sessions-sourced dimensions have no model column, and the
// first implementation simply did not pass the ?model= filter down to them —
// so a per-model question came back with fleet-wide totals that looked
// filtered. SPEC §4.3 applies the same not-attributable rule to /breakdown as
// to /timeseries, so those combinations must refuse rather than mislead.
func TestAnalyticsBreakdown_ModelFilter_NotAttributableDimensions(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("breakdown-attr")
	seedSession(t, pool, sessionSeed{ID: sessionID, Project: "argus-attr"})

	base := time.Now().UTC().Add(-time.Hour)
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(23001), SessionID: sessionID, ToolName: "Edit", StartedAt: base})

	from := base.Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	filter := store.AnalyticsFilter{From: &from, To: &to, Model: []string{"claude-sonnet-4-5"}}

	for _, dim := range []store.AnalyticsDimension{
		store.DimensionTool,
		store.DimensionDecisionSource,
		store.DimensionErrorType,
		store.DimensionQuerySource,
	} {
		_, err := st.AnalyticsBreakdown(ctx, filter, store.Dimension{Name: dim})
		require.ErrorIsf(t, err, store.ErrNotAttributable,
			"dimension=%s cannot honour a model filter and must say so, not answer with unfiltered totals", dim)
	}

	// `calls` is not model-attributable even on a rollup-sourced dimension:
	// rollup_hourly records tool_calls in the model='' group by construction,
	// so a model-filtered call count could only ever be zero.
	_, err := st.AnalyticsBreakdown(ctx, filter,
		store.Dimension{Name: store.DimensionProject, Metric: store.BreakdownMetricCalls})
	require.ErrorIs(t, err, store.ErrNotAttributable)

	// Cost and tokens on the rollup-sourced dimensions ARE attributable and
	// must still work under the same filter.
	for _, metric := range []store.BreakdownMetric{store.BreakdownMetricCost, store.BreakdownMetricTokens} {
		_, breakdownErr := st.AnalyticsBreakdown(ctx, filter,
			store.Dimension{Name: store.DimensionModel, Metric: metric})
		require.NoErrorf(t, breakdownErr, "dimension=model metric=%s is model-attributable and must be served", metric)
	}
}

// --- AC: an empty window's series is a full run of zeros, not an error --

func TestAnalyticsSeries_EmptyWindowReturnsZeros(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	from := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Hour)

	got, err := st.AnalyticsSeries(ctx, store.AnalyticsFilter{From: &from, To: &to}, store.Grouping{Metric: store.MetricCost})
	require.NoError(t, err)
	require.Len(t, got.Buckets, 3)
	require.Len(t, got.Series, 1)
	require.Equal(t, []float64{0, 0, 0}, got.Series[0].Values)
	require.Nil(t, got.Other)
}

// --- m10 (pre-Phase-4 audit wave, ticket W3): DecisionCounts's decided/
// exact_decided must agree with SessionDecisionTotals's
// decision IN ('accept','reject') convention, not IS NOT NULL -------------

// TestAnalyticsDecisions_ThirdDecisionValueDoesNotInflateDecided reproduces
// m10: `decision` is unconstrained vendor vocabulary (SPEC §0), so a third
// value alongside accept/reject (here "defer", standing in for any future
// or vendor-specific value this codebase has never seen) must be excluded
// from `decided` and `exact_share`'s denominator exactly like
// SessionDecisionTotals (read_sessions.sql:84-85) already excludes it —
// before the fix, DecisionCounts's `decision IS NOT NULL` filter counted it
// anyway, so `accept + reject != decided` and `/analytics/decisions`
// disagreed with `/sessions/{id}` for the same underlying data.
func TestAnalyticsDecisions_ThirdDecisionValueDoesNotInflateDecided(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	sessionID := nextTestSessionID("dec-third-value")
	seedSession(t, pool, sessionSeed{ID: sessionID, Project: "argus-third-value"})

	base := time.Now().UTC().Add(-time.Hour)
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22001), SessionID: sessionID, ToolName: "Edit", Decision: strPtr("accept"), DecisionSource: strPtr("config"), Correlation: "exact", StartedAt: base})
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22002), SessionID: sessionID, ToolName: "Edit", Decision: strPtr("reject"), DecisionSource: strPtr("config"), Correlation: "exact", StartedAt: base.Add(time.Second)})
	// A third, unconstrained decision value (SPEC §0), deliberately given
	// correlation="heuristic" so the buggy `IS NOT NULL` predicate and the
	// fixed `IN ('accept','reject')` predicate disagree on exact_share, not
	// just on the internal decided count no Go type exposes directly: the
	// buggy version inflates `decided` (denominator) with this row while
	// leaving `exact_decided` (numerator) untouched (it's heuristic in
	// both), so exact_share drops to 2/3 pre-fix instead of the correct
	// 2/2 = 1.0.
	seedToolCallFull(t, pool, toolCallFullSeed{ID: testUUID(22003), SessionID: sessionID, ToolName: "Edit", Decision: strPtr("defer"), DecisionSource: strPtr("config"), Correlation: "heuristic", StartedAt: base.Add(2 * time.Second)})

	from := base.Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	got, err := st.AnalyticsDecisions(ctx, store.AnalyticsFilter{From: &from, To: &to, Project: []string{"argus-third-value"}})
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)

	row := got.Rows[0]
	require.Equal(t, "Edit", row.ToolName)
	require.Equal(t, int64(1), row.Accept)
	require.Equal(t, int64(1), row.Reject)
	require.InDelta(t, 1.0, row.ExactShare, 1e-9, "the third decision value must not appear in decided's denominator, matching SessionDecisionTotals's decision IN ('accept','reject') convention")
}
