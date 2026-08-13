package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEventRef_RoundTrips is the P2-01 AC: EventRef round-trips.
func TestEventRef_RoundTrips(t *testing.T) {
	tests := []struct {
		name string
		ref  EventRef
	}{
		{"typical", EventRef{TS: time.Date(2026, 8, 11, 9, 12, 4, 221000000, time.UTC), Seq: 918233}},
		{"zero seq", EventRef{TS: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Seq: 0}},
		{"non-UTC location normalizes to UTC", EventRef{
			TS:  time.Date(2026, 8, 11, 9, 12, 4, 0, time.FixedZone("CEST", 2*3600)),
			Seq: 1,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.ref.Encode()
			decoded, err := DecodeEventRef(encoded)
			require.NoError(t, err)
			require.True(t, tt.ref.TS.Equal(decoded.TS))
			require.Equal(t, tt.ref.Seq, decoded.Seq)
		})
	}
}

// TestEventRef_RejectsTamperedInput is the P2-01 AC: EventRef rejects
// tampered input.
func TestEventRef_RejectsTamperedInput(t *testing.T) {
	ref := EventRef{TS: time.Date(2026, 8, 11, 9, 12, 4, 221000000, time.UTC), Seq: 918233}
	valid := ref.Encode()

	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"not base64url", "not!!valid!!base64"},
		{"truncated to one byte", valid[:1]},
		{"garbage payload", "aGVsbG8gd29ybGQ"}, // valid base64url, decodes to "hello world" (no ':')
		{"malformed ts", "OjE"},                // base64url of ":1" — empty ts field
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeEventRef(tt.input)
			require.Error(t, err)
		})
	}
}
