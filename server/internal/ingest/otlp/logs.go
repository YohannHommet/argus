package otlp

import (
	"errors"
	"fmt"
	"net/http"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
)

// handleLogs implements POST /v1/logs (SPEC §3.4): decode an
// ExportLogsServiceRequest's resource_logs, run them through the normalizer
// (SPEC §1.5.1), enqueue the resulting events, and answer with the
// OTLP/HTTP response contract — 200 (empty, or partial_success on a
// session-less record), 503 on backpressure, or 400/415/413 on a
// request-shaped problem.
//
// Normalization happens before enqueue (SPEC §3.6: "a malformed payload
// fails fast with a 400 and never occupies queue capacity") — decoding is
// the only step that can fail this handler's request; nothing past that
// point can, since FromOTLPLogs never returns a Go error (SPEC §0).
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

// writeQueueFull implements SPEC §3.4's backpressure case ("queue full ->
// 503 + Retry-After: 1") for any Enqueuer failure. EnqueueEvents/
// EnqueueMetrics document exactly one error (ingest.ErrQueueFull); any other
// error would be a bug in the Enqueuer implementation, not a client
// problem, so it degrades to the same 503 rather than inventing a new,
// undocumented contract for a case that should not occur in production.
func (h *Handler) writeQueueFull(w http.ResponseWriter, format wireFormat, err error, dropped int) {
	if !errors.Is(err, ingest.ErrQueueFull) {
		h.logger.Error("otlp: enqueue failed with an unexpected error", "error", err, "dropped", dropped)
	}
	w.Header().Set("Retry-After", retryAfterSeconds)
	writeStatus(w, http.StatusServiceUnavailable, format, grpcCodeUnavailable, "ingest queue is full")
}

// rejectionSummary renders SPEC §3.4's partial_success.error_message from
// the normalizer's Rejection list: a short, debuggable summary rather than
// dumping every rejected record's full attrs map into an HTTP response.
func rejectionSummary(rejections []normalize.Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	return fmt.Sprintf("%d record(s) rejected (e.g. %q)", len(rejections), rejections[0].Reason)
}
