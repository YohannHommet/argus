-- 004_rollups.sql
--
-- Reproduces docs/SPEC.md §2.4 verbatim: rollup_hourly, rollup_daily,
-- model_prices, job_state. `rollup_dirty` is deliberately ABSENT from this
-- file — it is already created by 003_projections.sql (P2-06 needed it to
-- exist so WriteBatch's single transaction could mark it alongside
-- events/projections, before this migration existed in the build order;
-- see docs/review/phase-2-deviations.md D-15 and SPEC §2.4's deviation
-- note). Re-creating it here would fail against every already-migrated
-- database.
--
-- Rollups deliberately carry no `query_source` dimension (SPEC §2.4): its
-- real vocabulary is unknown (§1.9), so putting it in a primary key would
-- make cardinality a function of undocumented agent behaviour.
--
-- §0's rule applies: no CHECK constraint, enum, or domain on any
-- vendor-supplied vocabulary column (project, vendor, model, source, ...).
-- The inline comments below are documentation only.

-- +goose Up
CREATE TABLE rollup_hourly (
    bucket                timestamptz NOT NULL,      -- date_trunc('hour', ts)
    project               text NOT NULL DEFAULT '',   -- '' = unknown, never NULL
    vendor                text NOT NULL DEFAULT '',
    model                 text NOT NULL DEFAULT '',   -- '' for non-model-attributable counters
    source                text NOT NULL,              -- 'event' | 'metric'
    sessions_started      integer NOT NULL DEFAULT 0,
    turns                 integer NOT NULL DEFAULT 0,
    api_requests          integer NOT NULL DEFAULT 0,
    api_errors            integer NOT NULL DEFAULT 0,
    tool_calls            integer NOT NULL DEFAULT 0,
    tool_rejects          integer NOT NULL DEFAULT 0,
    input_tokens          bigint NOT NULL DEFAULT 0,
    output_tokens         bigint NOT NULL DEFAULT 0,
    cache_read_tokens     bigint NOT NULL DEFAULT 0,
    cache_creation_tokens bigint NOT NULL DEFAULT 0,
    cost_reported_usd     numeric(16,8) NOT NULL DEFAULT 0,
    cost_estimated_usd    numeric(16,8) NOT NULL DEFAULT 0,
    loc_added             bigint NOT NULL DEFAULT 0,
    loc_removed           bigint NOT NULL DEFAULT 0,
    active_seconds        bigint NOT NULL DEFAULT 0,
    commits               integer NOT NULL DEFAULT 0,
    pull_requests         integer NOT NULL DEFAULT 0,
    edit_decisions_accept integer NOT NULL DEFAULT 0,
    edit_decisions_reject integer NOT NULL DEFAULT 0,
    computed_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket, project, vendor, model, source)
);
CREATE INDEX ON rollup_hourly (bucket DESC);
CREATE INDEX ON rollup_hourly (project, bucket DESC);

CREATE TABLE rollup_daily (LIKE rollup_hourly INCLUDING ALL);  -- bucket = date_trunc('day', ts)

CREATE TABLE model_prices (
    model                  text NOT NULL,
    effective_from         date NOT NULL,
    currency               text NOT NULL DEFAULT 'USD',
    input_per_mtok         numeric(12,6) NOT NULL,
    output_per_mtok        numeric(12,6) NOT NULL,
    cache_read_per_mtok    numeric(12,6) NOT NULL DEFAULT 0,
    cache_write_per_mtok   numeric(12,6) NOT NULL DEFAULT 0,
    source                 text NOT NULL DEFAULT 'repo',    -- repo|user
    PRIMARY KEY (model, effective_from)
);
-- lookup: latest effective_from <= event date, exact model, else longest matching prefix
CREATE INDEX ON model_prices (model text_pattern_ops, effective_from DESC);

CREATE TABLE job_state (
    job          text PRIMARY KEY,   -- 'rollup'|'retention'|'partitions'|'sweep'|'rebuild'
    last_run_at  timestamptz,
    last_error   text,
    watermark    bigint,             -- used ONLY by rebuild-projections for resumption
    watermark_ts timestamptz
);

-- +goose Down
DROP TABLE job_state;
DROP TABLE model_prices;
DROP TABLE rollup_daily;
DROP TABLE rollup_hourly;
