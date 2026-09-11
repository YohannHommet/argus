// Package httpapi is Argus's HTTP surface: router, middleware, ops endpoints, and read API (SPEC §3.1, §3.8, §4.1).
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// Problem is an RFC 9457 problem+json error body. Type is a stable URN (SPEC §4.1).
// RequestID (m2 audit finding) joins client responses to server logs.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// problemURNPrefix namespaces every error type per SPEC §4.1's example
// (urn:argus:error:invalid-cursor).
const problemURNPrefix = "urn:argus:error:"

// writeProblem writes an RFC 9457 problem+json response. Never pass err.Error() as detail (use writeInternalError for internal errors).
func writeProblem(w http.ResponseWriter, r *http.Request, status int, slug, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:      problemURNPrefix + slug,
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: chimw.GetReqID(r.Context()),
	})
}

// logStoreError logs an unexpected store/query failure with the request id for joining to responses (m2 audit finding; nil-safe).
func logStoreError(r *http.Request, logger *slog.Logger, err error) {
	if logger == nil {
		return
	}
	logger.LogAttrs(r.Context(), slog.LevelError, "internal_error",
		slog.String("request_id", chimw.GetReqID(r.Context())),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()),
	)
}

// writeInternalError writes a 500 response, never exposing err to the client (m2 audit finding).
func writeInternalError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	logStoreError(r, logger, err)
	writeProblem(w, r, http.StatusInternalServerError, "internal", "internal error, see server logs for the request id above")
}
