// Package postgres owns the model_prices DB side (SPEC §2.4): seeding and reading for the rollup job (P3-05).
// PriceRow isolates pgtype conversions; internal/pricing holds the lookup algorithm.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	argusdb "github.com/YohannHommet/argus/server/db"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// PriceRow is one model_prices row, reduced to plain Go types (mirroring
// internal/pricing.Price — see the package doc comment for why this
// package cannot simply return that type).
type PriceRow struct {
	Model             string
	EffectiveFrom     time.Time
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
}

// dateLayout is the seed JSON's effective_from format — a bare date, since
// model_prices.effective_from is `date`, not `timestamptz` (SPEC §2.4).
const dateLayout = "2006-01-02"

// seedPrice represents one row of server/db/prices/*.json.
type seedPrice struct {
	Model             string  `json:"model"`
	EffectiveFrom     string  `json:"effective_from"`
	Currency          string  `json:"currency"`
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok"`
	Source            string  `json:"source"`
}

// PriceImportSummary reports inserted/updated/unchanged rows from ImportPrices.
type PriceImportSummary struct {
	Inserted  int
	Updated   int
	Unchanged int
}

// ImportPrices upserts embedded price files idempotently; unchanged rows tracked in PriceImportSummary.Unchanged.
func (s *Store) ImportPrices(ctx context.Context) (PriceImportSummary, error) {
	rows, err := loadSeedPrices(argusdb.PricesFS)
	if err != nil {
		return PriceImportSummary{}, err
	}

	q := gen.New(s.pool)
	var summary PriceImportSummary
	for _, r := range rows {
		params, err := r.toParams()
		if err != nil {
			return summary, fmt.Errorf("postgres: import prices: %s: %w", r.Model, err)
		}

		inserted, err := q.UpsertModelPrice(ctx, params)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// DO UPDATE's WHERE clause matched all columns; no change.
			summary.Unchanged++
		case err != nil:
			return summary, fmt.Errorf("postgres: import prices: %s: %w", r.Model, err)
		case inserted:
			summary.Inserted++
		default:
			summary.Updated++
		}
	}
	return summary, nil
}

// ListModelPrices returns all model_prices rows for pricing.Estimate, ordered by (model, effective_from).
func (s *Store) ListModelPrices(ctx context.Context) ([]PriceRow, error) {
	rows, err := gen.New(s.pool).ListModelPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: list model prices: %w", err)
	}

	out := make([]PriceRow, 0, len(rows))
	for _, r := range rows {
		p, err := fromModelPrice(r)
		if err != nil {
			return nil, fmt.Errorf("postgres: list model prices: %s: %w", r.Model, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// loadSeedPrices reads and concatenates every prices/*.json file in fsys.
func loadSeedPrices(fsys fs.FS) ([]seedPrice, error) {
	matches, err := fs.Glob(fsys, "prices/*.json")
	if err != nil {
		return nil, fmt.Errorf("postgres: seed prices: globbing: %w", err)
	}

	var out []seedPrice
	for _, name := range matches {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("postgres: seed prices: reading %s: %w", name, err)
		}
		var rows []seedPrice
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil, fmt.Errorf("postgres: seed prices: parsing %s: %w", name, err)
		}
		out = append(out, rows...)
	}
	return out, nil
}

// toParams converts one seed row to gen.UpsertModelPriceParams with pgtype conversions.
func (r seedPrice) toParams() (gen.UpsertModelPriceParams, error) {
	effectiveFrom, err := time.Parse(dateLayout, r.EffectiveFrom)
	if err != nil {
		return gen.UpsertModelPriceParams{}, fmt.Errorf("parsing effective_from %q: %w", r.EffectiveFrom, err)
	}

	input, err := numericFromFloat(r.InputPerMTok)
	if err != nil {
		return gen.UpsertModelPriceParams{}, fmt.Errorf("input_per_mtok: %w", err)
	}
	output, err := numericFromFloat(r.OutputPerMTok)
	if err != nil {
		return gen.UpsertModelPriceParams{}, fmt.Errorf("output_per_mtok: %w", err)
	}
	cacheRead, err := numericFromFloat(r.CacheReadPerMTok)
	if err != nil {
		return gen.UpsertModelPriceParams{}, fmt.Errorf("cache_read_per_mtok: %w", err)
	}
	cacheWrite, err := numericFromFloat(r.CacheWritePerMTok)
	if err != nil {
		return gen.UpsertModelPriceParams{}, fmt.Errorf("cache_write_per_mtok: %w", err)
	}

	currency := r.Currency
	if currency == "" {
		currency = "USD"
	}
	source := r.Source
	if source == "" {
		source = "repo"
	}

	return gen.UpsertModelPriceParams{
		Model:             r.Model,
		EffectiveFrom:     pgtype.Date{Time: effectiveFrom, Valid: true},
		Currency:          currency,
		InputPerMtok:      input,
		OutputPerMtok:     output,
		CacheReadPerMtok:  cacheRead,
		CacheWritePerMtok: cacheWrite,
		Source:            source,
	}, nil
}

func fromModelPrice(r gen.ModelPrice) (PriceRow, error) {
	if !r.EffectiveFrom.Valid {
		return PriceRow{}, errors.New("effective_from is NULL")
	}

	input, err := numericToFloat(r.InputPerMtok)
	if err != nil {
		return PriceRow{}, fmt.Errorf("input_per_mtok: %w", err)
	}
	output, err := numericToFloat(r.OutputPerMtok)
	if err != nil {
		return PriceRow{}, fmt.Errorf("output_per_mtok: %w", err)
	}
	cacheRead, err := numericToFloat(r.CacheReadPerMtok)
	if err != nil {
		return PriceRow{}, fmt.Errorf("cache_read_per_mtok: %w", err)
	}
	cacheWrite, err := numericToFloat(r.CacheWritePerMtok)
	if err != nil {
		return PriceRow{}, fmt.Errorf("cache_write_per_mtok: %w", err)
	}

	return PriceRow{
		Model:             r.Model,
		EffectiveFrom:     r.EffectiveFrom.Time,
		InputPerMTok:      input,
		OutputPerMTok:     output,
		CacheReadPerMTok:  cacheRead,
		CacheWritePerMTok: cacheWrite,
	}, nil
}

// numericFromFloat converts a plain float64 (as decoded from the seed
// JSON) into a pgtype.Numeric via its decimal string representation,
// avoiding pgtype's float64 Scan path (which round-trips through its own
// base-2/base-10 conversion) in favour of the exact literal sqlc/pgx
// otherwise generates for a numeric column.
func numericFromFloat(v float64) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(v, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// numericToFloat converts a pgtype.Numeric read back from model_prices
// into a plain float64 for pricing.Price/pricing.Estimate, which never
// touches pgtype directly (internal/pricing must not import a
// pgx-shaped type — see that package's doc comment).
func numericToFloat(n pgtype.Numeric) (float64, error) {
	f, err := n.Float64Value()
	if err != nil {
		return 0, err
	}
	if !f.Valid {
		return 0, errors.New("numeric value is NULL")
	}
	return f.Float64, nil
}
