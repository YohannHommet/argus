// Package postgres — prices.go owns the DB side of docs/SPEC.md §2.4's
// model_prices table: importing the seeded server/db/prices/*.json price
// table idempotently (the `argusd prices import` subcommand, SPEC §3.8),
// and reading the full table back out as []PriceRow for the rollup job
// (P3-05) and any other caller that needs to resolve a price. PriceRow
// mirrors internal/pricing.Price field-for-field but is declared here
// rather than being internal/pricing.Price itself: this package's job is
// isolating every pgtype/numeric/date conversion the DB driver needs behind
// plain Go types, the same reason gen.ModelPrice (sqlc's own pgtype-typed
// row) is never handed to a caller directly. internal/pricing is a leaf
// package depguard's "store" rule permits internal/store to import (it
// denies only internal/httpapi and internal/query — see internal/pricing's
// package doc for why the algorithm lives there and not in
// internal/query/pricing), so the rollup job (rollups.go) converts a
// []PriceRow into a []pricing.Price once per run and calls pricing.Estimate
// directly instead of reimplementing its lookup.
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

// seedPrice mirrors one row of server/db/prices/*.json.
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

// PriceImportSummary reports what ImportPrices did, per row, so
// `argusd prices import` can print an honest summary and its integration
// test can assert a second run changes nothing.
type PriceImportSummary struct {
	Inserted  int
	Updated   int
	Unchanged int
}

// ImportPrices reads every server/db/prices/*.json file embedded in the
// binary (argusdb.PricesFS) and upserts each row into model_prices,
// keyed on (model, effective_from) as SPEC §2.4 requires. It is
// idempotent: re-running it with unchanged seed data updates nothing (see
// UpsertModelPrice's WHERE clause) — PriceImportSummary.Unchanged counts
// those rows rather than silently reporting them as updated.
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
			// The DO UPDATE's WHERE clause found every column already
			// matching, so Postgres skipped the write and returned nothing.
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

// ListModelPrices returns every model_prices row as []PriceRow, ready for
// the caller (the rollup job, P3-05) to convert into
// internal/pricing.Price and hand to pricing.Estimate. Ordering is
// not significant to pricing.Estimate, which scans the whole slice, but
// ListModelPrices (the sqlc query) orders by (model, effective_from) for
// deterministic output.
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

// loadSeedPrices reads and concatenates every *.json file under prices/ in
// fsys (argusdb.PricesFS in production; a caller-built fs.FS in tests).
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

// toParams converts one seed row into gen.UpsertModelPriceParams,
// including the numeric/date pgtype conversions.
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

// fromModelPrice converts one gen.ModelPrice row back into a PriceRow.
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
