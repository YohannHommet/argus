package normalize

import (
	"testing"

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
