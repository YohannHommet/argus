// Package httpapi is Argus's HTTP surface: the chi router, middleware
// chain, ops endpoints, the versioned read API, and the embedded SPA
// (docs/SPEC.md §3.1, §3.8, §4.1). It depends inward only on config, model,
// telemetry, and (later) query/store — never the other way around.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// Problem is an RFC 9457 "problem+json" error body. type is a stable URN
// (urn:argus:error:<slug>, SPEC §4.1) rather than a URL, since Argus has no
// public docs site for these to resolve against.
//
// RequestID (m2 audit finding) is chi's per-request id, the same one
// AccessLog attaches to every access-log line: every 5xx response carries
// it so an operator can join the client-visible response to the log line
// that has the real, non-public-facing error text — see writeInternalError.
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

// writeProblem writes an RFC 9457 problem+json response. slug becomes the
// stable part of the URN type (e.g. "not-found" -> "urn:argus:error:not-found");
// detail is human-readable context, safe to expose to API clients — callers
// reporting an *unexpected* failure (a wrapped store/DB error, which can
// carry internal detail like a DB user/database name, m2 audit finding)
// must use writeInternalError instead, never pass err.Error() here.
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

// logStoreError logs an unexpected store/query failure at ERROR level,
// tagged with the same request id writeProblem puts in every response
// body's `request_id` field, so an operator can join a client-visible
// error back to the log line that carries err's real text (m2 audit
// finding). logger may be nil (e.g. a test that doesn't care, or
// Deps.Logger unset): the call is then a no-op.
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

// writeInternalError writes a 500 urn:argus:error:internal problem+json
// response for an unexpected store/query failure, without ever putting
// err's own text in the client-visible `detail` (m2 audit finding: 22
// non-test call sites did exactly that, and at least one — GET /readyz,
// unauthenticated by default — could echo a raw pgx connection-failure
// string containing the DB user and database name). See logStoreError for
// where the real error goes instead.
func writeInternalError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	logStoreError(r, logger, err)
	writeProblem(w, r, http.StatusInternalServerError, "internal", "internal error, see server logs for the request id above")
}
