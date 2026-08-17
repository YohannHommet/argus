-- rollups.sql holds the fixed, single-statement queries P3-05's rollup job
-- (internal/store/postgres/rollups.go) drives through sqlc (SPEC §3.3,
-- §2.4). Every statement here is a single round trip issued against the
-- job's one transaction (gen.New(tx)), never a *pgx.Batch — the job has no
-- per-row loop that would benefit from batching the way WriteBatch's
-- per-event projections do (write.go's package doc), so there is nothing
-- sqlc can't express here the way there was for that file.
--
-- M8 fix (pre-Phase-4 audit wave, ticket W3): every date_trunc(...) call in
-- this file pins its zone explicitly to 'UTC' via the third argument. Go
-- builds every bucket key in hard UTC (dirty.go's hourBucket,
-- rollups.go's daysOf, read_analytics.go's dayBucket); date_trunc's
-- two-argument form evaluates in the connection's TimeZone GUC instead, so
-- without the explicit zone a non-UTC session (pool.go's NewPool now also
-- pins TimeZone=UTC as belt-and-braces) truncates to the wrong instant —
-- silently for a whole-hour-offset zone at the 'day' grain (every
-- InsertRollupDailyFromHourly/DeleteRollupDaily WHERE stops matching any
-- Go-built day key, so rollup_daily goes permanently empty) and even at the
-- 'hour' grain for a half-hour-offset zone (Asia/Kolkata's UTC+5:30 shifts
-- every hour bucket by 30 minutes). See rollups_test.go's
-- TestRunRollups_NonUTCSessionTimeZone{Paris,Kolkata} for the reproduction.
--
-- M9 fix (same wave): AggregateEventRollup, AggregateToolCallRollup and
-- FetchMetricRowsForRollup each gained a plain `ts >= from_ts AND ts <
-- to_ts` range predicate over the claimed bucket set's own timestamp column
-- (events.ts / tool_calls.started_at / metric_samples.ts), computed by the
-- Go caller as [min(buckets), max(buckets)+1h). Unlike the pre-existing
-- `date_trunc(...) = ANY(buckets)` filter, this predicate is sargable
-- against the RANGE-partitioned tables' own partition key, so the planner
-- can prune every partition outside the claimed window instead of Append-
-- scanning all of retention on every 60s tick (verified via EXPLAIN,
-- explain_test.go's TestAggregateEventRollup_PrunesPartitions). The range
-- is additive, not a replacement: the exact date_trunc equality still does
-- all the real bucket selection, so a non-contiguous claimed set (e.g.
-- hours 1 and 5 dirty, 2-4 clean) still excludes 2-4 correctly — the range
-- predicate only widens or narrows which partitions get opened, never which
-- rows within them match.

-- name: TryRollupLock :one
-- pg_try_advisory_xact_lock (NOT the blocking pg_advisory_lock migrate.go
-- uses): single-flights the whole job. Transaction-scoped, so it releases
-- itself automatically on COMMIT or ROLLBACK — no matching unlock call
-- needed, unlike migrate.go's session-scoped lock. Returns false immediately
-- if another rollup pass already holds it; the caller commits/rolls back an
-- otherwise-empty transaction and reports zero buckets, no error (SPEC
-- §2.4 step 1; ticket note: "a second concurrent invocation returns
-- immediately with zero buckets claimed and no error"). Key
-- 0x41_52_47_55_53_30_32 ("ARGUS02" packed into an int64) continues
-- migrate.go's migrationLockKey ("ARGUS01") numbering — that comment is
-- this codebase's only lock-key registry to date.
SELECT pg_try_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: ClaimDirtyBuckets :many
-- SPEC §2.4 step 2: DELETE … RETURNING claims up to max_buckets dirty
-- (bucket, source) pairs, oldest bucket first, so a backlog drains
-- front-to-back. Claimed rows are gone from rollup_dirty for good *unless*
-- this transaction rolls back, in which case Postgres's own MVCC undoes the
-- DELETE and the next run reclaims them (P3-05 AC: "a rolled-back job
-- leaves the dirty rows intact").
DELETE FROM rollup_dirty
WHERE (bucket, source) IN (
    SELECT bucket, source FROM rollup_dirty ORDER BY bucket LIMIT sqlc.arg(max_buckets)
)
RETURNING bucket, source;

-- name: DeleteRollupHourly :exec
-- First half of the SPEC §2.4 step 4 "full recompute": every existing
-- rollup_hourly row for these buckets/source is discarded before the fresh
-- aggregate (AggregateEventRollup/the metric pass) is inserted, so a group
-- key that no longer has any contributing data (e.g. the late-project M4
-- case's now-empty project='' row) actually disappears instead of going
-- stale — a plain INSERT … ON CONFLICT DO UPDATE can only ever touch keys
-- the fresh aggregate still produces, never a key it stopped producing.
DELETE FROM rollup_hourly
WHERE bucket = ANY(sqlc.arg(buckets)::timestamptz[]) AND source = sqlc.arg(source_kind)::text;

-- name: AggregateEventRollup :many
-- SPEC §2.4 step 4's source='event' pass: one row per (bucket, project,
-- vendor, model) group actually present in `events` for the claimed
-- buckets, joined to `sessions` for project (COALESCE to '' — sessions.
-- project is nullable until a SessionStart hook lands, and rollup_hourly.
-- project is NOT NULL DEFAULT ''). model is COALESCE'd to '' too: only
-- llm.request rows carry a model, so every non-model-attributable counter
-- (sessions_started, turns) naturally lands in the model='' group, and
-- token/cost counters land under the real model — exactly the split
-- rollup_hourly.model's doc comment describes, with no separate query
-- needed.
--
-- Counters are plain COUNT(*) FILTER (WHERE kind = …) over the raw
-- `events` table, not the deduplicated turns projection: SPEC §2.4
-- describes this pass as aggregating `events` directly, and it is an
-- accepted simplification that a turn independently reported over both a
-- hook and an OTel log event counts twice here where the turns projection
-- would fold it into one row (documented, not silently different from what
-- a reader would expect from "counts of events").
--
-- tool_calls/tool_rejects deliberately do NOT come from this query (P3-05
-- defect 1, SPEC §1.5.1/§1.5.2: `tool.pre` is produced only by the
-- PreToolUse hook, never by an OTel log event, so `count(*) FILTER (WHERE
-- kind = 'tool.pre')` silently reads 0 on any OTel-only deployment — a
-- deployment §4.1's meta endpoint explicitly supports via `hooks_seen:
-- false` — while sessions.tool_call_count, built from the tool_calls
-- projection, shows the real number. See AggregateToolCallRollup below,
-- which the Go caller merges into this query's (bucket, project, vendor,
-- model='') groups instead.
--
-- cost_reported_usd is SUM(cost_usd) WHERE cost_source = 'reported' (SPEC
-- §2.4's cost split verbatim). The four uncosted_* sums are the token
-- totals for llm.request rows with cost_usd IS NULL — Go-side
-- pricing.Estimate turns them into cost_estimated_usd per group; a group
-- with no uncosted tokens gets estimated cost 0 without ever calling
-- Estimate.
SELECT
    date_trunc('hour', e.ts, 'UTC')::timestamptz                 AS bucket,
    COALESCE(s.project, '')::text                                AS project,
    e.vendor::text                                                AS vendor,
    COALESCE(e.model, '')::text                                  AS model,
    count(*) FILTER (WHERE e.kind = 'session.start')::int         AS sessions_started,
    count(*) FILTER (WHERE e.kind = 'turn.start')::int            AS turns,
    count(*) FILTER (WHERE e.kind = 'llm.request')::int           AS api_requests,
    count(*) FILTER (WHERE e.kind = 'llm.error')::int             AS api_errors,
    COALESCE(sum(e.input_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint          AS input_tokens,
    COALESCE(sum(e.output_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint          AS output_tokens,
    COALESCE(sum(e.cache_read_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint      AS cache_read_tokens,
    COALESCE(sum(e.cache_creation_tokens) FILTER (WHERE e.kind = 'llm.request'), 0)::bigint  AS cache_creation_tokens,
    COALESCE(sum(e.cost_usd) FILTER (WHERE e.cost_source = 'reported'), 0)::numeric          AS cost_reported_usd,
    COALESCE(sum(e.input_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint          AS uncosted_input_tokens,
    COALESCE(sum(e.output_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint         AS uncosted_output_tokens,
    COALESCE(sum(e.cache_read_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint     AS uncosted_cache_read_tokens,
    COALESCE(sum(e.cache_creation_tokens) FILTER (WHERE e.kind = 'llm.request' AND e.cost_usd IS NULL), 0)::bigint AS uncosted_cache_creation_tokens
FROM events e
LEFT JOIN sessions s ON s.id = e.session_id
WHERE e.ts >= sqlc.arg(from_ts)::timestamptz AND e.ts < sqlc.arg(to_ts)::timestamptz
  AND date_trunc('hour', e.ts, 'UTC') = ANY(sqlc.arg(buckets)::timestamptz[])
GROUP BY 1, 2, 3, 4;

-- name: AggregateToolCallRollup :many
-- P3-05 defect 1's fix: rollup_hourly.tool_calls/tool_rejects, aggregated
-- from the `tool_calls` projection (SPEC §1.6) instead of raw `events`, so
-- the count is correct regardless of which surface (hook or OTel log)
-- reported each call — exactly one deduplicated, correlated row per real
-- tool call, with a `started_at` and a `decision`, already exists there
-- (SPEC §1.6's table). Counting DISTINCT tool_calls rows here, rather than
-- `events` rows carrying `tool.pre`/`tool.decision`, is what makes this
-- figure agree with sessions.tool_call_count/tool_reject_count (also built
-- from tool_calls, upsert_toolcall.go's RecomputeSessionToolCallCounts) —
-- and, unlike the old `tool.pre`-only count, non-zero on a hooks-disabled,
-- OTel-only deployment (`/api/v1/meta`'s `hooks_seen: false` case), where
-- tool.pre/PreToolUse never fires at all but tool_calls still gets a row
-- from tool.decision/tool.result.
--
-- Bucketed on date_trunc('hour', started_at), not the events pass's
-- date_trunc('hour', ts): a tool call's canonical timestamp is its own
-- started_at (tool_calls.started_at is NOT NULL — upsert_toolcall.go's
-- "started_at fallback" note guarantees it is always populated, falling
-- back to the earliest folded contribution's ts when no tool.pre/
-- tool.decision exists), not the ts of whichever event most recently
-- touched the row. This is allowed under SPEC §2.5's rule (which forbids
-- request-time aggregation over `events`, not the rollup job reading a
-- projection table): tool_calls is itself a table maintained transactionally
-- alongside `events` (SPEC §1.6), so this is exactly the same
-- "full per-bucket recompute of a table" shape AggregateEventRollup already
-- uses.
--
-- Joined to `sessions` for project/vendor (same COALESCE-to-'' reasoning as
-- AggregateEventRollup; tool_calls.session_id is FK-NOT-NULL so the LEFT
-- JOIN never actually needs the fallback, but it costs nothing to be
-- defensive the same way AggregateEventRollup is). Every counter here lands
-- in the model='' group (SPEC §2.4: "model='' for non-model-attributable
-- counters") because a tool call is never model-attributable — the Go
-- caller (recomputeEventBuckets, rollups.go) merges these rows additively
-- into AggregateEventRollup's (bucket, project, vendor, model='') groups
-- rather than issuing a second INSERT, since both aggregates can produce a
-- row for the very same primary key and rollup_hourly's delete-then-insert
-- recompute has no ON CONFLICT to merge two separate inserts with.
SELECT
    date_trunc('hour', tc.started_at, 'UTC')::timestamptz AS bucket,
    COALESCE(s.project, '')::text                  AS project,
    COALESCE(s.vendor, 'unknown')::text             AS vendor,
    count(*)::int                                   AS tool_calls,
    count(*) FILTER (WHERE tc.decision = 'reject')::int AS tool_rejects
FROM tool_calls tc
LEFT JOIN sessions s ON s.id = tc.session_id
WHERE tc.started_at >= sqlc.arg(from_ts)::timestamptz AND tc.started_at < sqlc.arg(to_ts)::timestamptz
  AND date_trunc('hour', tc.started_at, 'UTC') = ANY(sqlc.arg(buckets)::timestamptz[])
GROUP BY 1, 2, 3;

-- InsertEventRollupHourly and InsertMetricRollupHourly (the bulk multi-
-- column unnest inserts into rollup_hourly) are hand-written pgx SQL in
-- rollups.go instead of sqlc queries here, for the same reason write.go's
-- package doc gives for the sessions/turns/events/rollup_dirty upserts: a
-- multi-array `unnest($1::t1[], $2::t2[], …)` table function in a FROM
-- clause is exactly the shape sqlc v1.31.1 cannot resolve against this
-- codebase's shadow-Postgres analysis ("function unnest(unknown, unknown,
-- …) does not exist" — the per-argument casts that make it work as a bare
-- SQL statement do not survive into how sqlc prepares it). Every other
-- multi-column unnest bulk write in this codebase (insertMetricSamples,
-- upsertSessions, execToolCallUpsert, …) is hand-written for the identical
-- reason; this ticket follows the same split rather than fighting sqlc.

-- name: DeleteRollupDaily :exec
-- Same full-recompute-by-delete reasoning as DeleteRollupHourly, one day
-- level up (SPEC §2.4 step 5).
DELETE FROM rollup_daily
WHERE bucket = ANY(sqlc.arg(days)::timestamptz[]) AND source = sqlc.arg(source_kind)::text;

-- name: InsertRollupDailyFromHourly :exec
-- SPEC §2.4 step 5: rollup_daily is always derived from rollup_hourly, not
-- from events/metric_samples directly — every column sums identically
-- regardless of what fed the hourly row, so one statement recomputes both
-- sources' daily rollups (called once per source_kind by the caller, same
-- as the hourly pair).
INSERT INTO rollup_daily (
    bucket, project, vendor, model, source,
    sessions_started, turns, api_requests, api_errors, tool_calls, tool_rejects,
    input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
    cost_reported_usd, cost_estimated_usd,
    loc_added, loc_removed, active_seconds, commits, pull_requests,
    edit_decisions_accept, edit_decisions_reject
)
SELECT
    date_trunc('day', bucket, 'UTC'), project, vendor, model, source,
    sum(sessions_started)::int, sum(turns)::int, sum(api_requests)::int, sum(api_errors)::int,
    sum(tool_calls)::int, sum(tool_rejects)::int,
    sum(input_tokens)::bigint, sum(output_tokens)::bigint, sum(cache_read_tokens)::bigint, sum(cache_creation_tokens)::bigint,
    sum(cost_reported_usd)::numeric, sum(cost_estimated_usd)::numeric,
    sum(loc_added)::bigint, sum(loc_removed)::bigint, sum(active_seconds)::bigint,
    sum(commits)::int, sum(pull_requests)::int,
    sum(edit_decisions_accept)::int, sum(edit_decisions_reject)::int
FROM rollup_hourly
WHERE date_trunc('day', bucket, 'UTC') = ANY(sqlc.arg(days)::timestamptz[]) AND source = sqlc.arg(source_kind)::text
GROUP BY 1, 2, 3, 4, 5;

-- name: FetchMetricRowsForRollup :many
-- Every metric_samples row in the claimed metric buckets, joined to
-- sessions for project (same COALESCE-to-'' reasoning as
-- AggregateEventRollup) and with the three attrs this pass's Go-side
-- metric-name switch needs (type/decision/model — SPEC §1.8's per-metric
-- attribute table) pulled out of the jsonb column directly, so
-- internal/app's rollup job never unmarshals attrs itself. Ordered by
-- (series_hash, ts) so the caller can walk each series in chronological
-- order in one pass for the cumulative-diffing step (SPEC §1.8's
-- metric_series_state).
SELECT
    ms.ts, ms.series_hash, ms.name, ms.vendor,
    COALESCE(s.project, '')::text AS project,
    ms.value, ms.temporality, ms.dedup_key,
    COALESCE(ms.attrs->>'type', '')::text      AS attr_type,
    COALESCE(ms.attrs->>'decision', '')::text  AS attr_decision,
    COALESCE(ms.attrs->>'model', '')::text     AS attr_model
FROM metric_samples ms
LEFT JOIN sessions s ON s.id = ms.session_id
WHERE ms.ts >= sqlc.arg(from_ts)::timestamptz AND ms.ts < sqlc.arg(to_ts)::timestamptz
  AND date_trunc('hour', ms.ts, 'UTC') = ANY(sqlc.arg(buckets)::timestamptz[])
ORDER BY ms.series_hash, ms.ts;

-- FetchSeriesAnchors (the set-based per-series lookback) is hand-written
-- pgx SQL in rollups.go for the same multi-array-unnest reason as
-- InsertEventRollupHourly/InsertMetricRollupHourly above.

-- name: FetchSeriesState :many
-- The metric_series_state fallback FetchSeriesAnchors's caller uses when
-- the direct metric_samples lookback finds nothing (SPEC §1.8: state exists
-- precisely so a series' running cumulative value survives its raw samples'
-- partition being dropped by retention).
SELECT series_hash, last_ts, last_value FROM metric_series_state
WHERE series_hash = ANY(sqlc.arg(series_hash)::bytea[]);

-- UpdateMetricSampleDeltas and UpsertMetricSeriesState are hand-written pgx
-- SQL in rollups.go for the same multi-array-unnest reason as
-- InsertEventRollupHourly above. UpsertMetricSeriesState's ON CONFLICT DO
-- UPDATE ... WHERE metric_series_state.last_ts < EXCLUDED.last_ts guard is
-- what makes advancing every series' checkpoint monotonic: reprocessing an
-- older dirty bucket after a newer checkpoint is already recorded (e.g. a
-- late-arriving event re-dirtying a past hour while "current hour" keeps
-- advancing state forward every tick) can never move last_ts backwards —
-- SPEC §1.8's documented fallback for exactly that ordering ("a negative
-- diff is treated as a counter reset and the raw value is taken") already
-- covers the resulting mis-anchored diff safely, so this guard only
-- prevents the checkpoint itself from regressing, not the (rarer) diff
-- error that can still occur across a genuine out-of-order delivery.
