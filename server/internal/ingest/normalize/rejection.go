package normalize

// Rejection is a source record that normalizer could not attribute to any
// session (SPEC §3.4: OTLP/HTTP reports as partial_success.rejected_log_records).
// Deliberately not an error: SPEC §0 forbids rejecting vendor *values*; session-less
// record has no row key, cannot become model.Event. Surfacing as data ensures
// "rejection never discards rest of batch" (FromOTLPLogs + P2-03/P2-04 siblings).
type Rejection struct {
	// Reason is short explanation (e.g. "missing session.id"), not closed vocabulary.
	Reason string

	// Record is the fully merged attribute map (same shape as Event.Attrs),
	// debuggable from API/UI without re-decoding original wire payload.
	Record map[string]any

	// Count is how many underlying wire-level units (1 for OTel LogRecord/NumberDataPoint,
	// >1 for FromOTLPMetrics "unsupported aggregation" discarding entire Metric's
	// points as one Rejection per audit finding m14). Caller summing rejectedDataPoints
	// should sum Count, not len(rejections).
	Count int
}
