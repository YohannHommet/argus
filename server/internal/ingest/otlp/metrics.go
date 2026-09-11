package otlp

import (
	"fmt"
	"net/http"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
)

// handleMetrics implements POST /v1/metrics (SPEC §3.4): decodes
// resource_metrics, runs through the normalizer (SPEC §1.8/§2.3), enqueues
// samples, and returns the OTLP/HTTP response. Unlike handleLogs, rejections
// are never about missing session.id (SPEC §1.8: acceptable) — they are
// structurally-undecodable shapes (aggregation type, NumberDataPoint format).
// partial_success.rejected_data_points reports them like handleLogs' rejected_log_records.
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	format, body, derr := readBody(w, r, h.maxBodyBytes)
	if derr != nil {
		writeStatus(w, derr.httpStatus, format, derr.grpcCode, derr.message)
		return
	}

	resourceMetrics, err := decodeExportRequest(format, body, "resourceMetrics", func() *metricspb.ResourceMetrics { return &metricspb.ResourceMetrics{} })
	if err != nil {
		writeStatus(w, http.StatusBadRequest, format, grpcCodeInvalidArgument, "invalid ExportMetricsServiceRequest: "+err.Error())
		return
	}

	samples, rejections := h.normalizer.FromOTLPMetrics(&metricspb.MetricsData{ResourceMetrics: resourceMetrics})

	if err := h.enqueuer.EnqueueMetrics(samples); err != nil {
		h.writeQueueFull(w, format, err, len(samples))
		return
	}

	// Sum Rejection.Count, not len(rejections): a single Rejection can stand for
	// an entire Metric's worth of data points. FromOTLPMetrics emits one value
	// for an unsupported aggregation type regardless of how many points it carried.
	var rejectedDataPoints int64
	for _, rej := range rejections {
		rejectedDataPoints += int64(rej.Count)
	}

	writeExportResult(w, format, "rejectedDataPoints", rejectedDataPoints, metricRejectionSummary(rejections))
}

// metricRejectionSummary is rejectionSummary's counterpart for FromOTLPMetrics'
// Rejection list (rejections are never about session.id).
func metricRejectionSummary(rejections []normalize.Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	return fmt.Sprintf("%d data point(s) rejected (e.g. %q)", len(rejections), rejections[0].Reason)
}
