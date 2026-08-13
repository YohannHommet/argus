package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// TestImportPrices_SecondRunIsNoOp is the P3-04 AC: a second
// `argusd prices import` against unchanged seed data changes nothing.
func TestImportPrices_SecondRunIsNoOp(t *testing.T) {
	pool := storetesting.NewPool(t)
	store := postgres.New(pool)
	ctx := context.Background()

	first, err := store.ImportPrices(ctx)
	require.NoError(t, err)
	require.Positive(t, first.Inserted, "the seeded price table must contain at least one row")
	require.Zero(t, first.Updated)
	require.Zero(t, first.Unchanged)

	second, err := store.ImportPrices(ctx)
	require.NoError(t, err)
	require.Zero(t, second.Inserted, "a second import of unchanged seed data must insert nothing")
	require.Zero(t, second.Updated, "a second import of unchanged seed data must update nothing")
	require.Equal(t, first.Inserted, second.Unchanged, "every row from the first import must come back unchanged")

	prices, err := store.ListModelPrices(ctx)
	require.NoError(t, err)
	require.Len(t, prices, first.Inserted, "row count must be stable across the no-op re-import")
}

// TestImportPrices_ListModelPricesRoundTrips confirms the numeric/date
// pgtype conversions round-trip: a seeded row's fields survive the
// insert-then-read path without precision loss relevant to USD pricing.
func TestImportPrices_ListModelPricesRoundTrips(t *testing.T) {
	pool := storetesting.NewPool(t)
	store := postgres.New(pool)
	ctx := context.Background()

	_, err := store.ImportPrices(ctx)
	require.NoError(t, err)

	prices, err := store.ListModelPrices(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, prices)

	found := false
	for _, p := range prices {
		if p.Model != "claude-sonnet-4-5" {
			continue
		}
		found = true
		require.InDelta(t, 3.0, p.InputPerMTok, 1e-9)
		require.InDelta(t, 15.0, p.OutputPerMTok, 1e-9)
		require.InDelta(t, 0.3, p.CacheReadPerMTok, 1e-9)
		require.InDelta(t, 3.75, p.CacheWritePerMTok, 1e-9)
	}
	require.True(t, found, "seeded claude-sonnet-4-5 row must be present after import")
}
