package normalize

import (
	"math"
	"strings"
	"unicode/utf8"

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
	return sanitizeAttrString(sv.StringValue), true
}

// sanitizeAttrString and sanitizeAttrFloat are the package's single
// sanitisation point for vendor-controlled scalar values (audit finding
// M5): every string/float64 attribute value, wherever it is decoded —
// otlpAnyValueToGo above, otlpBodyString above, and hooks.go's decoded
// JSON tree via sanitizeHookAttrs — passes through one of these before it
// ever reaches a model.Event.Attrs map. Two independent failures converge
// on the same fix:
//
//   - a NUL byte (or any other invalid-UTF-8 byte sequence) in a string
//     attribute makes the `attrs jsonb` cast in the events INSERT raise
//     SQLSTATE 22P05 (a *permanent* error per retry.go's classification);
//   - a NaN/±Inf float64 attribute (an OTLP DoubleValue) makes
//     json.Marshal itself fail before the row is even built (a
//     *transient* error per retry.go's non-PgError default).
//
// Either failure currently takes out the entire WriteBatch — up to
// ARGUS_INGEST_BATCH_SIZE events from unrelated sessions — because the
// batch's INSERT is one statement and a 2xx has already gone out to every
// client that contributed to it.
//
// Sanitisation is replacement, never drop: the key stays present so a
// caller reading `attrs->>'…'` still gets a value, just not the poisoned
// one.
//   - sanitizeAttrString replaces a NUL byte and any invalid UTF-8 byte
//     sequence with U+FFFD (the Unicode replacement character) — the
//     standard "this byte was not valid text" signal, and the same
//     substitution encoding/json itself already performs silently for
//     invalid UTF-8 today (silently, because it is not the NUL case that
//     breaks jsonb; NUL *is* valid UTF-8 and Postgres's jsonb type is the
//     one that rejects it).
//   - sanitizeAttrFloat replaces a non-finite float64 with its sign-aware
//     string form ("NaN", "+Inf", "-Inf"): jsonb has no numeric
//     representation for it, and turning the value into a string
//     preserves that it was observed as non-finite rather than silently
//     coercing it to 0 or dropping the key. This changes the value's Go
//     type (float64 -> string), which is why it is applied at decode time
//     rather than deeper in the pipeline: every typed accessor
//     (attrs.go's Float64, etc.) already treats a wrong-typed attribute as
//     absent, so a sanitized non-finite value simply stops being readable
//     as a number, which is the correct outcome for a value that was
//     never a valid number.
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
