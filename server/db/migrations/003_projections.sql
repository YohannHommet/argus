-- 003_projections.sql
--
-- Reproduces docs/SPEC.md §2.3 verbatim: tool_calls, subagents,
-- metric_samples (partitioned like events, §2.2), metric_series_state.
--
-- Also creates `rollup_dirty` here rather than in 004_rollups.sql, where
-- SPEC §2.4 lists it: P2-06 (the ingest write path, this phase) writes to
-- `rollup_dirty` inside the same transaction as its events/projections
-- insert, and its AC asserts rows land in it, so the table must exist by
-- the end of Phase 2. The REST of §2.4 (rollup_hourly, rollup_daily,
-- model_prices, job_state) is owned by ticket P3-04 and belongs in
-- 004_rollups.sql — that migration must NOT re-create rollup_dirty.
--
-- §0's rule applies: no CHECK constraint, enum, or domain on any
-- vendor-supplied vocabulary column (tool_source, decision, decision_source,
-- permission_mode, agent_type, status, error_type, correlation-adjacent
-- fields, ...). The inline comments below are documentation only.

-- +goose Up
CREATE TABLE tool_calls (
    id                uuid PRIMARY KEY,          -- deterministic UUIDv5, computed in Go (SPEC §1.6)
    session_id        text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    prompt_id         text,
    tool_use_id       text,
    tool_name         text NOT NULL,
    tool_source       text,
    agent_id          text,                      -- hook-sourced only
    decision          text,
    decision_source   text,
    permission_mode   text,
    started_at        timestamptz NOT NULL,
    decided_at        timestamptz,
    ended_at          timestamptz,
    duration_ms       integer,
    wait_ms           integer,     -- decided_at - started_at: human/permission latency
    success           boolean,
    error_type        text,
    file_path         text,
    input_size_bytes  integer,     -- from attrs->>'tool_input_size_bytes' (verified present)
    result_size_bytes integer,     -- from attrs->>'tool_result_size_bytes' (verified present)
    correlation       text NOT NULL,   -- exact|otel_only|hook_only|heuristic
    event_count       integer NOT NULL DEFAULT 0,
    field_ranks       jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE UNIQUE INDEX tool_calls_use_id_uk ON tool_calls (session_id, tool_use_id)
    WHERE tool_use_id IS NOT NULL;
CREATE INDEX ON tool_calls (session_id, started_at);
CREATE INDEX ON tool_calls (tool_name, started_at DESC);
CREATE INDEX ON tool_calls (decision, decision_source, started_at DESC);
CREATE INDEX ON tool_calls (session_id, agent_id) WHERE agent_id IS NOT NULL;

CREATE TABLE subagents (
    session_id        text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    agent_id          text NOT NULL,
    parent_agent_id   text,           -- NULL = root (the main agent)
    agent_type        text,
    prompt_id         text,
    spawn_tool_use_id text,
    depth             integer NOT NULL DEFAULT 1,
    started_at        timestamptz,
    ended_at          timestamptz,
    status            text NOT NULL DEFAULT 'running',   -- running|complete|failed|unknown
    tool_call_count   integer,        -- NULL when the session has no hook coverage (SPEC §1.9)
    input_tokens      bigint,         -- always NULL in v1 (SPEC §1.9)
    output_tokens     bigint,         -- always NULL in v1
    cost_usd          numeric(16,8),  -- always NULL in v1
    field_ranks       jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (session_id, agent_id)
);
CREATE INDEX ON subagents (session_id, parent_agent_id);

CREATE TABLE metric_samples (
    ts          timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL DEFAULT now(),
    name        text NOT NULL,
    vendor      text NOT NULL,
    session_id  text,
    value       double precision NOT NULL,
    delta       double precision,      -- filled by the rollup job for cumulative series
    temporality text NOT NULL,         -- delta|cumulative|gauge
    series_hash bytea NOT NULL,        -- sha256(name + sorted attrs) — series identity
    attrs       jsonb NOT NULL DEFAULT '{}'::jsonb,
    dedup_key   text NOT NULL,
    PRIMARY KEY (ts, series_hash, dedup_key)
) PARTITION BY RANGE (ts);
-- per-partition (2), created by the partition manager (partitions.go):
--   CREATE INDEX ON metric_samples_YYYY_MM (name, ts DESC);
--   CREATE INDEX ON metric_samples_YYYY_MM (series_hash, ts);

CREATE TABLE metric_series_state (
    series_hash bytea PRIMARY KEY,
    last_ts     timestamptz NOT NULL,
    last_value  double precision NOT NULL
);

-- Buckets needing recomputation by the rollup job (P3-04, 004_rollups.sql).
-- Written INSIDE the ingest transaction, which is what makes the rollups
-- immune to pre-commit sequence allocation (SPEC §2.4).
CREATE TABLE rollup_dirty (
    bucket timestamptz NOT NULL,
    source text NOT NULL,            -- 'event' | 'metric'
    PRIMARY KEY (bucket, source)
);

-- +goose Down
DROP TABLE rollup_dirty;
DROP TABLE metric_series_state;
DROP TABLE metric_samples;
DROP TABLE subagents;
DROP TABLE tool_calls;
