// Package postgres implements dedup via ingest_dedup ledger (SPEC §1.7 rule 2, lock-ordering SPEC §1.6).
package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// insertIngestDedup runs the SPEC §1.7 rule 2 dedup gate, returning newly admitted keys.
func insertIngestDedup(ctx context.Context, tx pgx.Tx, dedupKeys []string) (map[string]bool, error) {
	if len(dedupKeys) == 0 {
		return map[string]bool{}, nil
	}
	sorted := make([]string, len(dedupKeys))
	copy(sorted, dedupKeys)
	sort.Strings(sorted)

	survived, err := gen.New(tx).InsertIngestDedup(ctx, sorted)
	if err != nil {
		return nil, fmt.Errorf("postgres: ingest_dedup gate: %w", err)
	}
	out := make(map[string]bool, len(survived))
	for _, k := range survived {
		out[k] = true
	}
	return out, nil
}
