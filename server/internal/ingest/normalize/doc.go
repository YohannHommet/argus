// Package normalize turns wire-format vendor telemetry — OTLP LogsData
// (this ticket, P2-02), OTLP MetricsData (P2-04), and hook JSON payloads
// (P2-03) — into the model.Event / model.MetricSample rows the store layer
// persists (SPEC §1.5, §3.4, §3.5). It contains no side effects: every
// exported function is a pure decode, so it can be unit-tested without a
// database, an HTTP server, or a clock other than the one a caller injects.
//
// # Shared contract for sibling normalizers (P2-03 hooks.go, P2-04 otel_metrics.go)
//
//   - Rejection (rejection.go) is the one way a normalizer says "this record
//     cannot be attributed to a session at all" (SPEC §3.4's
//     partial_success.rejected_log_records). It is never an error return —
//     SPEC §0 forbids any Go type that rejects a vendor-supplied *value*,
//     and a record with no session identity has nothing to key a stored row
//     on, so surfacing it as data (not an error) keeps the same shape for
//     every normalizer's "not everything in this batch became an Event"
//     case.
//
//   - attrs.go holds typed, coercing accessors over a plain
//     map[string]any: String, Int64, Float64, Bool (each returning a
//     pointer, nil meaning "absent" so a real zero value is never confused
//     with absence), Map (returning (map[string]any, bool) since a map has
//     no natural pointer-vs-nil-value ambiguity to resolve), and StringLike
//     (stringifies whichever scalar type is actually present, for text
//     columns that have an untyped fallback attribute). They coerce across
//     Go's JSON-decoding types and OTLP's typed AnyValue variants — the live
//     capture shows the *same* logical field (e.g. tool_result's
//     `duration_ms`/`success`) emitted as an OTel string in one event
//     schema and a native OTel int/bool in another — by trying every
//     plausible representation, never by rejecting a value (SPEC §0).
//     attrs.go imports nothing but stdlib: no protobuf types, so it is
//     usable unchanged by a hook-payload normalizer working over
//     encoding/json's map[string]any as well as by this package's own
//     OTLP-attribute walker.
//
//   - OTLP-specific decoding — []*commonpb.KeyValue / *commonpb.AnyValue →
//     map[string]any — lives in otlpattrs.go, kept out of attrs.go
//     specifically so attrs.go stays protobuf-free per the point above.
//
//   - eventname.go's ResolveEventName implements SPEC §1.5.1's OTel-log
//     event-name resolution order and is OTel-log-specific, not part of the
//     shared contract: hook payloads carry their event name directly in
//     `hook_event_name`, with no resolution ambiguity to resolve.
//
// depguard (SPEC §3.1, §3.4): normalize depends on internal/model + stdlib +
// the pinned OTLP protobuf packages only. It must never import
// internal/store, internal/httpapi, or internal/query — normalization is a
// pure decode step upstream of persistence and the read side, and importing
// either would create a cycle or smuggle a side effect into what callers
// expect to be side-effect-free.
package normalize
