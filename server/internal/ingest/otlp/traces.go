package otlp

import (
	"net/http"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// handleTraces implements POST /v1/traces (SPEC §3.4): "accept, discard,
// count argus_otlp_traces_discarded_total, return an empty
// ExportTraceServiceResponse." Traces are out of scope (DECISIONS.md), but
// silently 404-ing an exporter causes noisy client-side retry loops;
// accepting and dropping is friendlier and is a documented decision, not
// laziness (lead note 3).
//
// The request is still fully decoded (same content-negotiation, gzip-cap,
// and malformed-body handling as /v1/logs and /v1/metrics — SPEC §3.4
// applies those rules uniformly across all three routes) so a client sees
// the same error contract on every route; only the decoded spans are then
// thrown away instead of being turned into anything stored.
func (h *Handler) handleTraces(w http.ResponseWriter, r *http.Request) {
	format, body, derr := readBody(w, r, h.maxBodyBytes)
	if derr != nil {
		writeStatus(w, derr.httpStatus, format, derr.grpcCode, derr.message)
		return
	}

	resourceSpans, err := decodeExportRequest(format, body, "resourceSpans", func() *tracepb.ResourceSpans { return &tracepb.ResourceSpans{} })
	if err != nil {
		writeStatus(w, http.StatusBadRequest, format, grpcCodeInvalidArgument, "invalid ExportTraceServiceRequest: "+err.Error())
		return
	}

	if n := countSpans(resourceSpans); n > 0 {
		h.metrics.TracesDiscarded.Add(float64(n))
	}

	writeExportResult(w, format, "rejectedSpans", 0, "")
}

// countSpans totals every Span across ResourceSpans -> ScopeSpans, for the
// argus_otlp_traces_discarded_total counter (SPEC §3.4): per-span, not
// per-request, so the counter reflects how much data was actually dropped.
func countSpans(resourceSpans []*tracepb.ResourceSpans) int {
	n := 0
	for _, rs := range resourceSpans {
		for _, ss := range rs.GetScopeSpans() {
			n += len(ss.GetSpans())
		}
	}
	return n
}
