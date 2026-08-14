package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/telemetry"
)

// metaResponse is GET /api/v1/meta's body (SPEC §4.2, §4.3's full
// openapi.yaml Meta schema). P1-05 shipped only {version, commit,
// retention_days}; P3-08 fills in the rest — vendors seen, the four
// exporter/hook/tool-details observations (duplicated at top level and
// inside DataQuality, exactly as openapi.yaml's schema and worked example
// both do), estimated_cost_present, and feature_flags.
type metaResponse struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	RetentionDays int    `json:"retention_days"`

	// FeatureFlags is an empty map, not a speculative guess: SPEC §3.7's
	// config table ("complete and normative") defines no feature-flag keys
	// today, so there is nothing truthful to report yet beyond an empty
	// object. openapi.yaml's worked example shows `{estimated_cost: true}`,
	// but that flag has no backing config key — adding one is out of this
	// ticket's scope (extending internal/config/config.go is not among the
	// files this ticket owns).
	FeatureFlags map[string]bool `json:"feature_flags"`

	Vendors              []string          `json:"vendors"`
	LogsExporterSeen     bool              `json:"logs_exporter_seen"`
	MetricsExporterSeen  bool              `json:"metrics_exporter_seen"`
	HooksSeen            bool              `json:"hooks_seen"`
	ToolDetailsSeen      bool              `json:"tool_details_seen"`
	EstimatedCostPresent bool              `json:"estimated_cost_present"`
	DataQuality          model.DataQuality `json:"data_quality"`
}

// metaSinceEpoch is the lower bound estimatedCostPresent uses to ask "has
// Argus EVER estimated a cost" rather than "in some arbitrary recent
// window" — matching hooks_seen/tool_details_seen's own "ever" semantics
// (ticket note). AnalyticsSummary reads only rollup_hourly/rollup_daily
// (SPEC §2.5), which retention never prunes (retention.go: "rollups ...
// are never deleted by raw retention"), so an all-time window here is a
// bounded, cheap aggregate query, not an events scan.
var metaSinceEpoch = time.Unix(0, 0).UTC()

// estimatedCostPresent answers SPEC's "is any cost estimated rather than
// reported" (ticket note) by checking both the event- and metric-sourced
// rollups (SPEC §2.4: "never summed together", so both must be checked
// independently) for a nonzero estimated-cost total across all of recorded
// history.
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

func metaHandler(cfg *config.Config, reader AnalyticsReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := metaResponse{
			Version:      telemetry.Version,
			Commit:       telemetry.Commit,
			FeatureFlags: map[string]bool{},
			// Vendors defaults to an empty (never nil) slice: openapi.yaml's
			// Meta.vendors schema is a plain (non-nullable) array, and a nil
			// Go slice would otherwise marshal as JSON null when Analytics
			// is nil or Facets reports no vendors yet.
			Vendors: []string{},
		}
		if cfg != nil {
			resp.RetentionDays = cfg.RetentionRawDays
		}

		if reader != nil {
			facets, err := query.Facets(r.Context(), reader)
			if err != nil {
				writeProblem(w, r, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			resp.Vendors = facets.Vendors

			dq, err := query.DataQuality(r.Context(), reader)
			if err != nil {
				writeProblem(w, r, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			resp.DataQuality = dq
			resp.LogsExporterSeen = dq.LogsExporterSeen
			resp.MetricsExporterSeen = dq.MetricsExporterSeen
			resp.HooksSeen = dq.HooksSeen
			resp.ToolDetailsSeen = dq.ToolDetailsSeen

			present, err := estimatedCostPresent(r.Context(), reader)
			if err != nil {
				writeProblem(w, r, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			resp.EstimatedCostPresent = present
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
