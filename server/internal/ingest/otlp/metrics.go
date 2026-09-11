package otlp

import (
	"fmt"
	"net/http"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
)

// handleMetrics implements POST /v1/metrics (SPEC §3.4): decode, normalize, enqueue; rejections never about session.id.
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

	// Sum Rejection.Count, not len: one Rejection can stand for an entire Metric's worth of points.
	var rejectedDataPoints int64
	for _, rej := range rejections {
		rejectedDataPoints += int64(rej.Count)
	}

	writeExportResult(w, format, "rejectedDataPoints", rejectedDataPoints, metricRejectionSummary(rejections))
}

// metricRejectionSummary renders rejection summary for metrics (structural only, never session.id).
func metricRejectionSummary(rejections []normalize.Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	return fmt.Sprintf("%d data point(s) rejected (e.g. %q)", len(rejections), rejections[0].Reason)
}
