package pricing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestEstimate is the P3-04 AC table: exact model match; a versioned
// suffix resolving by prefix; two effective_from rows resolving by event
// date; unknown model -> ErrNoPrice and no cost; cache-read/write priced
// separately from input/output.
func TestEstimate(t *testing.T) {
	prices := []Price{
		{
			Model: "claude-sonnet-4-5", EffectiveFrom: date("2025-01-01"),
			InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75,
		},
		// A later price change for the same model, so lookup-by-date has
		// two effective_from rows to choose between.
		{
			Model: "claude-sonnet-4-5", EffectiveFrom: date("2025-06-01"),
			InputPerMTok: 4, OutputPerMTok: 20, CacheReadPerMTok: 0.4, CacheWritePerMTok: 5,
		},
		{
			Model: "claude-opus-5", EffectiveFrom: date("2025-01-01"),
			InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.5, CacheWritePerMTok: 18.75,
		},
	}

	tests := []struct {
		name    string
		model   string
		tokens  Tokens
		at      time.Time
		want    float64
		wantErr error
	}{
		{
			name:   "exact model match",
			model:  "claude-opus-5",
			tokens: Tokens{Input: 1_000_000, Output: 1_000_000},
			at:     date("2025-03-01"),
			want:   15 + 75,
		},
		{
			name:   "versioned suffix resolves by longest prefix",
			model:  "claude-sonnet-4-5-20250929",
			tokens: Tokens{Input: 1_000_000},
			at:     date("2025-03-01"),
			want:   3,
		},
		{
			name:   "two effective_from rows resolve by event date: before the change",
			model:  "claude-sonnet-4-5",
			tokens: Tokens{Output: 1_000_000},
			at:     date("2025-05-31"),
			want:   15,
		},
		{
			name:   "two effective_from rows resolve by event date: on/after the change",
			model:  "claude-sonnet-4-5",
			tokens: Tokens{Output: 1_000_000},
			at:     date("2025-06-01"),
			want:   20,
		},
		{
			name:    "unknown model has no price and no cost",
			model:   "gpt-5",
			tokens:  Tokens{Input: 1_000_000, Output: 1_000_000},
			at:      date("2025-06-01"),
			want:    0,
			wantErr: ErrNoPrice,
		},
		{
			name:  "cache-read and cache-write priced separately from input/output",
			model: "claude-sonnet-4-5",
			tokens: Tokens{
				Input: 0, Output: 0,
				CacheRead: 1_000_000, CacheWrite: 1_000_000,
			},
			at:   date("2025-03-01"),
			want: 0.3 + 3.75,
		},
		{
			name:  "all four token kinds summed independently",
			model: "claude-sonnet-4-5",
			tokens: Tokens{
				Input: 2_000_000, Output: 1_000_000,
				CacheRead: 500_000, CacheWrite: 250_000,
			},
			at:   date("2025-03-01"),
			want: 2*3 + 1*15 + 0.5*0.3 + 0.25*3.75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Estimate(prices, tt.model, tt.tokens, tt.at)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Zero(t, got, "cost must not be a silent zero-substitute when ErrNoPrice is returned")
				return
			}
			require.NoError(t, err)
			require.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

// TestEstimate_NoPriceAtAllNeverFallsBackAcrossModels asserts that even
// with other models priced, a model with zero matching rows (no exact, no
// prefix) returns ErrNoPrice rather than silently borrowing another
// model's price.
func TestEstimate_NoPriceAtAllNeverFallsBackAcrossModels(t *testing.T) {
	prices := []Price{
		{Model: "claude-opus-5", EffectiveFrom: date("2025-01-01"), InputPerMTok: 15, OutputPerMTok: 75},
	}

	_, err := Estimate(prices, "claude-haiku-4-5", Tokens{Input: 1}, date("2025-06-01"))
	require.ErrorIs(t, err, ErrNoPrice)
}

// TestEstimate_ExactWinsOverLongerPrefix ensures an exact match always
// wins even when a different, unrelated price row happens to be a longer
// string prefix match of a *different* observed model (i.e. exact beats
// prefix, never the reverse), and that a model with no effective_from row
// at or before the event date returns ErrNoPrice rather than resolving to
// a future price.
func TestEstimate_ExactWinsOverLongerPrefix(t *testing.T) {
	prices := []Price{
		{Model: "claude-sonnet-4", EffectiveFrom: date("2025-01-01"), InputPerMTok: 2, OutputPerMTok: 10},
		{Model: "claude-sonnet-4-5", EffectiveFrom: date("2025-01-01"), InputPerMTok: 3, OutputPerMTok: 15},
	}

	got, err := Estimate(prices, "claude-sonnet-4-5", Tokens{Input: 1_000_000}, date("2025-06-01"))
	require.NoError(t, err)
	require.InDelta(t, 3, got, 1e-9, "exact match for claude-sonnet-4-5 must win over the shorter claude-sonnet-4 prefix")
}

func TestEstimate_NoRowBeforeEventDate(t *testing.T) {
	prices := []Price{
		{Model: "claude-opus-5", EffectiveFrom: date("2025-06-01"), InputPerMTok: 15, OutputPerMTok: 75},
	}

	_, err := Estimate(prices, "claude-opus-5", Tokens{Input: 1}, date("2025-01-01"))
	require.ErrorIs(t, err, ErrNoPrice)
}
