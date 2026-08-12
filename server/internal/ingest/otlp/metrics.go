package otlp

import (
	"fmt"
	"net/http"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
)

// handleMetrics implements POST /v1/metrics (SPEC §3.4): decode an
// ExportMetricsServiceRequest's resource_metrics, run them through the
// normalizer (SPEC §1.8/§2.3), enqueue the resulting samples, and answer
// with the OTLP/HTTP response contract.
//
// Unlike handleLogs, FromOTLPMetrics' rejections are never about a missing
// session.id — a metric sample with no session_id is accepted (SPEC §1.8) —
// they are structurally-undecodable shapes (an aggregation type outside
// Sum/Gauge/Histogram, or a NumberDataPoint with neither AsDouble nor
// AsInt). partial_success.rejected_data_points reports those exactly the
// same way handleLogs reports rejected_log_records: as data the caller can
// act on, never as a 400 that would fail the whole batch.
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

	writeExportResult(w, format, "rejectedDataPoints", int64(len(rejections)), metricRejectionSummary(rejections))
}

// metricRejectionSummary is rejectionSummary's counterpart for
// FromOTLPMetrics' Rejection list (see handleMetrics' doc for why these
// rejections are never about session.id).
func metricRejectionSummary(rejections []normalize.Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	return fmt.Sprintf("%d data point(s) rejected (e.g. %q)", len(rejections), rejections[0].Reason)
}
