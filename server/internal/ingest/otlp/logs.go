package otlp

import (
	"errors"
	"fmt"
	"net/http"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
)

// handleLogs implements POST /v1/logs (SPEC §3.4): decodes resource_logs,
// runs them through the normalizer (SPEC §1.5.1), enqueues events, and
// returns 200/partial_success on success, 503 on backpressure, or 400/415/413
// on request problems. Normalization before enqueue (SPEC §3.6: fails fast
// before queue), so decoding is the only step that can fail.
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	format, body, derr := readBody(w, r, h.maxBodyBytes)
	if derr != nil {
		writeStatus(w, derr.httpStatus, format, derr.grpcCode, derr.message)
		return
	}

	resourceLogs, err := decodeExportRequest(format, body, "resourceLogs", func() *logspb.ResourceLogs { return &logspb.ResourceLogs{} })
	if err != nil {
		writeStatus(w, http.StatusBadRequest, format, grpcCodeInvalidArgument, "invalid ExportLogsServiceRequest: "+err.Error())
		return
	}

	events, rejections := h.normalizer.FromOTLPLogs(&logspb.LogsData{ResourceLogs: resourceLogs})

	if err := h.enqueuer.EnqueueEvents(events); err != nil {
		h.writeQueueFull(w, format, err, len(events))
		return
	}

	writeExportResult(w, format, "rejectedLogRecords", int64(len(rejections)), rejectionSummary(rejections))
}

// writeQueueFull implements SPEC §3.4's backpressure case: 503 + Retry-After
// on any Enqueuer failure. Documents ingest.ErrQueueFull; other errors are
// Enqueuer bugs, not client problems, so they degrade to 503.
func (h *Handler) writeQueueFull(w http.ResponseWriter, format wireFormat, err error, dropped int) {
	if !errors.Is(err, ingest.ErrQueueFull) {
		h.logger.Error("otlp: enqueue failed with an unexpected error", "error", err, "dropped", dropped)
	}
	w.Header().Set("Retry-After", retryAfterSeconds)
	writeStatus(w, http.StatusServiceUnavailable, format, grpcCodeUnavailable, "ingest queue is full")
}

// rejectionSummary renders SPEC §3.4's partial_success.error_message from
// the Rejection list: a short debuggable summary, not every record's attrs.
func rejectionSummary(rejections []normalize.Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	return fmt.Sprintf("%d record(s) rejected (e.g. %q)", len(rejections), rejections[0].Reason)
}
