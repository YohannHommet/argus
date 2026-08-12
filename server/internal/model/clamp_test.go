package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestClampTimestamp covers the P2-01 AC table: in-window, older-than-
// retention, 2h future, zero time — with the lower bound driven by the
// passed retention window, not a constant.
func TestClampTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	retention := 90 * 24 * time.Hour

	tests := []struct {
		name        string
		ts          time.Time
		retention   time.Duration
		wantSkewed  bool
		wantClamped time.Time
	}{
		{
			name:        "in window",
			ts:          now.Add(-1 * time.Hour),
			retention:   retention,
			wantSkewed:  false,
			wantClamped: now.Add(-1 * time.Hour),
		},
		{
			name:        "exactly at retention lower bound is in window",
			ts:          now.Add(-retention),
			retention:   retention,
			wantSkewed:  false,
			wantClamped: now.Add(-retention),
		},
		{
			name:        "older than retention",
			ts:          now.Add(-retention - time.Hour),
			retention:   retention,
			wantSkewed:  true,
			wantClamped: now,
		},
		{
			name:        "1h in the future is in window",
			ts:          now.Add(1 * time.Hour),
			retention:   retention,
			wantSkewed:  false,
			wantClamped: now.Add(1 * time.Hour),
		},
		{
			name:        "2h in the future is clamped",
			ts:          now.Add(2 * time.Hour),
			retention:   retention,
			wantSkewed:  true,
			wantClamped: now,
		},
		{
			name:        "zero time is clamped",
			ts:          time.Time{},
			retention:   retention,
			wantSkewed:  true,
			wantClamped: now,
		},
		{
			name:        "shorter retention window moves the lower bound",
			ts:          now.Add(-2 * time.Hour),
			retention:   1 * time.Hour, // a 2h-old event is now out of a 1h retention window
			wantSkewed:  true,
			wantClamped: now,
		},
		{
			name:        "longer retention window admits an older event",
			ts:          now.Add(-2 * time.Hour),
			retention:   365 * 24 * time.Hour,
			wantSkewed:  false,
			wantClamped: now.Add(-2 * time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skewed := ClampTimestamp(tt.ts, now, tt.retention)
			require.Equal(t, tt.wantSkewed, skewed)
			require.True(t, tt.wantClamped.Equal(got), "got %v want %v", got, tt.wantClamped)
		})
	}
}
