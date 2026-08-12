-- write.sql holds the fixed, single-statement queries P2-06's WriteBatch/
-- WriteMetrics drive through sqlc (SPEC §3.3: "sqlc for everything fixed").
-- The batch-shaped, unnest-driven statements that build the sessions/turns/
-- events/rollup_dirty projections are hand-written pgx.Batch SQL in write.go
-- and friends instead: sqlc generates functions that call db.Query/QueryRow
-- directly, which is not how *pgx.Batch queues statements, so nothing would
-- be gained by routing them through sqlc (see write.go's package doc for the
-- full reasoning).

-- name: InsertIngestDedup :many
-- The SPEC §1.7 rule 2 idempotency gate, shared by WriteBatch (event/hook/
-- metric dedup keys) and WriteMetrics: only keys not already in the ledger
-- come back, so the caller inserts exactly the survivors. $1 must be sorted
-- ascending by the caller (the lock-ordering invariant, SPEC §1.6).
INSERT INTO ingest_dedup (dedup_key)
SELECT unnest(sqlc.arg(dedup_keys)::text[])
ON CONFLICT (dedup_key) DO NOTHING
RETURNING dedup_key;

-- name: SweepAbandoned :execrows
-- Implements store.Maintenance.SweepAbandoned (SPEC §1.7's "stored column"
-- status rule): a session idle past the cutoff moves active/unknown ->
-- abandoned. A later session.end still moves it back to ended (handled by
-- the ordinary session upsert, not here). Rides sessions_sweep_idx (SPEC
-- §2.1).
UPDATE sessions
SET status = 'abandoned'
WHERE status IN ('active', 'unknown')
  AND ended_at IS NULL
  AND last_event_at < sqlc.arg(cutoff)::timestamptz;
