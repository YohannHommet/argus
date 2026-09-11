package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
)

// defaultUnknownKindsSince is the documented default for the `since` parameter.
const defaultUnknownKindsSince = "-24h"

// unknownKindsListResponse is GET /api/v1/quality/unknown-kinds' body (matches openapi.yaml schema exactly).
type unknownKindsListResponse struct {
	Rows []model.UnknownKindGroup `json:"rows"`
}

// mountQualityRoutes attaches the two quality-introspection routes this
// ticket owns (SPEC §4.2).
func mountQualityRoutes(r chi.Router, reader AnalyticsReader, logger *slog.Logger) {
	r.Get("/quality/unknown-kinds", getQualityUnknownKindsHandler(reader, logger))
	r.Get("/quality/hook-latency", getQualityHookLatencyHandler(reader, logger))
}

// getQualityUnknownKindsHandler implements GET /api/v1/quality/unknown-kinds (SPEC §4.3).
func getQualityUnknownKindsHandler(reader query.QualityReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("since")
		if raw == "" {
			raw = defaultUnknownKindsSince
		}
		since, err := parseTimeParam("since", raw, time.Now())
		if err != nil {
			writeBindError(w, r, err)
			return
		}

		rows, err := query.UnknownKinds(r.Context(), reader, *since, 0)
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, unknownKindsListResponse{Rows: rows})
	}
}

// getQualityHookLatencyHandler implements GET /api/v1/quality/hook-latency
// (SPEC §4.3): only `from`/`to`, matching getAnalyticsDecisionsHandler's
// narrow parameter set.
func getQualityHookLatencyHandler(reader query.QualityReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to, err := parseTimeWindow(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}

		hl, err := query.HookLatency(r.Context(), reader, store.AnalyticsFilter{From: from, To: to})
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, hl)
	}
}
