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

// insertIngestDedup runs the SPEC §1.7 rule 2 gate for the given dedup keys
// inside tx: keys are sorted ascending first (the lock-ordering invariant,
// SPEC §1.6: "ingest_dedup (by dedup_key)"), then
// `INSERT ... ON CONFLICT DO NOTHING RETURNING dedup_key` reports exactly
// the keys not already in the ledger. Duplicate keys within dedupKeys
// collapse to a single ledger row (Postgres's ON CONFLICT DO NOTHING has no
// "cannot affect row a second time" restriction, unlike DO UPDATE), which is
// exactly the semantics WriteBatch wants for a batch containing the same
// event delivered twice.
//
// Returns the set of keys that survived the gate (i.e. are new).
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
