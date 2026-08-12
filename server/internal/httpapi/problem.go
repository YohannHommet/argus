// Package httpapi is Argus's HTTP surface: the chi router, middleware
// chain, ops endpoints, the versioned read API, and the embedded SPA
// (docs/SPEC.md §3.1, §3.8, §4.1). It depends inward only on config, model,
// telemetry, and (later) query/store — never the other way around.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Problem is an RFC 9457 "problem+json" error body. type is a stable URN
// (urn:argus:error:<slug>, SPEC §4.1) rather than a URL, since Argus has no
// public docs site for these to resolve against.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// problemURNPrefix namespaces every error type per SPEC §4.1's example
// (urn:argus:error:invalid-cursor).
const problemURNPrefix = "urn:argus:error:"

// writeProblem writes an RFC 9457 problem+json response. slug becomes the
// stable part of the URN type (e.g. "not-found" -> "urn:argus:error:not-found");
// detail is human-readable context, safe to expose to API clients.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, slug, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:     problemURNPrefix + slug,
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	})
}
