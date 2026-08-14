package httpapi_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
)

func TestEncodeDecodeCursor_RoundTripsForEverySortKey(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 8, 11, 9, 31, 44, 900_000_000, time.UTC)

	tests := []struct {
		name   string
		key    string
		values []any
	}{
		{"last_event_at", "last_event_at", []any{fixedTime, "session-1"}},
		{"started_at non-null", "started_at", []any{fixedTime, "session-2"}},
		{"started_at null", "started_at", []any{nil, "session-3"}},
		{"cost_usd", "cost_usd", []any{4.2711, "session-4"}},
		{"event_count", "event_count", []any{int64(480), "session-5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := httpapi.EncodeCursor(tt.key, tt.values...)
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			decoded, err := httpapi.DecodeCursor(encoded, tt.key)
			require.NoError(t, err)
			require.Equal(t, tt.key, decoded.Key)
			require.Len(t, decoded.Values, len(tt.values))
		})
	}
}

func TestEncodeCursor_IsURLSafeAndOpaque(t *testing.T) {
	t.Parallel()

	encoded, err := httpapi.EncodeCursor("last_event_at", "2026-08-11T09:31:44.9Z", "session-1")
	require.NoError(t, err)

	// URL-safe base64: no '+', '/', or '=' padding (SPEC §4.1).
	require.NotContains(t, encoded, "+")
	require.NotContains(t, encoded, "/")
	require.NotContains(t, encoded, "=")

	_, err = base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err, "encoded cursor must be valid URL-safe base64 with no padding")
}

func TestDecodeCursor_SortKeyBinding(t *testing.T) {
	t.Parallel()

	encoded, err := httpapi.EncodeCursor("cost_usd", 4.27, "session-1")
	require.NoError(t, err)

	_, err = httpapi.DecodeCursor(encoded, "started_at")
	require.ErrorIs(t, err, httpapi.ErrInvalidCursor, "a cursor minted for one sort key must be rejected when replayed against another")
}

func TestDecodeCursor_TamperRejection(t *testing.T) {
	t.Parallel()

	valid, err := httpapi.EncodeCursor("last_event_at", "2026-08-11T09:31:44.9Z", "session-1")
	require.NoError(t, err)

	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64 at all", "not-valid-base64!!!"},
		{"flipped first byte", flipFirstChar(valid)},
		{"truncated", valid[:len(valid)/2]},
		{"valid base64 of non-JSON bytes", base64.RawURLEncoding.EncodeToString([]byte("not json"))},
		{"valid JSON missing k", base64.RawURLEncoding.EncodeToString([]byte(`{"v":["x","y"]}`))},
		{"valid JSON missing v", base64.RawURLEncoding.EncodeToString([]byte(`{"k":"last_event_at"}`))},
		{"valid JSON empty v", base64.RawURLEncoding.EncodeToString([]byte(`{"k":"last_event_at","v":[]}`))},
		{"empty string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := httpapi.DecodeCursor(tt.cursor, "last_event_at")
			require.Error(t, err)
			require.ErrorIsf(t, err, httpapi.ErrInvalidCursor, "expected ErrInvalidCursor, got %v", err)
		})
	}
}

// flipFirstChar mutates a URL-safe base64 string's first character to a
// different valid base64 character, simulating single-byte tampering while
// staying syntactically plausible base64 — the harder case than outright
// garbage, since it still decodes as base64 and may still decode as JSON.
func flipFirstChar(s string) string {
	if len(s) == 0 {
		return s
	}
	b := []byte(s)
	if b[0] == 'a' {
		b[0] = 'b'
	} else {
		b[0] = 'a'
	}
	return string(b)
}
