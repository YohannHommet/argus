package normalize

import (
	"encoding/json"
	"math"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// TestOTLPAnyValueToGo_AllVariants exercises every AnyValue oneof branch,
// including the recursive Array/Kvlist cases, and the nil-safety guards
// otlpAttrsToMap/otlpAnyValueToGo need when the OTel SDK emits a KeyValue
// with no value set at all.
func TestOTLPAnyValueToGo_AllVariants(t *testing.T) {
	t.Parallel()

	t.Run("nil AnyValue", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, otlpAnyValueToGo(nil))
	})

	t.Run("string", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "s"}})
		require.Equal(t, "s", v)
	})

	t.Run("bool", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}})
		require.Equal(t, true, v)
	})

	t.Run("int", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 7}})
		require.Equal(t, int64(7), v)
	})

	t.Run("double", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}})
		require.InDelta(t, 1.5, v, 1e-9)
	})

	t.Run("bytes", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte{1, 2}}})
		require.Equal(t, []byte{1, 2}, v)
	})

	t.Run("array", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{
			Values: []*commonpb.AnyValue{
				{Value: &commonpb.AnyValue_StringValue{StringValue: "a"}},
				{Value: &commonpb.AnyValue_IntValue{IntValue: 2}},
			},
		}}})
		require.Equal(t, []any{"a", int64(2)}, v)
	})

	t.Run("kvlist", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{
			Values: []*commonpb.KeyValue{
				{Key: "a", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "b"}}},
			},
		}}})
		require.Equal(t, map[string]any{"a": "b"}, v)
	})

	t.Run("unset oneof", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, otlpAnyValueToGo(&commonpb.AnyValue{}))
	})
}

func TestOTLPAttrsToMap_NilEntriesSkipped(t *testing.T) {
	t.Parallel()
	m := otlpAttrsToMap([]*commonpb.KeyValue{
		nil,
		{Key: "k", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "v"}}},
	})
	require.Equal(t, map[string]any{"k": "v"}, m)
}

func TestOTLPBodyString(t *testing.T) {
	t.Parallel()

	s, ok := otlpBodyString(nil)
	require.False(t, ok)
	require.Empty(t, s)

	s, ok = otlpBodyString(&commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 1}})
	require.False(t, ok, "a non-string body is not a valid event-name source")
	require.Empty(t, s)

	s, ok = otlpBodyString(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude_code.x"}})
	require.True(t, ok)
	require.Equal(t, "claude_code.x", s)
}

// --- M5: sanitization at the OTLP decode boundary ---------------------------

// TestSanitizeAttrString_ReplacesNULAndInvalidUTF8 asserts sanitizeAttrString's
// documented replacement behaviour: a NUL byte and any other invalid UTF-8
// byte sequence are both replaced with U+FFFD, never dropped, and a
// perfectly valid string passes through unchanged (including one that
// happens to already contain U+FFFD).
func TestSanitizeAttrString_ReplacesNULAndInvalidUTF8(t *testing.T) {
	t.Parallel()

	require.Equal(t, "clean", sanitizeAttrString("clean"))
	require.Equal(t, "a�b", sanitizeAttrString("a\x00b"))
	require.Equal(t, "a�b", sanitizeAttrString("a\xffb"))  // \xff alone is not valid UTF-8
	require.Equal(t, "��", sanitizeAttrString("\x00\xc0")) // NUL, then a lone invalid continuation byte

	// The sanitized string must always itself be valid UTF-8 and never
	// contain a raw NUL, since that is exactly what the jsonb write this
	// sanitizes for cannot tolerate (SQLSTATE 22P05, M5 evidence).
	for _, poisoned := range []string{"a\x00b", "a\xffb\x00c", "\xed\xa0\x80"} {
		out := sanitizeAttrString(poisoned)
		require.NotContains(t, out, "\x00")
		require.True(t, utf8.ValidString(out), "sanitized output must be valid UTF-8: %q", out)
	}
}

// TestSanitizeAttrFloat_ReplacesNonFiniteValues asserts sanitizeAttrFloat's
// documented sign-aware string substitution, and that a finite value
// (including 0 and a negative value) is returned unchanged as a float64.
func TestSanitizeAttrFloat_ReplacesNonFiniteValues(t *testing.T) {
	t.Parallel()

	require.Equal(t, "NaN", sanitizeAttrFloat(math.NaN()))
	require.Equal(t, "+Inf", sanitizeAttrFloat(math.Inf(1)))
	require.Equal(t, "-Inf", sanitizeAttrFloat(math.Inf(-1)))
	require.InDelta(t, 1.5, sanitizeAttrFloat(1.5), 1e-9)
	require.InDelta(t, 0.0, sanitizeAttrFloat(0), 1e-9)
	require.InDelta(t, -42.0, sanitizeAttrFloat(-42), 1e-9)

	// The whole point: every sanitized output must be safely json.Marshal-able,
	// unlike the raw NaN/Inf inputs (encoding/json's own documented failure
	// mode this finding is about).
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := json.Marshal(sanitizeAttrFloat(v))
		require.NoError(t, err)
	}
	_, err := json.Marshal(math.NaN())
	require.Error(t, err, "sanity check: raw NaN really does fail json.Marshal")
}

// TestOTLPAnyValueToGo_SanitizesStringAndDouble is the round-trip test M5's
// scope calls for at the decode function itself: a NUL byte in a
// StringValue and a NaN in a DoubleValue must come out sanitized, including
// when nested inside an ArrayValue/KvlistValue (the recursive case) — a
// vendor payload can bury a poisoned value at any depth.
func TestOTLPAnyValueToGo_SanitizesStringAndDouble(t *testing.T) {
	t.Parallel()

	t.Run("NUL byte in top-level string", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "a\x00b"}})
		require.Equal(t, "a�b", v)
	})

	t.Run("NaN in top-level double", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.NaN()}})
		require.Equal(t, "NaN", v)
	})

	t.Run("+Inf in top-level double", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.Inf(1)}})
		require.Equal(t, "+Inf", v)
	})

	t.Run("NUL and NaN nested in array and kvlist", func(t *testing.T) {
		t.Parallel()
		v := otlpAnyValueToGo(&commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{
			Values: []*commonpb.AnyValue{
				{Value: &commonpb.AnyValue_StringValue{StringValue: "x\x00y"}},
				{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{
					Values: []*commonpb.KeyValue{
						{Key: "n", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.NaN()}}},
					},
				}}},
			},
		}}})
		require.Equal(t, []any{"x�y", map[string]any{"n": "NaN"}}, v)
	})
}

// TestOTLPBodyString_SanitizesNUL asserts the record body — a decode path
// that bypasses otlpAnyValueToGo entirely — gets the same sanitization,
// since it is inserted verbatim into an event's attrs map as `body`
// (otel_logs.go) and used for event-name resolution.
func TestOTLPBodyString_SanitizesNUL(t *testing.T) {
	t.Parallel()
	s, ok := otlpBodyString(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude_code.x\x00tra"}})
	require.True(t, ok)
	require.Equal(t, "claude_code.x�tra", s)
}
