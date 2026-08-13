-- read_events.sql holds P3-03's ONE fixed, single-statement query:
-- GetEventByRef, Reader.GetEvent's (ts, seq) primary-key lookup (SPEC §1.2,
-- §2.2, §4.2). ListEvents is the dynamic-filter/dynamic-sort query SPEC
-- §3.3 explicitly carves out ("the three dynamic-filter queries ... are
-- hand-built with a whitelist-based clause builder in postgres/filter.go")
-- — it stays hand-written pgx SQL in read_events.go, exactly like
-- ListSessions, and is NOT here.

-- name: GetEventByRef :one
-- The full events row GetEvent needs (every column, including attrs — SPEC
-- §4.2: EventDetail is TimelineEvent plus attrs, unconditionally). A plain
-- PK lookup on (ts, seq) — the event_ref's decoded form (SPEC §1.2: "the PK
-- is (ts, seq)") — no filter, no sort, hence sqlc rather than filter.go.
-- Matching on both columns of the composite PK is what makes this an Index
-- Scan on exactly one partition (the ts equality prunes to it) rather than
-- a scan of every partition (SPEC §2.5's AC).
SELECT seq, id, ts, ingested_at, session_id, prompt_id, vendor, source, kind, event_name, vendor_seq,
       tool_name, tool_use_id, decision, decision_source, tool_source, query_source, model,
       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
       cost_usd, cost_source, duration_ms, success, error_type,
       agent_id, parent_agent_id, agent_type, permission_mode, file_path,
       request_id, message_uuid, clock_skewed, dedup_key, attrs
FROM events
WHERE ts = sqlc.arg(ts) AND seq = sqlc.arg(seq);
