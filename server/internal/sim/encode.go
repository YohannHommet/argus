package sim

import (
	"encoding/json"
	"fmt"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// logsScopeName/metricsScopeName match live capture and fixtures
// ("com.anthropic.claude_code.events" and "com.anthropic.claude_code").
const (
	logsScopeName    = "com.anthropic.claude_code.events"
	metricsScopeName = "com.anthropic.claude_code"
)

// wrapLogs assembles LogsData with one ResourceLogs/ScopeLogs pair,
// mirroring fixture shape (SPEC §1.5.1).
func wrapLogs(id sessionIdentity, records []*logspb.LogRecord) *logspb.LogsData {
	return &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: id.resource(),
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commonpb.InstrumentationScope{Name: logsScopeName, Version: id.appVersion},
				LogRecords: records,
			}},
		}},
	}
}

// wrapMetrics assembles one MetricsData analogous to wrapLogs, matching the
// testdata/metrics/*.json fixtures' resource/scope shape.
func wrapMetrics(id sessionIdentity, metrics []*metricspb.Metric) *metricspb.MetricsData {
	return &metricspb.MetricsData{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: id.resource(),
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: metricsScopeName},
				Metrics: metrics,
			}},
		}},
	}
}

// protoDeterministic ensures byte-identical output (SPEC §7.2 AC): pins
// field/element order to struct order (no map iteration, OTLP attributes are
// repeated KeyValue lists ordered by construction).
var protoDeterministic = proto.MarshalOptions{Deterministic: true}

// EncodeLogsProtobuf implements --otlp-protocol=http/protobuf for logs
// (SPEC §7.2), using the same types as Normalizer.FromOTLPLogs.
func EncodeLogsProtobuf(data *logspb.LogsData) ([]byte, error) {
	b, err := protoDeterministic.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("sim: encode logs protobuf: %w", err)
	}
	return b, nil
}

// EncodeMetricsProtobuf is EncodeLogsProtobuf's metrics counterpart,
// targeting Normalizer.FromOTLPMetrics.
func EncodeMetricsProtobuf(data *metricspb.MetricsData) ([]byte, error) {
	b, err := protoDeterministic.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("sim: encode metrics protobuf: %w", err)
	}
	return b, nil
}

// jsonMarshalOptions produces stable, human-diffable JSON (used by
// --otlp-protocol=http/json and, unconditionally, for hook payloads —
// hooks have no protobuf form, SPEC §1.5.2's transport is native JSON
// only).
var jsonMarshalOptions = protojson.MarshalOptions{}

// EncodeLogsJSON implements --otlp-protocol=http/json for logs (SPEC §7.2).
func EncodeLogsJSON(data *logspb.LogsData) ([]byte, error) {
	b, err := jsonMarshalOptions.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("sim: encode logs json: %w", err)
	}
	return b, nil
}

// EncodeMetricsJSON is EncodeLogsJSON's metrics counterpart.
func EncodeMetricsJSON(data *metricspb.MetricsData) ([]byte, error) {
	b, err := jsonMarshalOptions.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("sim: encode metrics json: %w", err)
	}
	return b, nil
}

// EncodeHookBatch marshals hook payloads as JSON array for batch replay
// (SPEC §3.5; FromHookPayload accepts single or multi-element arrays).
func EncodeHookBatch(payloads []map[string]any) ([]byte, error) {
	b, err := json.Marshal(payloads)
	if err != nil {
		return nil, fmt.Errorf("sim: encode hook batch: %w", err)
	}
	return b, nil
}
