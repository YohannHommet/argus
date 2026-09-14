// Package postgres — rebuild.go implements store.Maintenance.RebuildProjections
// (SPEC §1.6, §2.4, §3.8, P3-10): replays events into projection tables.
// M12: --from-ts filters by session, not event ts (session-scoped deletion+replay).
// M12: refuses fromTS older than oldest partition (would silently under-count).
// M13: advisory lock ARGUS03 prevents double-counting from concurrent writes.
//
// # Why this reuses write.go's fold/upsert functions verbatim
//
// Replaying is exactly WriteBatch's projection half (foldSessionEvents ->
// upsertSessions -> foldTurnEvents -> upsertTurns -> upsertToolCalls ->
// upsertSubagents -> correctSessionTurnCounts) applied to events already in
// the table, skipping only what WriteBatch does that a rebuild must not
// repeat: insertIngestDedup/insertEvents (events are never rebuilt, SPEC
// §1.6: "events.id stays uuidv7() — events are never rebuilt") and
// markRollupDirty (rollups are not one of the four projection tables and
// are never touched by a rebuild — SPEC §2.4 already draws this same line
// for retention: "rollups and projections are never deleted"). Calling the
// exact same unexported functions WriteBatch calls, rather than a parallel
// reimplementation, is what makes "rebuild produces identical rows"
// (SPEC §1.6, this ticket's checksum AC) true by construction instead of by
// coincidence: upsert_toolcall.go's own doc comment (lead note 5) names this
// file's global (ts, seq) single pass as exactly the scenario its
// ordinal-seeding scheme is designed for.
//
// # Chunked replay and job_state's resumable watermark
//
// One page (rebuildPageSize events) is folded and upserted per transaction,
// each commit advancing job_state's (job='rebuild') watermark to the last
// replayed event's (ts, seq). This is safe exactly because upsertToolCalls
// re-seeds its keyless ordinal counter from CountKeylessToolCalls (a fresh
// query against the already-committed tool_calls rows) at the start of every
// call, and every page is processed in strictly ascending (ts, seq) order —
// so splitting the global replay into committed pages reproduces the same
// per-key ordinal sequence a single unbounded pass would, unlike live
// ingestion's out-of-order batch arrival (which upsert_toolcall.go's doc
// explicitly calls out as the one case this scheme does NOT handle
// perfectly). A crash or process restart mid-rebuild therefore loses at most
// one partial page's work, and RebuildProjections resumes from job_state's
// watermark instead of re-truncating: presence of a non-NULL watermark
// means "an incomplete rebuild already truncated the four tables and
// replayed everything through this (ts, seq)", so a resumed call skips the
// TRUNCATE and continues from there, ignoring the fromTS argument (which
// only seeds a *fresh* rebuild's starting point). Completion clears the
// watermark (sets it back to NULL), so the next call with a fresh fromTS
// starts a new rebuild rather than being mistaken for a resume.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/YohannHommet/argus/server/internal/model"
)

// rebuildPageSize is how many events RebuildProjections folds/upserts per
// committed transaction (see this file's package doc on chunked replay).
const rebuildPageSize = 1000

// rebuildJobName is this job's job_state.job key (SPEC §2.4's DDL comment:
// "'rollup'|'retention'|'partitions'|'sweep'|'rebuild'").
const rebuildJobName = "rebuild"

// rebuildLockKey is the pg_try_advisory_lock key RebuildProjections holds for
// the whole rebuild (M13; see package doc). Continues migrate.go's ARGUS01/
// rollups.go's ARGUS02 numbering: ARGUS03 is this ticket's assigned id (the
// fix-wave lead reserved ARGUS04 for a concurrent partitions ticket).
const rebuildLockKey int64 = 0x41_52_47_55_53_30_33 // "ARGUS03" packed into an int64

// RebuildDestructionReport is the row-count preview RebuildProjectionsForce
// logs and returns before deleting any projection rows (M12: "print the row
// counts about to be destroyed"). All four counts are always populated,
// scoped to whichever sessions are about to be rebuilt (every session, for an
// unscoped fromTS.IsZero() full rebuild; only the sessions affectedSessionIDs
// finds, for a scoped --from-ts rebuild).
type RebuildDestructionReport struct {
	Sessions, Turns, ToolCalls, Subagents int64
}

// rebuildWatermark is job_state's (watermark, watermark_ts) pair for
// job='rebuild', decoded into Go types. HasWatermark is false both when no
// row exists yet and when the row exists with both columns NULL (the
// "completed, nothing in progress" state a prior rebuild leaves behind) —
// either way RebuildProjections must treat the next call as a fresh start.
type rebuildWatermark struct {
	TS           time.Time
	Seq          int64
	HasWatermark bool
}

// getRebuildWatermark reads job_state's current resumption point for the
// rebuild job, if any.
func getRebuildWatermark(ctx context.Context, pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) (rebuildWatermark, error) {
	var seq *int64
	var ts *time.Time
	err := pool.QueryRow(ctx, `SELECT watermark, watermark_ts FROM job_state WHERE job = $1`, rebuildJobName).Scan(&seq, &ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return rebuildWatermark{}, nil
	}
	if err != nil {
		return rebuildWatermark{}, fmt.Errorf("postgres: rebuild projections: reading watermark: %w", err)
	}
	if seq == nil || ts == nil {
		return rebuildWatermark{}, nil
	}
	return rebuildWatermark{TS: *ts, Seq: *seq, HasWatermark: true}, nil
}

// setRebuildWatermark records how far RebuildProjections has replayed,
// inside the same transaction as the page it just committed, so the mark
// and the projection rows it describes become durable atomically together
// (mirroring dirty.go's rollup_dirty same-transaction guarantee).
func setRebuildWatermark(ctx context.Context, tx pgx.Tx, ts time.Time, seq int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO job_state (job, watermark, watermark_ts, last_run_at, last_error)
		VALUES ($1, $2, $3, now(), NULL)
		ON CONFLICT (job) DO UPDATE SET
			watermark = EXCLUDED.watermark, watermark_ts = EXCLUDED.watermark_ts,
			last_run_at = EXCLUDED.last_run_at, last_error = NULL`,
		rebuildJobName, seq, ts)
	if err != nil {
		return fmt.Errorf("postgres: rebuild projections: recording watermark: %w", err)
	}
	return nil
}

// fetchEventPage reads the next page of events in (ts, seq) order, reusing eventColumnsFull/scanEvent.
func fetchEventPage(ctx context.Context, pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, afterTS time.Time, afterSeq int64, limit int) ([]model.Event, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+eventColumnsFull+`
		FROM events e
		WHERE (e.ts, e.seq) > ($1, $2)
		ORDER BY e.ts, e.seq
		LIMIT $3`, afterTS, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: fetching page: %w", err)
	}
	defer rows.Close()

	events := make([]model.Event, 0, limit)
	for rows.Next() {
		e, err := scanEvent(rows, true)
		if err != nil {
			return nil, fmt.Errorf("postgres: rebuild projections: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: fetching page: %w", err)
	}
	return events, nil
}

// fetchEventPageForSessions is the session-scoped variant: reads events after (afterTS, afterSeq) restricted to sessionIDs. No ts lower bound (M12: scoped rebuild replays each session from its true start, not fromTS).
func fetchEventPageForSessions(ctx context.Context, pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, sessionIDs []string, afterTS time.Time, afterSeq int64, limit int) ([]model.Event, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+eventColumnsFull+`
		FROM events e
		WHERE e.session_id = ANY($1) AND (e.ts, e.seq) > ($2, $3)
		ORDER BY e.ts, e.seq
		LIMIT $4`, sessionIDs, afterTS, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: fetching scoped page: %w", err)
	}
	defer rows.Close()

	events := make([]model.Event, 0, limit)
	for rows.Next() {
		e, err := scanEvent(rows, true)
		if err != nil {
			return nil, fmt.Errorf("postgres: rebuild projections: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: fetching scoped page: %w", err)
	}
	return events, nil
}

// acquireRebuildLock takes pg_try_advisory_lock(rebuildLockKey) non-blocking; returns release func or error if locked (M13).
func acquireRebuildLock(ctx context.Context, pool *pgxpool.Pool) (release func(), err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: acquiring lock connection: %w", err)
	}

	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, rebuildLockKey).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("postgres: rebuild projections: acquiring advisory lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, errors.New("postgres: rebuild projections: another rebuild-projections already holds the ARGUS03 advisory lock (or, once write.go's shared-mode follow-up lands, `serve` is running) — stop it before retrying")
	}

	return func() {
		// Unlock on the same backend connection that took the lock
		// (advisory locks are session-scoped), with a context that survives
		// the caller's cancellation so a timed-out rebuild still releases the
		// lock instead of stranding it for the connection's lifetime
		// (mirrors migrate.go's migrationLockKey release).
		unlockCtx := context.WithoutCancel(ctx)
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, rebuildLockKey)
		conn.Release()
	}, nil
}

// truncateProjections empties the four projection tables in one statement (unscoped fromTS.IsZero() path).
func truncateProjections(ctx context.Context, pool interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}) error {
	if _, err := pool.Exec(ctx, `TRUNCATE tool_calls, subagents, turns, sessions RESTART IDENTITY`); err != nil {
		return fmt.Errorf("postgres: rebuild projections: truncating projections: %w", err)
	}
	return nil
}

// countProjectionRows returns the RebuildDestructionReport for the unscoped
// (full-table) truncate path.
func countProjectionRows(ctx context.Context, pool *pgxpool.Pool) (RebuildDestructionReport, error) {
	var r RebuildDestructionReport
	err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sessions),
		(SELECT count(*) FROM turns),
		(SELECT count(*) FROM tool_calls),
		(SELECT count(*) FROM subagents)`).Scan(&r.Sessions, &r.Turns, &r.ToolCalls, &r.Subagents)
	if err != nil {
		return RebuildDestructionReport{}, fmt.Errorf("postgres: rebuild projections: counting projection rows: %w", err)
	}
	return r, nil
}

// affectedSessionIDs finds sessions with events at or after fromTS (M12: scope predicate to avoid partial replays).
func affectedSessionIDs(ctx context.Context, pool *pgxpool.Pool, fromTS time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT session_id FROM events WHERE ts >= $1`, fromTS)
	if err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: finding affected sessions: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: rebuild projections: scanning affected sessions: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: rebuild projections: finding affected sessions: %w", err)
	}
	return ids, nil
}

// scopedDeleteSessions counts and deletes the given sessions' projection rows atomically; cascade handles FK graph.
func scopedDeleteSessions(ctx context.Context, pool *pgxpool.Pool, sessionIDs []string) (RebuildDestructionReport, error) {
	if len(sessionIDs) == 0 {
		return RebuildDestructionReport{}, nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return RebuildDestructionReport{}, fmt.Errorf("postgres: rebuild projections: begin scoped delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	var r RebuildDestructionReport
	err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sessions WHERE id = ANY($1)),
		(SELECT count(*) FROM turns WHERE session_id = ANY($1)),
		(SELECT count(*) FROM tool_calls WHERE session_id = ANY($1)),
		(SELECT count(*) FROM subagents WHERE session_id = ANY($1))`, sessionIDs).
		Scan(&r.Sessions, &r.Turns, &r.ToolCalls, &r.Subagents)
	if err != nil {
		return RebuildDestructionReport{}, fmt.Errorf("postgres: rebuild projections: counting scoped rows: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = ANY($1)`, sessionIDs); err != nil {
		return RebuildDestructionReport{}, fmt.Errorf("postgres: rebuild projections: deleting scoped sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RebuildDestructionReport{}, fmt.Errorf("postgres: rebuild projections: commit scoped delete: %w", err)
	}
	return r, nil
}

// replayPage folds and upserts one event page (WriteBatch's projection half without insert/rollup steps).
func (s *Store) replayPage(ctx context.Context, events []model.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: rebuild projections: begin page: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// D-30 (docs/review/phase-4-gauntlet.md): a rebuild must reproduce
	// exactly what WriteBatch would have produced (this file's package doc:
	// "the 'rebuild produces identical rows' guarantee"), so it needs the
	// same conditional price load WriteBatch's own D-30 fix uses
	// (write.go's loadPricesIfNeeded) — skipping it here would silently
	// zero out cost_estimated_usd on every rebuild, reintroducing the exact
	// defect this ticket fixes.
	prices, err := loadPricesIfNeeded(ctx, tx, events)
	if err != nil {
		return err
	}
	sessionAggs := foldSessionEvents(events, prices)
	turnAggs := foldTurnEvents(events, prices)

	if _, err := upsertSessions(ctx, tx, sessionAggs); err != nil {
		return err
	}
	if err := upsertTurns(ctx, tx, turnAggs); err != nil {
		return err
	}
	if _, err := upsertToolCalls(ctx, tx, events); err != nil {
		return err
	}
	if err := upsertSubagents(ctx, tx, events); err != nil {
		return err
	}
	if err := correctSessionTurnCounts(ctx, tx, sessionAggs); err != nil {
		return err
	}

	last := events[len(events)-1]
	if err := setRebuildWatermark(ctx, tx, last.TS, last.Seq); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: rebuild projections: commit page: %w", err)
	}
	return nil
}

// RebuildProjections implements store.Maintenance (SPEC §1.6, §2.4, §3.8, P3-10); safe entry point refusing dangerous fromTS (M12).
func (s *Store) RebuildProjections(ctx context.Context, fromTS time.Time) error {
	_, err := s.RebuildProjectionsForce(ctx, fromTS, false)
	return err
}

// RebuildProjectionsForce is a Store-only method outside store.Maintenance
// (the same "extra concrete method beyond the interface" shape as
// retention.go's ApplyRetentionPrecise) that adds the M12 --force flag and
// destruction-count report the `rebuild-projections` CLI subcommand needs.
// See this file's package doc for the full M12/M13 design.
func (s *Store) RebuildProjectionsForce(ctx context.Context, fromTS time.Time, force bool) (RebuildDestructionReport, error) {
	release, err := acquireRebuildLock(ctx, s.pool)
	if err != nil {
		return RebuildDestructionReport{}, err
	}
	defer release()

	wm, err := getRebuildWatermark(ctx, s.pool)
	if err != nil {
		return RebuildDestructionReport{}, err
	}

	fromTS = fromTS.UTC()
	scoped := !fromTS.IsZero()

	var sessionIDs []string
	var report RebuildDestructionReport

	if scoped {
		sessionIDs, err = affectedSessionIDs(ctx, s.pool, fromTS)
		if err != nil {
			return RebuildDestructionReport{}, err
		}
	}

	cursorTS, cursorSeq := time.Time{}, int64(0)
	if wm.HasWatermark {
		cursorTS, cursorSeq = wm.TS, wm.Seq
		slog.Default().InfoContext(ctx, "postgres: rebuild projections: resuming from watermark",
			"watermark_ts", cursorTS, "watermark_seq", cursorSeq, "scoped", scoped, "from_ts", fromTS)
	} else {
		if scoped && !force {
			oldest, ok, oldestErr := oldestEventsPartitionStart(ctx, s.pool)
			if oldestErr != nil {
				return RebuildDestructionReport{}, oldestErr
			}
			if ok && fromTS.Before(oldest) {
				return RebuildDestructionReport{}, fmt.Errorf(
					"postgres: rebuild projections: --from-ts %s predates the oldest surviving events partition (%s) — raw events before that point may already be gone (SPEC §2.4 retention), so affected sessions could be rebuilt from an incomplete history; pass --force to proceed anyway",
					fromTS.Format(time.RFC3339), oldest.Format(time.RFC3339))
			}
		}

		if scoped {
			report, err = scopedDeleteSessions(ctx, s.pool, sessionIDs)
			if err != nil {
				return RebuildDestructionReport{}, err
			}
			slog.Default().InfoContext(ctx, "postgres: rebuild projections: starting scoped rebuild",
				"from_ts", fromTS, "affected_sessions", len(sessionIDs),
				"destroying_sessions", report.Sessions, "destroying_turns", report.Turns,
				"destroying_tool_calls", report.ToolCalls, "destroying_subagents", report.Subagents)
		} else {
			report, err = countProjectionRows(ctx, s.pool)
			if err != nil {
				return RebuildDestructionReport{}, err
			}
			if err := truncateProjections(ctx, s.pool); err != nil {
				return RebuildDestructionReport{}, err
			}
			slog.Default().InfoContext(ctx, "postgres: rebuild projections: starting fresh full rebuild",
				"destroying_sessions", report.Sessions, "destroying_turns", report.Turns,
				"destroying_tool_calls", report.ToolCalls, "destroying_subagents", report.Subagents)
		}
	}

	var total int64
	for {
		var events []model.Event
		var err error
		if scoped {
			events, err = fetchEventPageForSessions(ctx, s.pool, sessionIDs, cursorTS, cursorSeq, rebuildPageSize)
		} else {
			events, err = fetchEventPage(ctx, s.pool, cursorTS, cursorSeq, rebuildPageSize)
		}
		if err != nil {
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO job_state (job, last_error, last_run_at) VALUES ($1, $2, now())
				ON CONFLICT (job) DO UPDATE SET last_error = EXCLUDED.last_error, last_run_at = EXCLUDED.last_run_at`,
				rebuildJobName, err.Error())
			return RebuildDestructionReport{}, err
		}
		if len(events) == 0 {
			break
		}

		if err := s.replayPage(ctx, events); err != nil {
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO job_state (job, last_error, last_run_at) VALUES ($1, $2, now())
				ON CONFLICT (job) DO UPDATE SET last_error = EXCLUDED.last_error, last_run_at = EXCLUDED.last_run_at`,
				rebuildJobName, err.Error())
			return RebuildDestructionReport{}, err
		}

		last := events[len(events)-1]
		cursorTS, cursorSeq = last.TS, last.Seq
		total += int64(len(events))
		slog.Default().InfoContext(ctx, "postgres: rebuild projections: progress",
			"replayed", total, "watermark_ts", cursorTS, "watermark_seq", cursorSeq)

		if len(events) < rebuildPageSize {
			break
		}
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO job_state (job, watermark, watermark_ts, last_run_at, last_error)
		VALUES ($1, NULL, NULL, now(), NULL)
		ON CONFLICT (job) DO UPDATE SET
			watermark = NULL, watermark_ts = NULL, last_run_at = EXCLUDED.last_run_at, last_error = NULL`,
		rebuildJobName); err != nil {
		return RebuildDestructionReport{}, fmt.Errorf("postgres: rebuild projections: clearing watermark: %w", err)
	}

	slog.Default().InfoContext(ctx, "postgres: rebuild projections: complete", "replayed_total", total)
	return report, nil
}
