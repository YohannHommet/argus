// Package normalize turns wire-format vendor telemetry (OTLP LogsData/MetricsData, hook JSON)
// into model.Event/model.MetricSample rows. Pure decoding: no side effects, testable
// without database/server/injected clock.
//
// Shared contract for normalizers: Rejection (rejection.go) surfaces records that cannot
// be attributed to a session. attrs.go provides coercing typed accessors (String, Int64,
// Float64, Bool, Map, StringLike) over map[string]any. otlpattrs.go handles protobuf
// decoding; attrs.go stays protobuf-free for reuse by hook normalizers. eventname.go's
// ResolveEventName is OTLP-log-specific.
//
// depguard: normalize imports only internal/model + stdlib + pinned OTLP protobuf.
// Never internal/store/httpapi/query — must stay pure decode, upstream of persistence.
package normalize
