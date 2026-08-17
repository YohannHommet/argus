package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
)

// defaultTimeseriesLimitSeries / defaultBreakdownLimit mirror openapi.yaml's
// documented defaults for `limit_series` (getAnalyticsTimeseries) and
// `limit` (getAnalyticsBreakdown) — httpapi only needs them to distinguish
// "absent" from "explicit 0" when parsing; read_analytics.go's own defaults
// (store package) apply identically when 0 reaches the store layer, so
// these constants exist for doc/error-message purposes only, not because
// httpapi enforces them itself.
const (
	defaultTimeseriesLimitSeries = 8
	defaultBreakdownLimit        = 20
	maxBreakdownLimit            = 500
)

// validTimeseriesMetrics is SPEC §4.3's closed TimeseriesMetric vocabulary
// (openapi.yaml's TimeseriesMetric schema) in its documented order — Argus's
// own query surface, so (unlike vendor vocabulary elsewhere in this
// package) an unrecognized value is a 400 that enumerates the valid set,
// per the ticket's explicit AC wording.
var validTimeseriesMetrics = []store.TimeseriesMetric{
	store.MetricCost, store.MetricTokens, store.MetricSessions, store.MetricTurns,
	store.MetricAPIRequests, store.MetricAPIErrors, store.MetricToolCalls, store.MetricToolRejects, store.MetricLOC,
}

// validBreakdownDimensions is SPEC §4.3's closed AnalyticsDimension
// vocabulary (openapi.yaml's `dimension` enum), same closed-vocabulary
// reasoning as validTimeseriesMetrics.
var validBreakdownDimensions = []store.AnalyticsDimension{
	store.DimensionModel, store.DimensionProject, store.DimensionTool,
	store.DimensionDecisionSource, store.DimensionQuerySource, store.DimensionErrorType,
}

// validBreakdownMetrics is SPEC §4.3's closed BreakdownMetric vocabulary
// (openapi.yaml's `metric` enum on getAnalyticsBreakdown).
var validBreakdownMetrics = []store.BreakdownMetric{
	store.BreakdownMetricCost, store.BreakdownMetricCalls, store.BreakdownMetricTokens,
}

func joinStrings[T ~string](values []T) string {
	ss := make([]string, len(values))
	for i, v := range values {
		ss[i] = string(v)
	}
	return strings.Join(ss, ", ")
}

func contains[T comparable](values []T, want T) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// mountAnalyticsRoutes attaches the four analytics read routes this ticket
// owns (SPEC §4.2).
func mountAnalyticsRoutes(r chi.Router, reader AnalyticsReader, logger *slog.Logger) {
	r.Get("/analytics/summary", getAnalyticsSummaryHandler(reader, logger))
	r.Get("/analytics/timeseries", getAnalyticsTimeseriesHandler(reader, logger))
	r.Get("/analytics/breakdown", getAnalyticsBreakdownHandler(reader, logger))
	r.Get("/analytics/decisions", getAnalyticsDecisionsHandler(reader, logger))
}

// parseAnalyticsFilter binds the `from`/`to`/`project`/`model`/`vendor`/
// `source` query parameters every analytics endpoint shares (SPEC §4.3,
// openapi.yaml's AnalyticsSource parameter). `source` is not strictly
// validated against its two-value enum — an unrecognized value simply
// defaults to "event" at the store layer (store.sourceKindOf's documented
// zero-value behaviour), the same permissive-unless-Argus-invented-and-
// AC'd convention params.go's `sort`/`group_by` binding already follows.
func parseAnalyticsFilter(r *http.Request) (store.AnalyticsFilter, error) {
	from, to, err := parseTimeWindow(r)
	if err != nil {
		return store.AnalyticsFilter{}, err
	}
	q := r.URL.Query()
	return store.AnalyticsFilter{
		From:    from,
		To:      to,
		Project: repeatedParam(r, "project"),
		Model:   repeatedParam(r, "model"),
		Vendor:  repeatedParam(r, "vendor"),
		Source:  store.AnalyticsSource(q.Get("source")),
	}, nil
}

// writeNotAttributable writes SPEC §4.3's documented 400 for a metric/
// dimension that cannot honour an active `?model=` filter (openapi.yaml's
// notAttributable example: urn:argus:error:not-attributable).
func writeNotAttributable(w http.ResponseWriter, r *http.Request, detail string) {
	writeProblem(w, r, http.StatusBadRequest, "not-attributable", detail)
}

// getAnalyticsSummaryHandler implements GET /api/v1/analytics/summary (SPEC
// §4.3). No parameter here is Argus-invented-and-closed (source is
// permissive, see parseAnalyticsFilter), so the only 400s are
// parseTimeWindow's.
func getAnalyticsSummaryHandler(reader AnalyticsReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := parseAnalyticsFilter(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		summary, err := query.AnalyticsSummary(r.Context(), reader, f)
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	}
}

// getAnalyticsTimeseriesHandler implements GET /api/v1/analytics/timeseries
// (SPEC §4.3): `metric` (required, closed) and `bucket` (optional, closed)
// are validated here and 400 with the allowed-values list on a miss; `
// group_by`/`limit_series` are permissive/clamped at the store layer, same
// reasoning as `source`.
func getAnalyticsTimeseriesHandler(reader AnalyticsReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		metric := store.TimeseriesMetric(q.Get("metric"))
		if !contains(validTimeseriesMetrics, metric) {
			writeProblem(w, r, http.StatusBadRequest, "invalid-parameter",
				"metric must be one of "+joinStrings(validTimeseriesMetrics))
			return
		}

		bucket := store.AnalyticsBucket(q.Get("bucket"))
		if raw := q.Get("bucket"); raw != "" && bucket != store.BucketHour && bucket != store.BucketDay {
			writeProblem(w, r, http.StatusBadRequest, "invalid-parameter", "bucket must be one of hour, day")
			return
		}

		limitSeries := defaultTimeseriesLimitSeries
		if raw := q.Get("limit_series"); raw != "" {
			n, convErr := strconv.Atoi(raw)
			if convErr != nil || n < 1 {
				writeProblem(w, r, http.StatusBadRequest, "invalid-parameter", "limit_series must be a positive integer")
				return
			}
			limitSeries = n
		}

		f, err := parseAnalyticsFilter(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		g := store.Grouping{
			Metric:      metric,
			Bucket:      bucket,
			GroupBy:     store.GroupBy(q.Get("group_by")),
			LimitSeries: limitSeries,
		}

		series, err := query.AnalyticsSeries(r.Context(), reader, f, g)
		if err != nil {
			if errors.Is(err, store.ErrNotAttributable) {
				writeNotAttributable(w, r, fmt.Sprintf("metric=%s is not attributable under a model filter", metric))
				return
			}
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, series)
	}
}

// getAnalyticsBreakdownHandler implements GET /api/v1/analytics/breakdown
// (SPEC §4.3): `dimension` (required, closed) and `metric` (optional,
// closed) are validated here and 400 with the allowed-values list on a
// miss.
func getAnalyticsBreakdownHandler(reader AnalyticsReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		dimension := store.AnalyticsDimension(q.Get("dimension"))
		if !contains(validBreakdownDimensions, dimension) {
			writeProblem(w, r, http.StatusBadRequest, "invalid-parameter",
				"dimension must be one of "+joinStrings(validBreakdownDimensions))
			return
		}

		metric := store.BreakdownMetric(q.Get("metric"))
		if raw := q.Get("metric"); raw != "" && !contains(validBreakdownMetrics, metric) {
			writeProblem(w, r, http.StatusBadRequest, "invalid-parameter",
				"metric must be one of "+joinStrings(validBreakdownMetrics))
			return
		}

		limit := defaultBreakdownLimit
		if raw := q.Get("limit"); raw != "" {
			n, convErr := strconv.Atoi(raw)
			if convErr != nil || n < 1 {
				writeProblem(w, r, http.StatusBadRequest, "invalid-parameter", "limit must be a positive integer")
				return
			}
			if n > maxBreakdownLimit {
				n = maxBreakdownLimit
			}
			limit = n
		}

		f, err := parseAnalyticsFilter(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		d := store.Dimension{Name: dimension, Metric: metric, Limit: limit}

		breakdown, err := query.AnalyticsBreakdown(r.Context(), reader, f, d)
		if err != nil {
			if errors.Is(err, store.ErrNotAttributable) {
				writeNotAttributable(w, r, fmt.Sprintf("dimension=%s is not attributable under a model filter", dimension))
				return
			}
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, breakdown)
	}
}

// getAnalyticsDecisionsHandler implements GET /api/v1/analytics/decisions
// (SPEC §4.3): only `from`/`to`/`project`, none of them closed.
func getAnalyticsDecisionsHandler(reader AnalyticsReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to, err := parseTimeWindow(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		f := store.AnalyticsFilter{From: from, To: to, Project: repeatedParam(r, "project")}

		matrix, err := query.AnalyticsDecisions(r.Context(), reader, f)
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, matrix)
	}
}
