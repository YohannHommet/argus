package otlp

import (
	"errors"
	"fmt"
	"net/http"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
)

// handleLogs implements POST /v1/logs (SPEC §3.4): decode, normalize, enqueue; fail fast per SPEC §3.6.
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

// writeQueueFull implements SPEC §3.4's backpressure: 503 + Retry-After (unexpected errors also degrade to 503).
func (h *Handler) writeQueueFull(w http.ResponseWriter, format wireFormat, err error, dropped int) {
	if !errors.Is(err, ingest.ErrQueueFull) {
		h.logger.Error("otlp: enqueue failed with an unexpected error", "error", err, "dropped", dropped)
	}
	w.Header().Set("Retry-After", retryAfterSeconds)
	writeStatus(w, http.StatusServiceUnavailable, format, grpcCodeUnavailable, "ingest queue is full")
}

// rejectionSummary renders partial_success.error_message from rejections (summary, not attrs).
func rejectionSummary(rejections []normalize.Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	return fmt.Sprintf("%d record(s) rejected (e.g. %q)", len(rejections), rejections[0].Reason)
}
