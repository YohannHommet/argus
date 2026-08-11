# Spec review #1 — adversarial pass (2026-08-11)

Reviewer: independent Opus agent, did not author SPEC/PLAN. Blockers B1/B2 reproduced
against a live Postgres with the spec's exact DDL. Findings to be resolved as spec
amendments before Phase 1 starts.

---

## BLOCKERS

**B1 — `ON CONFLICT (ts, dedup_key)` cannot work with per-partition unique indexes.** `SPEC.md` §1.7(2) + §2.2 index block + `PLAN.md` P2-05/P2-06.
§2.2 creates the idempotency index as `CREATE UNIQUE INDEX ON events_YYYY_MM (ts, dedup_key)` — "per-partition, created by the partition manager" — while `WriteBatch` inserts into the parent `events` with `ON CONFLICT (ts, dedup_key) DO NOTHING`. Postgres requires the inference target to exist on the *partitioned table*. Verified on postgres:16-alpine with the spec's exact DDL:
```
CREATE INDEX
ERROR:  there is no unique or exclusion constraint matching the ON CONFLICT specification
```
The whole idempotency design (and Phase-2 exit criterion 5) fails at the first insert.
**Fix**: declare `UNIQUE (ts, dedup_key)` on the parent in `002_events.sql` (legal — it contains the partition key); Postgres then creates the per-partition indexes itself. Drop that line from the partition manager and from P2-05's "all 7 indexes" AC (it becomes 6 + 1 inherited).

**B2 — Hook idempotency is inoperative, because `ts` is receipt time and `ts` is in the unique key.** `SPEC.md` §1.5.2 (`ts = now()` at receipt) + §1.7(2) + `PLAN.md` Phase-2 EC 5, P2-13.
The key is `(ts, dedup_key)`. Two POSTs of the same hook body (agent retry, or the duplicate-settings-scope case §1.7 explicitly promises to collapse) arrive milliseconds apart with *different* `ts` → two rows. Verified: same `dedup_key`, `ts` 1 s apart → `rows_for_one_dedup_key = 2`. So "collapses to one row" is false, `argus_ingest_deduped_total` will read 0 for hooks, and P2-13's `--chaos-duplicates` AC cannot pass.
**Fix**: make hook dedup independent of receipt time — add a non-partitioned `ingest_dedup(dedup_key text PRIMARY KEY, first_seen timestamptz)` ledger inserted in the same transaction (TTL-pruned by the retention job), and let the events insert be gated on that. Or require a payload-derived timestamp before accepting a hook.

**B3 — Per-subagent cost/tokens have no data source; the simulator hides it.** `SPEC.md` §1.3 (declines to promote `query_source`), §2.3 `subagents.cost_usd/input_tokens/output_tokens`, §4.3 subagent response, `PLAN.md` P2-08 AC ("root aggregates equal session totals minus the sum of children"), P4-05 ("per-node badges … cost").
Cost exists **only** on `claude_code.api_request` (§1.5.1: "Cost is only on api_request"). Per the research doc, `api_request`'s fields and the standard-attribute set contain no `agent_id` — `agent_id`/`parent_agent_id` exist only on **beta traces** (not ingested) and on **hook** payloads. §1.3 justifies dropping `query_source` because "subagent attribution in v1 comes from `agent_id`, which is stronger" — but `agent_id` is never present on any cost-bearing event. Meanwhile §7.1 has the sim emit "a nested mini-session of api_requests … with `agent_id`/`parent_agent_id`", i.e. an attribute real Claude Code does not emit — so every test and demo is green and production shows $0.00 on every subagent node.
**Fix**: promote `query_source` (`main|subagent|auxiliary`) to an events column; report subagent cost only in aggregate ("subagent cost: $X of $Y") in v1; make `subagents.cost_usd`/tokens nullable-and-unrendered, and remove the "root = session minus children" AC. Keep per-node `tool_call_count` only if hook tool payloads are confirmed to carry `agent_id` (unverified today), otherwise null it too. Stop emitting `agent_id` on sim `api_request` events.

**B4 — The rollup watermark on `events.seq` will permanently miss buckets under concurrent ingest.** `SPEC.md` §2.4 rollup job steps 2/5 + §3.6 ("`seq` is assigned by Postgres, so parallel workers can interleave").
Sequence values are handed out before commit. Worker A takes seq 100, worker B takes 101 and commits first; the job (60 s later, or sooner) reads `WHERE seq > w`, sees 101, advances the watermark to 101; A then commits. Seq 100's bucket is never marked dirty again unless another event happens to land in the same hour. Since §2.5 forbids any request-time aggregation over `events`, the dashboard silently and permanently undercounts cost/tokens — the #2 v1 feature. Note §2.2 already contradicts the job text: the index is annotated "rollup watermark scan" on `ingested_at`, but the job filters on `seq`.
**Fix**: watermark on `ingested_at` with a fixed overlap (`WHERE ingested_at > watermark_ts - 2*ARGUS_ROLLUP_INTERVAL`), or keep `seq` but re-scan a lookback window; additionally always recompute the current and previous bucket unconditionally. Add a P3-05 AC that races two open transactions with interleaved commits and asserts the earlier one's bucket is still corrected.

---

## MAJOR

**M1 — `sessions.status` is simultaneously derived, stored, and indexed.** §1.7 rule 1 says a stub row exists with `status='unknown'`; the derivation paragraph says status is "Computed in SQL, not stored as truth" with three values; §2.1 has **no** `status` column; §4.3 exposes `status ∈ active|ended|abandoned|unknown` as a filter and sort surface; §2.5 demands an index `sessions (status, last_event_at DESC)` that cannot exist. Also §2.1 defines *zero* indexes on `sessions`, yet §4.3 offers keyset sort on `last_event_at|started_at|cost_usd|event_count`.
**Fix**: pick one — recommend a stored `status` column maintained by the upsert plus a periodic active→abandoned sweep (this also makes the index and `ORDER BY` real), define `unknown` = stub, and add the four sort-supporting indexes to `001_core.sql`.

**M2 — `GET /api/v1/events/{id}` is required by the UI and the plan but absent from the API table, the Store interface, and any index.** §4.3 ("the event-detail drawer fetches one event with `attrs`"), §6.3 `EventDetailSheet`, `PLAN.md` P3-03 (`GetEvent(id)`) and P3-07 (handler for `/api/v1/events/{id}`) — but §4.2's endpoint table and §3.3's `Reader` interface have neither. P1-04 is told to implement "the **complete** interface from §3.3" and P3-01 authors OpenAPI from §4.2, so the endpoint gets no schema and P3-09's "request table covers 100 % of operationIds" won't cover it. Worse, `events` has no index on `id uuid` (PK is `(ts, seq)`), so the lookup is a full scan of every partition.
**Fix**: add `GetEvent` to §3.3 and `/api/v1/events/{id}` to §4.2; either add a per-partition index on `(id)` or make the drawer fetch by `(ts, seq)` and drop the uuid lookup.

**M3 — Analytics filtered by model returns zeroed counters.** §2.4 `rollup_hourly` PK includes `model`; only `llm.request` events carry a model, so `sessions_started`, `turns`, `tool_calls`, `tool_rejects`, `active_seconds` all land in the `model=''` row. `GET /api/v1/analytics/summary?model=X` (§4.3, and DECISIONS' "dashboards filtered by project, **model**, date range") then returns `turns: 0, tool_calls: 0, sessions: 0` alongside a real cost.
**Fix**: define in §4.3 which counters are model-attributable and return `null` (not `0`) for the rest when a model filter is active; or split model-dimensioned metrics into their own rollup table. Add a P3-06 AC for the model-filtered summary.

**M4 — Late-learned `project` silently splits historical rollups.** `project` lives only on `sessions` and is written by the hook `SessionStart`/`CwdChanged` (§1.5.3(a)), while §1.7 stub-on-reference lets `llm.request` events be rolled up before the session's project is known. Those buckets are attributed to `project=''` and are never re-dirtied (their events' `seq` is below the watermark), so the project dashboard is permanently wrong for every session whose OTel logs beat its `SessionStart` hook — the common case, since OTel batches at 5 s and hooks fire synchronously in some orders but not others.
**Fix**: when a session's `project`/`cwd` changes, enqueue its touched hour buckets into a `rollup_dirty(bucket)` table consumed by the job (or recompute buckets for sessions whose `updated_at > last_run_at`).

**M5 — `EventsSince(seq, limit)` has no usable index.** §3.3/§5.2 replay filters on `seq`, but the PK is `(ts, seq)` (leading column `ts`) and the only other candidate — the partial index on `ingested_at WHERE kind <> 'unknown'` — is both on the wrong column and excludes `unknown` events that the hub does publish live. Every SSE reconnect scans up to 90 days of partitions.
**Fix**: make the contract `EventsSince(ts, seq, limit)` and bound it by the replay window (`ts >= now() - window AND (ts, seq) > (…)`) so the PK is used; drop the `kind <> 'unknown'` predicate from that index.

**M6 — Clock clamp / retention / `too_old` / DEFAULT partition are mutually inconsistent, and P3-10's AC is unreachable.** §1.2 clamps any `ts` outside `[now-30d, now+1h]` to `ingested_at`; §1.7(3) says events older than a dropped partition are "rejected with `argus_ingest_too_old_total`"; §2.4 keeps a DEFAULT partition that "must stay empty". With a 30-day clamp and 90-day retention, no event can ever address a dropped partition, so `too_old` is dead code and `PLAN.md` P3-10's AC ("an event whose partition was dropped is rejected at write with the `too_old` counter") cannot be satisfied. Conversely a DEFAULT partition means an out-of-range insert never errors, so rejection could not be detected anyway. The clamp also silently rewrites any legitimate backfill beyond 30 days.
**Fix**: set the clamp lower bound to `ARGUS_RETENTION_RAW_DAYS` (not 30 d), drop the DEFAULT partition (let the insert error and be classified as `too_old`), and restate P3-10's AC as "an event with `ts` older than the retention cutoff is rejected".

**M7 — The simulator cannot be deterministic as specified.** §7.2 promises "Identical seed + flags ⇒ byte-identical payload sequence (asserted by a golden test over `--out`)" while §7.2 also derives all timestamps from a simulated clock anchored on wall-clock `now` with `--backfill=14d`. `PLAN.md` P2-12 AC ("twice produces byte-identical files") and Phase-2 EC 5 ("re-running the same seeded sim twice adds zero new rows") both depend on stable timestamps; OTel dedup keys are `session_id:vendor_seq:event_name` but the partition key `ts` is part of the unique index, so a second run at a later wall clock duplicates everything.
**Fix**: add `--clock-origin=<RFC3339>` (required, or defaulted to a fixed epoch, whenever `--out` or the dedup test is used) and derive every timestamp from it.

**M8 — Phase 1 cannot be green as its exit criteria demand.** Exit criterion 1 requires "the GitHub Actions `ci.yml` run on `main` is green (all 6 jobs)", and P1-06 builds all six from SPEC §8.3 — including `openapi`, which runs `pnpm gen:api` + spec lint against `server/api/openapi.yaml`. That file is first authored in P3-01 and `gen:api` first exists in P3-11. P1-06 waives only the `compose` job.
**Fix**: in P1-06, scope `ci.yml` to five jobs and add the `openapi` job in P3-01/P3-11; restate Phase-1 EC 1 as "all jobs defined in this phase".

**M9 — Parallel tickets collide on files, violating PLAN's own normative rule.**
- `[P] P1-06, P1-07` — both own `scripts/smoke.sh` and both edit `Makefile`; P1-07 also edits `README.md` created by P1-01.
- `[P] P2-10, P2-11` — both edit `server/internal/httpapi/router.go` (mount).
- Phase 6 `[P] P6-01…P6-06`, asserted "disjoint file sets": P6-01 and P6-03 both own `docs/OPERATIONS.md`; P6-01 and P6-02 both edit `Makefile`; P6-04 (`web/src/components/**`) and P6-05 (`HealthStrip.vue`, `router/index.ts`) overlap, and P6-05/P6-06 both rewrite tests and fixtures.
**Fix**: give `smoke.sh` to P1-07 only and `Makefile` edits to P1-01 only; add a tiny `router_mounts.go` per ingest package (or serialize P2-10 → P2-11); serialize P6-01 → P6-03 on `OPERATIONS.md` and P6-04 → P6-05 → P6-06.

**M10 — `tool_decision` carrying `tool_use_id` is unverified yet load-bearing on the differentiator.** §1.5.1 maps `tool_name` and `tool_use_id` off `claude_code.tool_decision`. The research doc's `tool_decision` field list is `decision`, `tool_source`, `source` — `tool_use_id` is documented only on `tool_result`. The entire decision-provenance join (`tool_calls` unique on `(session_id, tool_use_id)`, §1.6's promise that "decision provenance comes from OTel and never depends on the heuristic") rests on it, and unlike the hook `tool_use_id` it carries no `[unverified-safe]` marker.
**Fix**: verify against a live Claude Code capture before P2-02. If absent, correlate `tool_decision` by `(session_id, prompt_id, tool_name)` nearest-open-call — which means the heuristic path *is* load-bearing and §1.6's guarantee must be rewritten.

**M11 — `event.sequence` "per-session monotonic — verified" is not established by the research, and a wrong assumption causes silent loss.** §1.2 and §1.7(2) both assert it as verified; the research doc only verifies **presence** ("all carrying `event.name`, `event.timestamp`, `event.sequence`"). The OTel dedup key is `otel:{session_id}:{vendor_seq}:{event_name}` with `DO NOTHING`, so if the counter is per-export-batch or per-process rather than per-session, two genuinely distinct events collapse and are dropped with no error.
**Fix**: append a content hash: `otel:{session_id}:{vendor_seq}:{event_name}:{sha256(record)}`. This is idempotent for true retransmits (identical bytes) and collision-free otherwise, and removes the dependency on the unverified property. Also define the "hash form" §1.7 says to fall through to when `vendor_seq` is absent — it is currently undefined for `otel_log`.

**M12 — The README quickstart config makes `file_path` (and subagent-spawn linkage) permanently NULL.** §1.5.1 sources `file_path` and `agent_type` from `attrs.tool_parameters.*`, which per the research exists only with `OTEL_LOG_TOOL_DETAILS=1`. The §8.2 quickstart export block sets `CLAUDE_CODE_ENABLE_TELEMETRY`, both exporters, protocol, endpoint and temporality — not `OTEL_LOG_TOOL_DETAILS`. So the file column in `ToolCallTable` (P4-06), `file_path` in the timeline (§4.3 example) and `subagents.spawn_tool_use_id`/`agent_type` are dead for every user who follows the README.
**Fix**: add `OTEL_LOG_TOOL_DETAILS=1` to the quickstart block with a one-line note about what it exposes, and to P6-01's AC.

**M13 — Multi-row upserts inside the ingest transaction have no ordering rule; deadlock is likely.** §1.6 does event insert + `sessions`/`turns`/`tool_calls`/`subagents` upserts in one transaction, with 4 workers × 500-event batches (§3.6). Two batches touching sessions A and B in opposite order deadlock; the `turns.session_id` FK adds a share-lock-then-exclusive-lock interleaving on the same rows. §3.6's retry is a generic 3× backoff with no `40P01` classification, and after 3 failures the batch is **dropped**.
**Fix**: sort all projection upserts within a batch by primary key (sessions by id, turns by (session_id, prompt_id), …) and document it as an invariant; classify `40P01`/`40001` as immediately retryable with more attempts than 3, and add a P2-06 AC with two concurrent overlapping batches asserting no dropped batch.

**M14 — PLAN adds two top-level views the SPEC does not have, and the criteria still say "four".** P4-06 creates route `/tools` + `ToolExplorerView.vue`; P6-05 creates `DataQualityView.vue` + a router edit. SPEC §6.1 says "4 top-level routes" and §6.2's table lists only `/sessions`, `/sessions/:id`, `/analytics`, `/live`. Phase-4 EC 8 and P6-04's AC both say "the four views", so the two new views escape a11y and build criteria.
**Fix**: add both routes to §6.2 (they are justified — `/tools` *is* the differentiator drill-down) and change every "four views" to enumerate them.

---

## MINOR

**m1** — `metric.sample` kind is unproducible: §1.4 says it is mirrored into `events` for metrics "that have no log-event twin", but §1.8's table answers **no** to "Mirrored as an `event`" for all seven metrics plus unknown. With `exhaustive` enabled on `Kind` (§8.4) it's a permanently dead branch. *Fix*: delete `metric.sample` from the taxonomy and the parenthetical in §1.4.

**m2** — `tool_calls.input_size_bytes` / `result_size_bytes` (§2.3) and §1.5.3(a) ("`duration_ms`, sizes | otel_log") have no population path: §1.3 deliberately leaves `tool_input_size_bytes`/`tool_result_size_bytes` unpromoted, and P2-07 never mentions them. *Fix*: state that the upsert reads them from `attrs->>`, and add them to a P2-07 AC — or drop the columns.

**m3** — §3.7's config table (which P1-02 must implement in full) is missing keys used elsewhere in the spec: `ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY` (§1.5.2), `ARGUS_STREAM_MAX_SUBSCRIBERS` (§5.3), `ARGUS_STREAM_REPLAY_MAX` (§5.2), `ARGUS_SHUTDOWN_GRACE` (§3.8), `ARGUS_RETENTION_HOUR` (§2.4), `ARGUS_ROLLUP_MAX_BUCKETS` is present but `ARGUS_ATTRS_RETENTION_DAYS` (OQ-5) is not. *Fix*: complete the table.

**m4** — §5.1's framing example puts `id: 918234` on an `event: session` frame, contradicting the bullet "`id:` is the event `seq` — only on `event: event` frames". Since the browser latches `Last-Event-ID` from any `id:`, this would corrupt replay. *Fix*: remove `id:` from the session-frame example.

**m5** — `GET /api/v1/stream?project=` (§5.1) is unimplementable: the hub filters `model.Event`, which has no `project` (project lives on `sessions`). *Fix*: drop the param, or have the hub consult a session→project map maintained by the publisher.

**m6** — P3-10's "`RebuildProjections` reproduces **byte-identical** projection rows (compared by a checksum query)" is impossible: `tool_calls.id uuid DEFAULT uuidv7()` regenerates on replay. *Fix*: exclude generated ids from the checksum, or derive `tool_calls.id` deterministically (uuidv5 over `session_id|tool_use_id`).

**m7** — §1.5.1 claims "All 15 documented `claude_code.*` log events", but the research doc's heading says **16** log events and then lists 15 names — the source itself is internally inconsistent, so exhaustiveness cannot be claimed. *Fix*: say "the 15 documented in the research doc; the `unknown` fallback covers the rest" and add a P6-05 note that the `unknown` inspector is how the 16th is found.

**m8** — OQ-1 (GitHub owner slug → Go module path) is still open, yet P1-02 is told to use "module path per SPEC §3.1", and §3.1 defers to OQ-1. Changing it later rewrites every import. *Fix*: answer OQ-1 before P1-01 lands; it is a one-word decision.

**m9** — `turns.cost_source text NOT NULL DEFAULT 'reported'` (§2.1) cannot represent a turn mixing reported and estimated cost, while `sessions` correctly splits `cost_usd`/`cost_estimated_usd`. §4.2's `/turns` endpoint promises "per-turn aggregates". *Fix*: mirror the session shape (`cost_usd` + `cost_estimated_usd`) and drop `cost_source`.

**m10** — The README hook snippet (§1.5.2) sets `"timeout": 5` on every event including `SessionEnd`, whose budget is a hard 1.5 s shared across all hooks (verified fact). Harmless at runtime but it teaches the wrong number. *Fix*: use `"timeout": 1` for `SessionEnd` in the shipped config, with a comment citing the shared budget.

**m11** — P2-06 AC (e) asserts `status='abandoned'`/`active` before any read path exists (§1.7 makes status a read-time derivation; `ListSessions` lands in P3-02). Testable with inline SQL, but the ticket's file list has no read query to test against. *Fix*: state that the AC uses the derivation SQL directly, or move it to P3-02. (Resolved automatically if M1 makes status a stored column.)

---

## Verdict

The spec is unusually strong on the things specs usually get wrong — it has an explicit precedence model, a rebuildable-projection invariant, a named place for every heuristic, and it mostly respects the research doc's confidence flags. But it is **not implementation-ready**: four blockers are load-bearing on the first two phases, and two of them (B1, B2) were reproduced against a real Postgres in under a minute. B3 is the most damaging finding because the simulator manufactures the very attribute that makes the subagent feature look implementable, so the failure surfaces only when the owner points real Claude Code at it. B4 and M4 mean the analytics dashboard would be quietly, permanently wrong under the concurrency the design explicitly chose. Scope discipline is good (M14 is the only real creep, and it's defensible), and the plan's ticket decomposition is genuinely well-sized — its defects are mechanical (M8, M9) and cheap to fix. Recommendation: resolve B1–B4 plus M1, M2, M5, M6, M10, M11 as spec amendments before P1-01, answer OQ-1, and schedule a live-capture of `claude_code.tool_decision` and `event.sequence` (M10, M11) so the differentiator's join key stops being an assumption. With those in hand, Phases 1–2 are ready to hand to implementers.
