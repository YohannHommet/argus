// Package query — analytics.go is its read-service layer for the four
// analytics endpoints (SPEC §3.1: "httpapi -> query -> store", P3-08). Unlike
// sessions.go/events.go it computes nothing of its own: model.Summary/
// Series/Breakdown/DecisionMatrix already carry SPEC §4.3's exact wire
// shape (read_analytics.go's own doc comment: "this file's own job is
// choosing which fixed query to run ... and applying the two pieces of
// logic SQL alone cannot express"), so every function here is a thin
// call-through that only adds error context and lets
// store.ErrNotAttributable propagate unchanged (via %w) for httpapi to
// recognise with errors.Is.
package query

import (
	"context"
	"fmt"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// AnalyticsReader is the narrow store port every Analytics* function below
// needs — the same consumer-owned-port convention as SessionReader/
// EventReader.
type AnalyticsReader interface {
	AnalyticsSummary(ctx context.Context, f store.AnalyticsFilter) (model.Summary, error)
	AnalyticsSeries(ctx context.Context, f store.AnalyticsFilter, g store.Grouping) (model.Series, error)
	AnalyticsBreakdown(ctx context.Context, f store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error)
	AnalyticsDecisions(ctx context.Context, f store.AnalyticsFilter) (model.DecisionMatrix, error)
}

// AnalyticsSummary implements GET /api/v1/analytics/summary (SPEC §4.3). f
// is assumed already validated by httpapi/params.go.
func AnalyticsSummary(ctx context.Context, r AnalyticsReader, f store.AnalyticsFilter) (model.Summary, error) {
	s, err := r.AnalyticsSummary(ctx, f)
	if err != nil {
		return model.Summary{}, fmt.Errorf("query: analytics summary: %w", err)
	}
	return s, nil
}

// AnalyticsSeries implements GET /api/v1/analytics/timeseries (SPEC §4.3).
// f and g are assumed already validated by httpapi/params.go; a non-
// attributable metric under a model filter surfaces as store.
// ErrNotAttributable (wrapped, still matched by errors.Is) for httpapi to
// map onto its 400.
func AnalyticsSeries(ctx context.Context, r AnalyticsReader, f store.AnalyticsFilter, g store.Grouping) (model.Series, error) {
	series, err := r.AnalyticsSeries(ctx, f, g)
	if err != nil {
		return model.Series{}, fmt.Errorf("query: analytics series: %w", err)
	}
	return series, nil
}

// AnalyticsBreakdown implements GET /api/v1/analytics/breakdown (SPEC
// §4.3), with the same store.ErrNotAttributable propagation as
// AnalyticsSeries.
func AnalyticsBreakdown(ctx context.Context, r AnalyticsReader, f store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error) {
	b, err := r.AnalyticsBreakdown(ctx, f, d)
	if err != nil {
		return model.Breakdown{}, fmt.Errorf("query: analytics breakdown: %w", err)
	}
	return b, nil
}

// AnalyticsDecisions implements GET /api/v1/analytics/decisions (SPEC §4.3).
func AnalyticsDecisions(ctx context.Context, r AnalyticsReader, f store.AnalyticsFilter) (model.DecisionMatrix, error) {
	d, err := r.AnalyticsDecisions(ctx, f)
	if err != nil {
		return model.DecisionMatrix{}, fmt.Errorf("query: analytics decisions: %w", err)
	}
	return d, nil
}
