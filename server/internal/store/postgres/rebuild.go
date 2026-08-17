// Package postgres — rebuild.go implements store.Maintenance.RebuildProjections
// (SPEC §1.6, §2.4, §3.8, P3-10): `argusd rebuild-projections` replays every
// event in (ts, seq) order into the four SPEC §1.6 projection tables
// (sessions, turns, tool_calls, subagents) — truncated first — so a
// schema/mapping change can be re-derived from the immutable events table
// without re-ingesting anything.
//
// # M12 — --from-ts is a session filter, not a replay lower bound
//
// A first cut of this file truncated all four projection tables unconditionally
// and then replayed only events with ts >= fromTS. That destroys every
// projection row for a session whose LAST event is before fromTS (no replayed
// event ever recreates it) and, worse, silently corrupts any session that
// straddles fromTS (started before, still active at/after it): the row would
// be deleted by the truncate and then rebuilt from only its post-fromTS
// events, so aggregates seeded from the session's start (started_at, cwd,
// event_count, token/cost sums, …) come out wrong instead of missing —
// exactly the "rebuild produces identical rows" guarantee (SPEC §1.6) this
// file exists to uphold.
//
// The fix scopes both deletion AND replay by SESSION, not by event ts:
// affectedSessionIDs(fromTS) finds every session with at least one event at
// or after fromTS, the scoped delete removes only THOSE sessions' projection
// rows (turns/tool_calls/subagents cascade off `sessions(id) ON DELETE
// CASCADE`, 001_core.sql/003_projections.sql), and the replay then walks
// EVERY event those sessions ever produced — from each session's true start,
// not from fromTS — so no straddling session is ever rebuilt from a partial
// slice of its own history. A session with zero events at/after fromTS is
// left completely untouched, matching the "starting point" framing in
// runRebuildProjections's doc comment. fromTS.IsZero() (the CLI's default,
// "replay every event ever stored") is treated as the unscoped full-rebuild
// case and skips session filtering entirely, matching this file's original,
// already-tested full-truncate behaviour exactly (and avoiding an
// `session_id = ANY(...)` array the size of the whole sessions table for the
// common case).
//
// Consequence for resumption: job_state's watermark is only (ts, seq) —
// 004_rollups.sql leaves no room for "and here was the affected-session set"
// without a migration, which is out of this ticket's file-ownership scope
// (rebuild.go/rebuild_test.go/main.go only). affectedSessionIDs is therefore
// RECOMPUTED from fromTS on every call, including a resume, rather than
// cached: since it is a pure function of fromTS and the (frozen, by the
// ARGUS03 lock below) events table, a resumed call reproduces the exact same
// session set as the interrupted one PROVIDED the operator re-supplies the
// same --from-ts. This is a real, documented operational contract (see
// runRebuildProjections's flag help) rather than a silent hazard: passing a
// different --from-ts on a resume recomputes a different session set against
// an already-partially-truncated/replayed state and produces inconsistent
// results. Reported to the fix-wave lead as a residual limitation rather than
// solved outright — closing it properly needs a job_state column to persist
// the original fromTS, which is a migration outside this ticket's scope.
//
// # M12 — refusing a dangerous fromTS
//
// Even with session-scoped deletion, a --from-ts older than the oldest
// surviving `events` partition is worth refusing by default: it means the
// operator is asking to rebuild sessions whose EARLIEST events may already be
// gone (dropped by retention, SPEC §2.4), so the "full session history"
// replay this file now performs can only reconstruct what raw retention left
// behind — a session's rebuilt aggregates would then silently under-count
// relative to what they held before the rebuild. RebuildProjectionsForce
// refuses this case unless force=true, and always logs the row counts about
// to be deleted before touching anything (SPEC audit finding M12).
//
// # M13 — a session-scoped advisory lock (ARGUS03) against concurrent writers
//
// Nothing serialises a rebuild against a running `serve`: replayPage reuses
// WriteBatch's additive upserts (event_count = sessions.event_count +
// EXCLUDED.event_count, upsert_session.go:445), so an event ingested after
// this file's truncate/delete step and before the replay cursor passes it
// gets counted twice. rebuild.go owns only this side of the fix: it takes
// pg_try_advisory_lock(ARGUS03) for the whole rebuild and refuses loudly if
// it cannot, mirroring migrate.go's ARGUS01/rollups.go's ARGUS02 numbering.
// write.go (owned by another ticket) does NOT yet take ARGUS03 in shared
// mode, so today this only protects a rebuild against a CONCURRENT REBUILD —
// it does not yet stop `serve`'s ingest path from double-counting into a
// running rebuild. That follow-up is reported verbatim to the fix-wave lead.
// Until it lands, an operator MUST stop `serve` before running
// rebuild-projections; this file cannot enforce that on its own without
// write.go's cooperation.
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

// fetchEventPageForSessions is fetchEventPage's session-scoped sibling (M12):
// it reads up to rebuildPageSize events strictly after (afterTS, afterSeq),
// restricted to sessionIDs, in the same global (ts, seq) order. Deliberately
// NO lower bound on ts beyond the (afterTS, afterSeq) cursor — the whole
// point of the M12 fix is that a scoped rebuild replays each affected
// session's events from that session's true start, not from fromTS, so a
// straddling session's pre-fromTS events are included exactly like its
// post-fromTS ones (see package doc).
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

// acquireRebuildLock takes pg_try_advisory_lock(rebuildLockKey) on a
// dedicated connection and returns a release func, or an error if the lock
// is already held (M13; see package doc — today this only rejects a
// CONCURRENT REBUILD, since write.go does not yet take ARGUS03 in shared
// mode). Non-blocking (pg_try_advisory_lock, not pg_advisory_lock) so a
// second invocation fails loudly and immediately instead of queuing behind
// the first and silently serialising two rebuilds that might disagree about
// fromTS.
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

// truncateProjections empties the four SPEC §1.6 projection tables in full —
// the unscoped path, used when fromTS.IsZero() (see package doc). All four in
// one statement: Postgres allows a single TRUNCATE to name tables on both
// sides of a foreign key (tool_calls/subagents/turns all REFERENCE sessions),
// so no CASCADE or per-table ordering is needed. RESTART IDENTITY is a no-op
// here (none of the four tables has a serial/identity column) but is
// included for clarity that this is a full reset, matching the "truncate"
// language in this ticket's AC.
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

// affectedSessionIDs finds every session with at least one event at or after
// fromTS — the session-scoping predicate this file's M12 fix uses in place of
// a literal "replay events >= fromTS" (see package doc for why: it is the
// only way to rebuild a straddling session from its true start rather than a
// partial slice).
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

// M12's "oldest surviving events partition" guard reuses read_sessions.go's
// oldestEventsPartitionStart (same package, already implements exactly this
// pg_inherits lookup for SPEC's session-timeline-expiry rule) rather than
// duplicating it a second time — unlike retention.go's listPartitionRanges,
// which duplicates partitions.go's query because no ready-made helper
// already existed with the right shape.

// scopedDeleteSessions counts (for the RebuildDestructionReport) and then
// deletes exactly the given sessions' projection rows, inside one
// transaction so the report matches what is actually destroyed even under
// concurrent activity. turns/tool_calls/subagents cascade off
// `sessions(id) ON DELETE CASCADE` (001_core.sql, 003_projections.sql), so
// deleting from `sessions` alone is sufficient — the same single-statement-
// covers-the-FK-graph property truncateProjections's doc comment already
// relies on for the unscoped path. A nil/empty sessionIDs is a no-op
// (nothing to delete, nothing destroyed).
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
// P3-10). It is the safe, force=false entry point: a --from-ts predating the
// oldest surviving events partition is refused rather than silently
// producing an incomplete rebuild (M12). See RebuildProjectionsForce for the
// operator-facing variant with the --force escape hatch and the
// RebuildDestructionReport preview, and this file's package doc for the
// session-scoping/advisory-lock design both share.
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
