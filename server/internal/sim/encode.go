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

// logsScopeName/metricsScopeName are the InstrumentationScope names the
// live capture and the committed metric fixtures use verbatim
// (testdata/otel/*.json: "com.anthropic.claude_code.events";
// testdata/metrics/*.json: "com.anthropic.claude_code").
const (
	logsScopeName    = "com.anthropic.claude_code.events"
	metricsScopeName = "com.anthropic.claude_code"
)

// wrapLogs assembles one LogsData with a single ResourceLogs/ScopeLogs pair
// carrying id's resource and records, mirroring every testdata/otel/*.json
// fixture's shape (one resource per export, SPEC §1.5.1's "resource
// attributes … service.name/service.version").
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

// protoDeterministic is shared by every protobuf-binary encode call in this
// package: SPEC §7.2's byte-identical-output AC requires that encoding the
// same message twice produces the same bytes, which proto.Marshal alone
// does not guarantee (map iteration order) — Deterministic:true pins field
// and repeated-element order to Go struct field order, which is fixed by
// this package's own code, not by map iteration (none of these messages
// contain a protobuf map field; OTLP attributes are a repeated KeyValue
// list, ordered by construction).
var protoDeterministic = proto.MarshalOptions{Deterministic: true}

// EncodeLogsProtobuf implements --otlp-protocol=http/protobuf for logs
// (SPEC §7.2), using the same go.opentelemetry.io/proto/otlp types the
// receiver decodes (internal/ingest/normalize's Normalizer.FromOTLPLogs),
// so there is no wire-shape drift between what argus-sim sends and what a
// real exporter sends.
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

// EncodeHookBatch marshals a batch of hook payload maps as a JSON array —
// the "also accepts an array for batch replay by argus-sim" shape
// HookNormalizer.FromHookPayload documents (SPEC §3.5). A single-element
// batch marshals as a one-element array, which FromHookPayload also
// accepts (splitHookPayload sniffs the leading '[' regardless of length).
func EncodeHookBatch(payloads []map[string]any) ([]byte, error) {
	b, err := json.Marshal(payloads)
	if err != nil {
		return nil, fmt.Errorf("sim: encode hook batch: %w", err)
	}
	return b, nil
}
