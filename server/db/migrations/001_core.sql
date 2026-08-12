-- 001_core.sql
--
-- Reproduces docs/SPEC.md §2.1 verbatim. §0's rule is load-bearing here: no
-- CHECK constraint, Postgres enum, or other closed vocabulary on any
-- vendor-supplied column (status, start_type, end_reason, permission_mode,
-- terminal_type, vendor, entrypoint, ...). The comments below marking
-- "allowed" values (e.g. "-- unknown|active|ended|abandoned") are
-- documentation only, never enforced.

-- +goose Up
CREATE TABLE sessions (
    id                    text PRIMARY KEY,
    vendor                text NOT NULL DEFAULT 'unknown',
    project               text,                  -- basename(cwd), the analytics dimension
    cwd                   text,
    status                text NOT NULL DEFAULT 'unknown',  -- unknown|active|ended|abandoned
    start_type            text,
    end_reason            text,
    permission_mode       text,
    app_version           text,                  -- app.version, else resource service.version
    entrypoint            text,
    terminal_type         text,
    user_email            text,
    user_account_uuid     text,
    organization_id       text,
    started_at            timestamptz,           -- NULL until session.start seen
    ended_at              timestamptz,
    first_seen_at         timestamptz NOT NULL,
    last_event_at         timestamptz NOT NULL,
    event_count           bigint NOT NULL DEFAULT 0,
    turn_count            integer NOT NULL DEFAULT 0,
    tool_call_count       integer NOT NULL DEFAULT 0,
    tool_reject_count     integer NOT NULL DEFAULT 0,
    subagent_count        integer NOT NULL DEFAULT 0,
    error_count           integer NOT NULL DEFAULT 0,
    input_tokens          bigint NOT NULL DEFAULT 0,
    output_tokens         bigint NOT NULL DEFAULT 0,
    cache_read_tokens     bigint NOT NULL DEFAULT 0,
    cache_creation_tokens bigint NOT NULL DEFAULT 0,
    cost_usd              numeric(16,8) NOT NULL DEFAULT 0,   -- reported
    cost_estimated_usd    numeric(16,8) NOT NULL DEFAULT 0,
    -- map: raw query_source value ('' when absent) -> summed reported cost. Uninterpreted (§1.9).
    cost_by_query_source  jsonb NOT NULL DEFAULT '{}'::jsonb,
    models                text[] NOT NULL DEFAULT '{}',
    field_ranks           jsonb NOT NULL DEFAULT '{}'::jsonb,  -- per-column source rank (§1.5.3)
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- Indexes for §4.3's filters and its four keyset sort keys. Each sort index carries `id` as the
-- tiebreak so keyset pagination is total.
CREATE INDEX sessions_last_event_idx ON sessions (last_event_at DESC, id DESC);
CREATE INDEX sessions_started_idx    ON sessions (started_at DESC NULLS LAST, id DESC);
CREATE INDEX sessions_cost_idx       ON sessions (cost_usd DESC, id DESC);
CREATE INDEX sessions_events_idx     ON sessions (event_count DESC, id DESC);
CREATE INDEX sessions_project_idx    ON sessions (project, last_event_at DESC, id DESC);
CREATE INDEX sessions_status_idx     ON sessions (status, last_event_at DESC, id DESC);
CREATE INDEX sessions_vendor_idx     ON sessions (vendor, last_event_at DESC, id DESC);
CREATE INDEX sessions_sweep_idx      ON sessions (last_event_at)
    WHERE status IN ('active','unknown') AND ended_at IS NULL;   -- the abandoned sweep

CREATE TABLE turns (
    session_id            text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    prompt_id             text NOT NULL,
    turn_index            integer,
    started_at            timestamptz,
    ended_at              timestamptz,
    first_seen_at         timestamptz NOT NULL,
    last_event_at         timestamptz NOT NULL,
    duration_ms           integer,
    status                text NOT NULL DEFAULT 'open',   -- open|complete|failed
    api_request_count     integer NOT NULL DEFAULT 0,
    tool_call_count       integer NOT NULL DEFAULT 0,
    tool_reject_count     integer NOT NULL DEFAULT 0,
    error_count           integer NOT NULL DEFAULT 0,
    input_tokens          bigint NOT NULL DEFAULT 0,
    output_tokens         bigint NOT NULL DEFAULT 0,
    cache_read_tokens     bigint NOT NULL DEFAULT 0,
    cache_creation_tokens bigint NOT NULL DEFAULT 0,
    cost_usd              numeric(16,8) NOT NULL DEFAULT 0,   -- reported
    cost_estimated_usd    numeric(16,8) NOT NULL DEFAULT 0,   -- a turn can legitimately mix both
    models                text[] NOT NULL DEFAULT '{}',
    field_ranks           jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (session_id, prompt_id)
);
CREATE INDEX turns_session_started_idx ON turns (session_id, started_at NULLS LAST);

CREATE TABLE ingest_dedup (
    dedup_key     text PRIMARY KEY,
    first_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ingest_dedup_age_idx ON ingest_dedup (first_seen_at);

-- +goose Down
DROP TABLE ingest_dedup;
DROP TABLE turns;
DROP TABLE sessions;
