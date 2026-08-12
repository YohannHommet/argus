package normalize

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// otlpAttrsToMap converts a slice of OTLP KeyValue pairs into a plain
// map[string]any, decoding each AnyValue into its native Go representation
// via otlpAnyValueToGo. This is the only place in the package that touches
// protobuf types for attribute decoding (package doc comment): attrs.go's
// typed accessors operate purely on the resulting map[string]any so they
// stay reusable by a JSON-based hook normalizer. A later entry overwrites an
// earlier one on a duplicate key; OTLP forbids duplicate keys within one
// attribute set, so this only matters as a defensive default, never as
// documented behaviour a caller should rely on.
func otlpAttrsToMap(kvs []*commonpb.KeyValue) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		out[kv.GetKey()] = otlpAnyValueToGo(kv.GetValue())
	}
	return out
}

// otlpAnyValueToGo decodes one OTLP AnyValue into the native Go type its
// populated oneof variant carries: string, bool, int64, float64, []byte, or
// recursively []any (ArrayValue) / map[string]any (KvlistValue). A nil
// AnyValue (an attribute key present with no value set) decodes to nil
// rather than panicking, since the OTel SDK can legitimately emit one.
func otlpAnyValueToGo(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch t := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return t.StringValue
	case *commonpb.AnyValue_BoolValue:
		return t.BoolValue
	case *commonpb.AnyValue_IntValue:
		return t.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return t.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return t.BytesValue
	case *commonpb.AnyValue_ArrayValue:
		vals := t.ArrayValue.GetValues()
		arr := make([]any, len(vals))
		for i, e := range vals {
			arr[i] = otlpAnyValueToGo(e)
		}
		return arr
	case *commonpb.AnyValue_KvlistValue:
		return otlpAttrsToMap(t.KvlistValue.GetValues())
	default:
		return nil
	}
}

// otlpBodyString extracts a LogRecord's Body as a string, reporting ok=false
// for a nil body or one whose oneof variant is not StringValue — SPEC
// §1.5.1's event-name resolution step 3 ("the record body when it is a
// string") only accepts this one shape.
func otlpBodyString(v *commonpb.AnyValue) (s string, ok bool) {
	if v == nil {
		return "", false
	}
	sv, ok := v.GetValue().(*commonpb.AnyValue_StringValue)
	if !ok {
		return "", false
	}
	return sv.StringValue, true
}
