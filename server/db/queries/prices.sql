-- prices.sql holds the fixed, single-statement queries P3-04's prices.go
-- drives through sqlc (SPEC §3.3): the idempotent seed upsert behind
-- `argusd prices import`, and the full-table read the rollup job (P3-05)
-- and internal/query/pricing.Estimate's caller use to resolve a price.

-- name: UpsertModelPrice :one
-- Upserts one seeded model_prices row (model, effective_from) is the PK,
-- per SPEC §2.4. The WHERE clause on the DO UPDATE makes a byte-identical
-- re-import a true no-op: when every column already matches, the UPDATE's
-- WHERE fails, Postgres skips the write entirely, and no row comes back —
-- the caller (ImportPrices) treats pgx.ErrNoRows here as "unchanged".
-- Otherwise the returned `inserted` boolean (xmax = 0 is the standard
-- upsert-reporting idiom: an inserted row's xmax is always 0) tells the
-- caller whether to count it as inserted or updated.
INSERT INTO model_prices (
    model, effective_from, currency,
    input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok,
    source
) VALUES (
    sqlc.arg(model), sqlc.arg(effective_from), sqlc.arg(currency),
    sqlc.arg(input_per_mtok), sqlc.arg(output_per_mtok), sqlc.arg(cache_read_per_mtok), sqlc.arg(cache_write_per_mtok),
    sqlc.arg(source)
)
ON CONFLICT (model, effective_from) DO UPDATE SET
    currency             = EXCLUDED.currency,
    input_per_mtok       = EXCLUDED.input_per_mtok,
    output_per_mtok      = EXCLUDED.output_per_mtok,
    cache_read_per_mtok  = EXCLUDED.cache_read_per_mtok,
    cache_write_per_mtok = EXCLUDED.cache_write_per_mtok,
    source               = EXCLUDED.source
WHERE model_prices.currency             IS DISTINCT FROM EXCLUDED.currency
   OR model_prices.input_per_mtok       IS DISTINCT FROM EXCLUDED.input_per_mtok
   OR model_prices.output_per_mtok      IS DISTINCT FROM EXCLUDED.output_per_mtok
   OR model_prices.cache_read_per_mtok  IS DISTINCT FROM EXCLUDED.cache_read_per_mtok
   OR model_prices.cache_write_per_mtok IS DISTINCT FROM EXCLUDED.cache_write_per_mtok
   OR model_prices.source               IS DISTINCT FROM EXCLUDED.source
RETURNING (xmax = 0) AS inserted;

-- name: ListModelPrices :many
-- Every model_prices row, feeding internal/query/pricing.Estimate's price
-- table (the rollup job's per-bucket cost-estimation pass, P3-05) and the
-- integration test's no-op-reimport assertion.
SELECT model, effective_from, currency,
       input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok,
       source
FROM model_prices
ORDER BY model, effective_from;
