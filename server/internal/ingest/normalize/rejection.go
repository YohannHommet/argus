package normalize

// Rejection is a source record that a normalizer could not attribute to any
// session at all (SPEC §3.4: the OTLP/HTTP receiver reports these as
// `partial_success{rejected_log_records: N, error_message}`, while the rest
// of the batch is still stored, possibly as `kind='unknown'`). It is
// deliberately not a Go error: SPEC §0 forbids any Go type that can reject a
// *value* a vendor supplies, but a record with no session identity has no
// key to store a row under, so it cannot become a model.Event. Surfacing it
// as data here — rather than dropping it silently or returning an error that
// would abort the whole batch — is what lets FromOTLPLogs (and its P2-03/
// P2-04 siblings) guarantee "a rejection never discards the rest of the
// batch".
type Rejection struct {
	// Reason is a short, human-readable explanation (e.g. "missing
	// session.id"), not a closed vocabulary — SPEC §0 only closes kind,
	// source, correlation and status.
	Reason string

	// Record is the fully merged attribute map for the rejected record —
	// the same shape a surviving record's Event.Attrs would have received
	// — so a rejection is debuggable from the API/UI without re-decoding
	// the original wire payload.
	Record map[string]any
}
