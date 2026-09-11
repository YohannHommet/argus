package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/telemetry"
)

// metaResponse is GET /api/v1/meta's body (SPEC §4.2, §4.3).
type metaResponse struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	RetentionDays int    `json:"retention_days"`

	// FeatureFlags is an empty map (SPEC §3.7 defines no feature-flag keys today).
	FeatureFlags map[string]bool `json:"feature_flags"`

	Vendors              []string          `json:"vendors"`
	LogsExporterSeen     bool              `json:"logs_exporter_seen"`
	MetricsExporterSeen  bool              `json:"metrics_exporter_seen"`
	HooksSeen            bool              `json:"hooks_seen"`
	ToolDetailsSeen      bool              `json:"tool_details_seen"`
	EstimatedCostPresent bool              `json:"estimated_cost_present"`
	DataQuality          model.DataQuality `json:"data_quality"`
}

// metaSinceEpoch bounds estimatedCostPresent's "ever estimated" check;
// rollups are never pruned by retention so all-time is cheap (SPEC §2.5).
var metaSinceEpoch = time.Unix(0, 0).UTC()

// estimatedCostPresent checks both event- and metric-sourced rollups for estimated cost (SPEC §2.4).
func estimatedCostPresent(ctx context.Context, r query.AnalyticsReader) (bool, error) {
	now := time.Now()
	from := metaSinceEpoch
	for _, source := range []store.AnalyticsSource{store.AnalyticsSourceEvent, store.AnalyticsSourceMetric} {
		summary, err := query.AnalyticsSummary(ctx, r, store.AnalyticsFilter{From: &from, To: &now, Source: source})
		if err != nil {
			return false, err
		}
		if summary.Cost.EstimatedUSD > 0 {
			return true, nil
		}
	}
	return false, nil
}

func metaHandler(cfg *config.Config, reader AnalyticsReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := metaResponse{
			Version:      telemetry.Version,
			Commit:       telemetry.Commit,
			FeatureFlags: map[string]bool{},
			// Vendors is empty slice (not nil) to marshal as JSON [] not null.
			Vendors: []string{},
		}
		if cfg != nil {
			resp.RetentionDays = cfg.RetentionRawDays
		}

		if reader != nil {
			facets, err := query.Facets(r.Context(), reader)
			if err != nil {
				writeInternalError(w, r, logger, err)
				return
			}
			resp.Vendors = facets.Vendors

			dq, err := query.DataQuality(r.Context(), reader)
			if err != nil {
				writeInternalError(w, r, logger, err)
				return
			}
			resp.DataQuality = dq
			resp.LogsExporterSeen = dq.LogsExporterSeen
			resp.MetricsExporterSeen = dq.MetricsExporterSeen
			resp.HooksSeen = dq.HooksSeen
			resp.ToolDetailsSeen = dq.ToolDetailsSeen

			present, err := estimatedCostPresent(r.Context(), reader)
			if err != nil {
				writeInternalError(w, r, logger, err)
				return
			}
			resp.EstimatedCostPresent = present
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
