// Package postgres — dedup.go implements SPEC §1.7 rule 2's idempotency
// gate: the non-partitioned ingest_dedup ledger is the single mechanism for
// deduplicating every source (otel_log, otel_metric, hook), because
// receipt-time ts on hook events makes any ts-bearing unique key useless for
// hook dedup. WriteBatch and WriteMetrics both call insertIngestDedup before
// touching any projection table (the lock-ordering invariant, SPEC §1.6:
// ingest_dedup is always first).
package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// insertIngestDedup runs the SPEC §1.7 rule 2 gate for dedup keys inside tx.
// Keys are sorted ascending first (lock-ordering invariant SPEC §1.6), then
// inserted with ON CONFLICT DO NOTHING RETURNING to report only new keys.
// Duplicate keys within dedupKeys collapse to a single row—the semantics
// WriteBatch needs for duplicate-event batches. Returns the set of new keys.
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
