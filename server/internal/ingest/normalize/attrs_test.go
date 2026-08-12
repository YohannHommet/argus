package normalize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAttrsHelpers_CoerceAcrossWireRepresentations exercises attrs.go's
// typed accessors against every representation the live capture (or plain
// JSON decoding) can hand them: native Go types, numeric-looking OTel
// strings, and absence. This is the shared contract P2-03/P2-04 reuse
// (package doc comment), so its coercion behaviour is pinned directly
// rather than only indirectly through OTLP fixtures.
func TestAttrsHelpers_CoerceAcrossWireRepresentations(t *testing.T) {
	t.Parallel()

	attrs := map[string]any{
		"str":          "hello",
		"int_native":   int64(42),
		"int_as_int":   int(7),
		"int_as_float": float64(9),
		"int_as_str":   "123",
		"float_as_str": "1.5",
		"float_native": float64(2.5),
		"bool_native":  true,
		"bool_as_str":  "false",
		"not_a_number": "not-a-number",
		"nested":       map[string]any{"inner": "v"},
		"wrong_type":   42, // int, not int64 — String() must reject it
	}

	t.Run("String", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "hello", *String(attrs, "str"))
		require.Nil(t, String(attrs, "missing"))
		require.Nil(t, String(attrs, "int_native"), "String must not coerce a non-string type")
	})

	t.Run("Int64", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, int64(42), *Int64(attrs, "int_native"))
		require.Equal(t, int64(7), *Int64(attrs, "int_as_int"))
		require.Equal(t, int64(9), *Int64(attrs, "int_as_float"))
		require.Equal(t, int64(123), *Int64(attrs, "int_as_str"))
		require.Nil(t, Int64(attrs, "missing"))
		require.Nil(t, Int64(attrs, "not_a_number"))
		require.Nil(t, Int64(attrs, "nested"))
	})

	t.Run("Float64", func(t *testing.T) {
		t.Parallel()
		require.InDelta(t, 2.5, *Float64(attrs, "float_native"), 1e-9)
		require.InDelta(t, float64(42), *Float64(attrs, "int_native"), 1e-9)
		require.InDelta(t, float64(7), *Float64(attrs, "int_as_int"), 1e-9)
		require.InDelta(t, 1.5, *Float64(attrs, "float_as_str"), 1e-9)
		require.Nil(t, Float64(attrs, "missing"))
		require.Nil(t, Float64(attrs, "not_a_number"))
	})

	t.Run("Bool", func(t *testing.T) {
		t.Parallel()
		require.True(t, *Bool(attrs, "bool_native"))
		require.False(t, *Bool(attrs, "bool_as_str"))
		require.Nil(t, Bool(attrs, "missing"))
		require.Nil(t, Bool(attrs, "not_a_number"))
	})

	t.Run("Map", func(t *testing.T) {
		t.Parallel()
		m, ok := Map(attrs, "nested")
		require.True(t, ok)
		require.Equal(t, "v", m["inner"])

		_, ok = Map(attrs, "missing")
		require.False(t, ok)

		_, ok = Map(attrs, "str")
		require.False(t, ok, "a non-map value is not a Map")
	})

	t.Run("StringLike", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "hello", *StringLike(attrs, "str"))
		require.Equal(t, "42", *StringLike(attrs, "int_native"))
		require.Equal(t, "2.5", *StringLike(attrs, "float_native"))
		require.Equal(t, "true", *StringLike(attrs, "bool_native"))
		require.Nil(t, StringLike(attrs, "missing"))
		require.Nil(t, StringLike(attrs, "nested"), "a map has no scalar representation")
	})
}

func TestAttrsHelpers_EmptyMap(t *testing.T) {
	t.Parallel()
	empty := map[string]any{}
	require.Nil(t, String(empty, "x"))
	require.Nil(t, Int64(empty, "x"))
	require.Nil(t, Float64(empty, "x"))
	require.Nil(t, Bool(empty, "x"))
	_, ok := Map(empty, "x")
	require.False(t, ok)
	require.Nil(t, StringLike(empty, "x"))
}
