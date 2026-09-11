package query

import (
	"context"
	"fmt"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// QualityReader is the narrow store port for quality operations.
type QualityReader interface {
	Facets(ctx context.Context) (model.Facets, error)
	DataQuality(ctx context.Context) (model.DataQuality, error)
	UnknownKinds(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error)
	HookLatency(ctx context.Context, f store.AnalyticsFilter) (model.HookLatency, error)
}

// Facets implements GET /api/v1/facets (SPEC §4.2).
func Facets(ctx context.Context, r QualityReader) (model.Facets, error) {
	f, err := r.Facets(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("query: facets: %w", err)
	}
	return f, nil
}

// DataQuality backs the data_quality block of GET /api/v1/meta (SPEC §4.2).
func DataQuality(ctx context.Context, r QualityReader) (model.DataQuality, error) {
	dq, err := r.DataQuality(ctx)
	if err != nil {
		return model.DataQuality{}, fmt.Errorf("query: data quality: %w", err)
	}
	return dq, nil
}

// UnknownKinds implements GET /api/v1/quality/unknown-kinds (SPEC §4.3).
// since/limit are assumed already validated/defaulted by httpapi/params.go.
func UnknownKinds(ctx context.Context, r QualityReader, since time.Time, limit int) ([]model.UnknownKindGroup, error) {
	rows, err := r.UnknownKinds(ctx, since, limit)
	if err != nil {
		return nil, fmt.Errorf("query: unknown kinds: %w", err)
	}
	return rows, nil
}

// HookLatency implements GET /api/v1/quality/hook-latency (SPEC §4.3).
func HookLatency(ctx context.Context, r QualityReader, f store.AnalyticsFilter) (model.HookLatency, error) {
	hl, err := r.HookLatency(ctx, f)
	if err != nil {
		return model.HookLatency{}, fmt.Errorf("query: hook latency: %w", err)
	}
	return hl, nil
}
