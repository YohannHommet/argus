package otlp

import (
	"net/http"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// handleTraces accepts, drops, counts (SPEC §3.4). Traces out of scope but accept avoids 404 retries.
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

// countSpans totals per-span not per-request (SPEC §3.4: reflects actual data dropped).
func countSpans(resourceSpans []*tracepb.ResourceSpans) int {
	n := 0
	for _, rs := range resourceSpans {
		for _, ss := range rs.GetScopeSpans() {
			n += len(ss.GetSpans())
		}
	}
	return n
}
