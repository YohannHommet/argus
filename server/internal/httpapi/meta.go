package httpapi

import (
	"net/http"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/telemetry"
)

// metaResponse is GET /api/v1/meta's body (SPEC §4.2). This is deliberately
// the smallest slice that satisfies Phase-1 exit criterion 3; P3-08 adds the
// vendor/exporter/data-quality blocks once there is real ingest data to
// report on. Do not add fields here speculatively — extend when the owning
// ticket needs them.
type metaResponse struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	RetentionDays int    `json:"retention_days"`
}

func metaHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := metaResponse{
			Version: telemetry.Version,
			Commit:  telemetry.Commit,
		}
		if cfg != nil {
			resp.RetentionDays = cfg.RetentionRawDays
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
