// Package postgres — rebuild.go implements store.Maintenance.RebuildProjections
// (SPEC §1.6, §2.4, §3.8, P3-10): `argusd rebuild-projections` replays every
// event in (ts, seq) order into the four SPEC §1.6 projection tables
// (sessions, turns, tool_calls, subagents) — truncated first — so a
// schema/mapping change can be re-derived from the immutable events table
// without re-ingesting anything.
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

	"github.com/YohannHommet/argus/server/internal/model"
)

// rebuildPageSize is how many events RebuildProjections folds/upserts per
// committed transaction (see this file's package doc on chunked replay).
const rebuildPageSize = 1000

// rebuildJobName is this job's job_state.job key (SPEC §2.4's DDL comment:
// "'rollup'|'retention'|'partitions'|'sweep'|'rebuild'").
const rebuildJobName = "rebuild"

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

// fetchEventPage reads up to rebuildPageSize events strictly after
// (afterTS, afterSeq) in (ts, seq) order — the global replay order SPEC
// §1.6 requires — reusing read_events.go's eventColumnsFull/scanEvent so
// this file does not hand-roll a second 34-column scan.
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

// truncateProjections empties the four SPEC §1.6 projection tables (SPEC
// §1.6: "Full rebuild for schema/mapping changes ... replays events in
// (ts, seq) order"). All four in one statement: Postgres allows a single
// TRUNCATE to name tables on both sides of a foreign key
// (tool_calls/subagents/turns all REFERENCE sessions), so no CASCADE or
// per-table ordering is needed. RESTART IDENTITY is a no-op here (none of
// the four tables has a serial/identity column) but is included for
// clarity that this is a full reset, matching the "truncate" language in
// this ticket's AC.
func truncateProjections(ctx context.Context, pool interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}) error {
	if _, err := pool.Exec(ctx, `TRUNCATE tool_calls, subagents, turns, sessions RESTART IDENTITY`); err != nil {
		return fmt.Errorf("postgres: rebuild projections: truncating projections: %w", err)
	}
	return nil
}

// replayPage folds and upserts one page of events into the four projection
// tables inside its own transaction — exactly write.go's WriteBatch
// projection half (see this file's package doc), minus insertIngestDedup/
// insertEvents (events already exist and are never rebuilt) and
// markRollupDirty (rollups are not one of the four projection tables).
func (s *Store) replayPage(ctx context.Context, events []model.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: rebuild projections: begin page: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	sessionAggs := foldSessionEvents(events)
	turnAggs := foldTurnEvents(events)

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

// RebuildProjections implements store.Maintenance (SPEC §1.6, §2.4, §3.8,
// P3-10). See this file's package doc for the fold/upsert reuse rationale
// and the resumable-watermark design.
func (s *Store) RebuildProjections(ctx context.Context, fromTS time.Time) error {
	wm, err := getRebuildWatermark(ctx, s.pool)
	if err != nil {
		return err
	}

	cursorTS, cursorSeq := fromTS.UTC(), int64(0)
	if wm.HasWatermark {
		cursorTS, cursorSeq = wm.TS, wm.Seq
		slog.Default().InfoContext(ctx, "postgres: rebuild projections: resuming from watermark",
			"watermark_ts", cursorTS, "watermark_seq", cursorSeq)
	} else {
		if err := truncateProjections(ctx, s.pool); err != nil {
			return err
		}
		slog.Default().InfoContext(ctx, "postgres: rebuild projections: starting fresh rebuild", "from_ts", cursorTS)
	}

	var total int64
	for {
		events, err := fetchEventPage(ctx, s.pool, cursorTS, cursorSeq, rebuildPageSize)
		if err != nil {
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO job_state (job, last_error, last_run_at) VALUES ($1, $2, now())
				ON CONFLICT (job) DO UPDATE SET last_error = EXCLUDED.last_error, last_run_at = EXCLUDED.last_run_at`,
				rebuildJobName, err.Error())
			return err
		}
		if len(events) == 0 {
			break
		}

		if err := s.replayPage(ctx, events); err != nil {
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO job_state (job, last_error, last_run_at) VALUES ($1, $2, now())
				ON CONFLICT (job) DO UPDATE SET last_error = EXCLUDED.last_error, last_run_at = EXCLUDED.last_run_at`,
				rebuildJobName, err.Error())
			return err
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
		return fmt.Errorf("postgres: rebuild projections: clearing watermark: %w", err)
	}

	slog.Default().InfoContext(ctx, "postgres: rebuild projections: complete", "replayed_total", total)
	return nil
}
