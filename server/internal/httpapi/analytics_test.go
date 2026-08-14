// analytics_test.go is httpapi's black-box (package httpapi_test) test
// suite for the four analytics handlers, GET /api/v1/facets, GET
// /api/v1/meta's P3-08 extension, and the two GET /api/v1/quality/*
// handlers (P3-08), following events_test.go's convention: exercise
// httpapi.New's real router end to end via httptest, using fakeReader (this
// package's shared P3-07/P3-08 test double, sessions_test.go) rather than a
// concrete store.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

func newAnalyticsRouter(reader *fakeReader) http.Handler {
	return httpapi.New(httpapi.Deps{Analytics: reader})
}

func getJSON(t *testing.T, r http.Handler, path string, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	r.ServeHTTP(rec, req)
	if out != nil && rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), out))
	}
	return rec
}

// --- AC: invalid metric=/bucket=/dimension= -> 400 listing allowed values --

func TestGetAnalyticsTimeseries_InvalidMetric_400ListsAllowedValues(t *testing.T) {
	t.Parallel()
	r := newAnalyticsRouter(&fakeReader{})

	rec := getJSON(t, r, "/api/v1/analytics/timeseries?metric=bogus", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	body := rec.Body.String()
	for _, want := range []string{"cost", "tokens", "sessions", "turns", "api_requests", "api_errors", "tool_calls", "tool_rejects", "loc"} {
		require.Contains(t, body, want, "the 400 detail must list every allowed metric value")
	}
}

func TestGetAnalyticsTimeseries_InvalidBucket_400ListsAllowedValues(t *testing.T) {
	t.Parallel()
	r := newAnalyticsRouter(&fakeReader{})

	rec := getJSON(t, r, "/api/v1/analytics/timeseries?metric=cost&bucket=fortnight", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	require.Contains(t, rec.Body.String(), "hour")
	require.Contains(t, rec.Body.String(), "day")
}

func TestGetAnalyticsBreakdown_InvalidDimension_400ListsAllowedValues(t *testing.T) {
	t.Parallel()
	r := newAnalyticsRouter(&fakeReader{})

	rec := getJSON(t, r, "/api/v1/analytics/breakdown?dimension=bogus", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"type":"urn:argus:error:invalid-parameter"`)
	for _, want := range []string{"model", "project", "tool", "decision_source", "query_source", "error_type"} {
		require.Contains(t, body, want, "the 400 detail must list every allowed dimension value")
	}
}

func TestGetAnalyticsBreakdown_InvalidMetric_400ListsAllowedValues(t *testing.T) {
	t.Parallel()
	r := newAnalyticsRouter(&fakeReader{})

	rec := getJSON(t, r, "/api/v1/analytics/breakdown?dimension=tool&metric=bogus", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"type":"urn:argus:error:invalid-parameter"`)
	for _, want := range []string{"cost", "calls", "tokens"} {
		require.Contains(t, body, want)
	}
}

// --- AC: source=metric returns metric-sourced rows, never mixed with event -

func TestGetAnalyticsSummary_SourceMetric_NeverMixedWithEvent(t *testing.T) {
	t.Parallel()

	var gotSource store.AnalyticsSource
	reader := &fakeReader{
		analyticsSummary: func(_ context.Context, f store.AnalyticsFilter) (model.Summary, error) {
			gotSource = f.Source
			return model.Summary{Source: model.Source(f.Source), MetricsOnlyProjects: []string{}, NotAttributable: []string{}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var got model.Summary
	rec := getJSON(t, r, "/api/v1/analytics/summary?source=metric", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, store.AnalyticsSourceMetric, gotSource, "the ?source=metric filter must reach the store unchanged")
	require.Equal(t, model.Source("metric"), got.Source, "the response must echo the metric source, never fall back to event")
}

func TestGetAnalyticsSummary_SourceEvent_IsTheDefault(t *testing.T) {
	t.Parallel()

	var gotSource store.AnalyticsSource
	reader := &fakeReader{
		analyticsSummary: func(_ context.Context, f store.AnalyticsFilter) (model.Summary, error) {
			gotSource = f.Source
			return model.Summary{Source: model.Source("event"), MetricsOnlyProjects: []string{}, NotAttributable: []string{}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var got model.Summary
	rec := getJSON(t, r, "/api/v1/analytics/summary", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, store.AnalyticsSource(""), gotSource, "absent ?source= must reach the store as the zero value, which store.sourceKindOf treats as event")
	require.Equal(t, model.Source("event"), got.Source)
}

// --- AC: store.ErrNotAttributable maps to 400 urn:argus:error:not-attributable

func TestGetAnalyticsTimeseries_NotAttributable_400(t *testing.T) {
	t.Parallel()
	reader := &fakeReader{
		analyticsSeries: func(context.Context, store.AnalyticsFilter, store.Grouping) (model.Series, error) {
			return model.Series{}, store.ErrNotAttributable
		},
	}
	r := newAnalyticsRouter(reader)

	rec := getJSON(t, r, "/api/v1/analytics/timeseries?metric=sessions&model=claude-opus-5", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-attributable"`)
	require.Contains(t, rec.Body.String(), "metric=sessions")
}

func TestGetAnalyticsBreakdown_NotAttributable_400(t *testing.T) {
	t.Parallel()
	reader := &fakeReader{
		analyticsBreakdown: func(context.Context, store.AnalyticsFilter, store.Dimension) (model.Breakdown, error) {
			return model.Breakdown{}, store.ErrNotAttributable
		},
	}
	r := newAnalyticsRouter(reader)

	rec := getJSON(t, r, "/api/v1/analytics/breakdown?dimension=tool&model=claude-opus-5", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-attributable"`)
}

// TestGetAnalyticsSummary_ModelFiltered_NullCountersPassThrough is the
// ticket note's AC: AnalyticsSummary does NOT error under a model filter —
// httpapi must pass the null counters and not_attributable[] through
// faithfully, a `null` counter serialising as JSON null, never 0 or
// omitted.
func TestGetAnalyticsSummary_ModelFiltered_NullCountersPassThrough(t *testing.T) {
	t.Parallel()
	reader := &fakeReader{
		analyticsSummary: func(_ context.Context, _ store.AnalyticsFilter) (model.Summary, error) {
			return model.Summary{
				Source:              model.Source("event"),
				MetricsOnlyProjects: []string{},
				NotAttributable:     []string{"sessions", "turns", "tool_calls", "tool_rejects", "reject_rate", "loc", "active_seconds"},
			}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	rec := getJSON(t, r, "/api/v1/analytics/summary?model=claude-opus-5", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.JSONEq(t, "null", string(raw["sessions"]), "a non-attributable counter must serialise as JSON null")
	require.JSONEq(t, "null", string(raw["turns"]))
	require.JSONEq(t, "null", string(raw["loc"]))

	var got model.Summary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.ElementsMatch(t, []string{"sessions", "turns", "tool_calls", "tool_rejects", "reject_rate", "loc", "active_seconds"}, got.NotAttributable)
}

// --- AC: /facets served from cache on the second call ----------------------

func TestGetFacets_ServedFromCacheOnSecondCall(t *testing.T) {
	t.Parallel()

	var calls int32
	reader := &fakeReader{
		facets: func(context.Context) (model.Facets, error) {
			atomic.AddInt32(&calls, 1)
			return model.Facets{Projects: []string{"argus"}, Models: []string{}, Vendors: []string{}, Tools: []string{}, DecisionSources: []string{}, QuerySources: []string{}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var first, second model.Facets
	rec1 := getJSON(t, r, "/api/v1/facets", &first)
	rec2 := getJSON(t, r, "/api/v1/facets", &second)

	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "the second call within the cache window must not hit the store again")
	require.Equal(t, first, second)
}

// TestGetFacets_StoreErrorNotCached is the ticket note's AC: an error must
// not be cached as a success — the very next call must retry the store, not
// serve a stuck zero value.
func TestGetFacets_StoreErrorNotCached(t *testing.T) {
	t.Parallel()

	var calls int32
	reader := &fakeReader{
		facets: func(context.Context) (model.Facets, error) {
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				return model.Facets{}, context.DeadlineExceeded
			}
			return model.Facets{Projects: []string{"argus"}, Models: []string{}, Vendors: []string{}, Tools: []string{}, DecisionSources: []string{}, QuerySources: []string{}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	rec1 := getJSON(t, r, "/api/v1/facets", nil)
	require.Equal(t, http.StatusInternalServerError, rec1.Code)

	var second model.Facets
	rec2 := getJSON(t, r, "/api/v1/facets", &second)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls), "a failed refresh must not be cached — the next call must retry the store")
	require.Equal(t, []string{"argus"}, second.Projects)
}

// --- AC: /meta reports hooks_seen=false / tool_details_seen=false ----------

func TestGetMeta_ExtendedFields_ReportsHonestFalseFlags(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		facets: func(context.Context) (model.Facets, error) {
			return model.Facets{Projects: []string{}, Models: []string{}, Vendors: []string{"claude_code"}, Tools: []string{}, DecisionSources: []string{}, QuerySources: []string{}}, nil
		},
		dataQuality: func(context.Context) (model.DataQuality, error) {
			// A database that received only OTLP: logs/metrics seen, hooks
			// and tool_parameters detail never seen.
			return model.DataQuality{LogsExporterSeen: true, MetricsExporterSeen: false, HooksSeen: false, ToolDetailsSeen: false}, nil
		},
		analyticsSummary: func(context.Context, store.AnalyticsFilter) (model.Summary, error) {
			return model.Summary{Source: model.Source("event"), MetricsOnlyProjects: []string{}, NotAttributable: []string{}}, nil
		},
	}
	r := httpapi.New(httpapi.Deps{Analytics: reader})

	var got map[string]any
	rec := getJSON(t, r, "/api/v1/meta", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, false, got["hooks_seen"])
	require.Equal(t, false, got["tool_details_seen"])
	require.Equal(t, true, got["logs_exporter_seen"])
	require.Equal(t, []any{"claude_code"}, got["vendors"])

	dq, ok := got["data_quality"].(map[string]any)
	require.True(t, ok, "data_quality must be present as an object")
	require.Equal(t, false, dq["hooks_seen"])
	require.Equal(t, false, dq["tool_details_seen"])
}

func TestGetMeta_NilAnalytics_StillServesBaseFields(t *testing.T) {
	t.Parallel()
	r := httpapi.New(httpapi.Deps{})

	rec := getJSON(t, r, "/api/v1/meta", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Contains(t, got, "version")
	require.False(t, got["hooks_seen"].(bool))
}

// --- AC: /quality/unknown-kinds groups by event_name, bounded to window ----

func TestGetQualityUnknownKinds_PassesSinceThrough(t *testing.T) {
	t.Parallel()

	var gotSince time.Time
	reader := &fakeReader{
		unknownKinds: func(_ context.Context, since time.Time, _ int) ([]model.UnknownKindGroup, error) {
			gotSince = since
			return []model.UnknownKindGroup{
				{EventName: "some_new_event", Source: model.SourceOTelLog, Count: 41, FirstSeen: since, LastSeen: since, Sample: map[string]any{"raw.attr": "value"}},
			}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var got struct {
		Rows []model.UnknownKindGroup `json:"rows"`
	}
	rec := getJSON(t, r, "/api/v1/quality/unknown-kinds?since=-1h", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.WithinDuration(t, time.Now().Add(-time.Hour), gotSince, 5*time.Second)
	require.Len(t, got.Rows, 1)
	require.Equal(t, "some_new_event", got.Rows[0].EventName)
	require.Equal(t, "value", got.Rows[0].Sample["raw.attr"])
}

func TestGetQualityUnknownKinds_DefaultSinceIsMinus24h(t *testing.T) {
	t.Parallel()

	var gotSince time.Time
	reader := &fakeReader{
		unknownKinds: func(_ context.Context, since time.Time, _ int) ([]model.UnknownKindGroup, error) {
			gotSince = since
			return nil, nil
		},
	}
	r := newAnalyticsRouter(reader)

	rec := getJSON(t, r, "/api/v1/quality/unknown-kinds", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.WithinDuration(t, time.Now().Add(-24*time.Hour), gotSince, 5*time.Second)
}

// --- AC: /quality/hook-latency returns percentiles per hook_event ----------

func TestGetQualityHookLatency_ReturnsRowsPerHookEvent(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		hookLatency: func(context.Context, store.AnalyticsFilter) (model.HookLatency, error) {
			return model.HookLatency{Rows: []model.HookLatencyRow{
				{HookEvent: "PostToolUse", Executions: 412, P50MS: 9, P95MS: 41, P99MS: 120, Errors: 0, Cancelled: 0},
			}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var got model.HookLatency
	rec := getJSON(t, r, "/api/v1/quality/hook-latency", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, got.Rows, 1)
	require.Equal(t, "PostToolUse", got.Rows[0].HookEvent)
	require.Equal(t, int64(9), got.Rows[0].P50MS)
}

// --- AC: decisions endpoint (from/to/project only) --------------------------

func TestGetAnalyticsDecisions_ReturnsMatrix(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		analyticsDecisions: func(context.Context, store.AnalyticsFilter) (model.DecisionMatrix, error) {
			return model.DecisionMatrix{Rows: []model.DecisionMatrixRow{
				{ToolName: "Edit", Accept: 300, Reject: 41, BySource: map[string]int64{"config": 210}, ExactShare: 1.0},
			}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var got model.DecisionMatrix
	rec := getJSON(t, r, "/api/v1/analytics/decisions", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, got.Rows, 1)
	require.Equal(t, "Edit", got.Rows[0].ToolName)
}

// --- AC: breakdown happy path -----------------------------------------------

func TestGetAnalyticsBreakdown_ReturnsRows(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		analyticsBreakdown: func(_ context.Context, _ store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error) {
			require.Equal(t, store.DimensionTool, d.Name)
			return model.Breakdown{Dimension: string(d.Name), Rows: []model.BreakdownRow{{Key: "Edit", Value: 812, Share: 0.37}}}, nil
		},
	}
	r := newAnalyticsRouter(reader)

	var got model.Breakdown
	rec := getJSON(t, r, "/api/v1/analytics/breakdown?dimension=tool", &got)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "tool", got.Dimension)
	require.Len(t, got.Rows, 1)
}
