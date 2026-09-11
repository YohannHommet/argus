package normalize

import (
	"math"
	"strings"
	"unicode/utf8"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// otlpAttrsToMap converts OTLP KeyValue slice to map[string]any (only protobuf
// touch-point per package doc; attrs.go's accessors reusable by JSON normalizers).
// Later entry overwrites on duplicate key (OTLP forbids; defensive only).
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

// otlpAnyValueToGo decodes OTLP AnyValue to native Go type (string, bool, int64,
// float64, []byte, recursively []any or map[string]any). Nil AnyValue → nil
// (OTel SDK can legitimately emit; don't panic).
func otlpAnyValueToGo(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch t := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return sanitizeAttrString(t.StringValue)
	case *commonpb.AnyValue_BoolValue:
		return t.BoolValue
	case *commonpb.AnyValue_IntValue:
		return t.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return sanitizeAttrFloat(t.DoubleValue)
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

// otlpBodyString extracts LogRecord Body as string, ok=false for nil or
// non-StringValue (SPEC §1.5.1 step 3 only accepts string body).
func otlpBodyString(v *commonpb.AnyValue) (s string, ok bool) {
	if v == nil {
		return "", false
	}
	sv, ok := v.GetValue().(*commonpb.AnyValue_StringValue)
	if !ok {
		return "", false
	}
	return sanitizeAttrString(sv.StringValue), true
}

// sanitizeAttrString and sanitizeAttrFloat are the package's single sanitization
// point (M5): replace poisoned vendor scalars before they reach model.Event.Attrs.
// NUL bytes and NaN/±Inf floats cause silent WriteBatch failures; replaced with
// U+FFFD and string form respectively, never dropped.
func sanitizeAttrString(s string) string {
	if !strings.ContainsRune(s, 0) && utf8.ValidString(s) {
		return s
	}
	s = strings.ReplaceAll(s, "\x00", "�")
	return strings.ToValidUTF8(s, "�")
}

func sanitizeAttrFloat(f float64) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	default:
		return f
	}
}

// sanitizeHookAttrValue recursively applies sanitizeAttrString/
// sanitizeAttrFloat to a value decoded by encoding/json (string, float64,
// bool, nil, []any, or map[string]any — the only types Unmarshal into
// `any` ever produces), so a hook payload converges on the exact same
// sanitisation as the OTLP decode path above. JSON text cannot itself
// encode NaN/Infinity, so the float64 branch is defensive rather than
// reachable through today's hook transport, but keeping both branches
// here (instead of only the string one) is what makes this the *one*
// sanitisation point hooks.go needs to call, rather than two.
func sanitizeHookAttrValue(v any) any {
	switch t := v.(type) {
	case string:
		return sanitizeAttrString(t)
	case float64:
		return sanitizeAttrFloat(t)
	case map[string]any:
		for k, val := range t {
			t[k] = sanitizeHookAttrValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = sanitizeHookAttrValue(val)
		}
		return t
	default:
		return v
	}
}

// sanitizeHookAttrs sanitizes every string/float64 value in a decoded hook
// payload, in place, and returns it for call-site convenience.
func sanitizeHookAttrs(attrs map[string]any) map[string]any {
	sanitizeHookAttrValue(attrs)
	return attrs
}
