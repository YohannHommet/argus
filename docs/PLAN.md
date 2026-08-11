# Argus — Phased Implementation Plan

*Companion to `docs/SPEC.md`. Phases match the orchestration sequence in `docs/DECISIONS.md`:
spec → scaffold+CI → ingestion → storage/query+API → UI explorer+analytics → live view →
polish/README. Owner reviews at phase boundaries only.*

## How to read this

- Every ticket is sized for **one Sonnet implementation agent in one session**: a bounded file set,
  a stated contract, and acceptance criteria it can verify itself by running something.
- **Files touched** is normative. Tickets marked as parallel in the same phase must not overlap on
  files. If an implementer needs a file outside its list, it stops and reports instead of widening
  scope.
- Acceptance criteria are observable. "Compiles" is never an acceptance criterion.
- `[P]` in the parallel line = these ticket IDs may run concurrently.
- Every ticket ends with `go test ./...` (server) or `pnpm unit && pnpm type-check && pnpm lint`
  (web) green, plus its own new tests. That is implicit and not repeated per ticket.
- Ticket IDs are stable. Later tickets reference earlier ones by ID.

**Global conventions the implementer must not re-decide**: `SPEC.md` §3.1 package layout, §3.2
pinned dependency versions, §3.3 the `Store` interface, §1.4 the kind taxonomy, §4.1 API
conventions. Any deviation is a report-back, not a judgement call.

---

## Phase 1 — Scaffold + CI (walking skeleton)

**Goal**: a public repo where `docker compose up` serves an empty-but-real UI backed by a migrated
Postgres, and CI is green — before any feature exists.

**Exit criteria**
1. `make ci` passes locally; the GitHub Actions `ci.yml` run on `main` is green (all 6 jobs).
2. `docker compose -f deploy/docker-compose.yml up -d` → `curl -fsS localhost:8080/readyz` returns
   `200 {"status":"ok","migrations":"current"}`.
3. `curl -fsS localhost:8080/api/v1/meta` returns `{"version":"…","commit":"…","retention_days":90,…}`.
4. `http://localhost:8080/` serves the Vue SPA from the embedded assets, in dark mode, with a
   working theme toggle and a sidebar containing the four (mostly empty) routes.
5. `psql \dt` shows `sessions`, `turns`, `goose_db_version`.
6. LICENSE (MIT) and a README with the quickstart skeleton are committed.

| ID | Title | Depends on |
|---|---|---|
| P1-01 | Repo scaffold, license, Makefile | — |
| P1-02 | Go module, `argusd` subcommand skeleton, config, logging | P1-01 |
| P1-03 | Web app scaffold: Vite + Vue + Tailwind 4 + shadcn-vue + router + Pinia + dark mode | P1-01 |
| P1-04 | Store skeleton: pgxpool, embedded goose migrations, `001_core.sql`, `Store` interface | P1-02 |
| P1-05 | HTTP skeleton: chi, middleware, problem+json, ops endpoints, `/api/v1/meta`, embedded SPA | P1-02, P1-04 |
| P1-06 | CI workflows, golangci-lint config, dependabot | P1-02, P1-03 |
| P1-07 | Dockerfile, docker-compose, compose smoke script | P1-03, P1-05 |

Parallel: `[P] P1-02, P1-03` — then `[P] P1-04` → `P1-05` → `[P] P1-06, P1-07`.

---

**P1-01 — Repo scaffold, license, Makefile**
Scope: directory skeleton, MIT LICENSE (owner's personal identity), README with the quickstart
outline and a "status: pre-alpha" note, `.gitignore` (Go, node, dist, .env), `.editorconfig` (LF
enforced — never CRLF), `Makefile` with `dev`, `build`, `test`, `lint`, `ci`, `gen`, `migrate`,
`sim`, `compose-up` targets, `CONTRIBUTING.md` stub, `docs/` left as-is.
Files: `LICENSE`, `README.md`, `.gitignore`, `.editorconfig`, `.gitattributes`, `Makefile`,
`CONTRIBUTING.md`, empty `server/`, `web/`, `deploy/`.
AC: `make` with no target prints the target list; `git ls-files` shows no build artefacts;
`.gitattributes` sets `* text=auto eol=lf`.

**P1-02 — Go module, subcommand skeleton, config, logging**
Scope: `go.mod` (module path per SPEC §3.1, `go 1.25`); `cmd/argusd/main.go` dispatching
`serve|migrate|sim|retention|rebuild-projections|config|version|healthcheck` (all but `version`,
`config`, `healthcheck` may be stubs returning "not implemented" in this ticket); `internal/config`
with the full key table from SPEC §3.7 loaded via koanf (defaults ← YAML ← `ARGUS_` env),
validation, `--config` flag, `config --print` with secret redaction, unknown-`ARGUS_*` warning;
`internal/telemetry` with slog setup (`json`/`text` via tint) and build-info vars set by ldflags.
Files: `server/go.mod`, `server/go.sum`, `server/cmd/argusd/main.go`,
`server/internal/config/{config.go,config_test.go}`,
`server/internal/telemetry/{log.go,buildinfo.go}`.
AC: table-driven `config_test.go` covers defaults, env override, YAML override, env-beats-YAML,
invalid duration → error, missing `ARGUS_DATABASE_URL` → error naming the key;
`go run ./cmd/argusd version` prints version+commit+go version; `argusd config --print` shows
`ARGUS_DATABASE_URL=postgres://argus:***@…` redacted; `argusd nonsense` exits 2 with usage.

**P1-03 — Web app scaffold**
Scope: pnpm project (`vue@3.5.41`, `vite@8.2.1`, `typescript@7.0.2`, `vue-router@5`, `pinia@4`,
`tailwindcss@4.3.3` via `@tailwindcss/vite`, `vitest@4.1.10`, `@vue/test-utils`, eslint flat
config, `vue-tsc`); `shadcn-vue` init with 4 primitives (button, card, badge, switch);
`src/assets/theme.css` with the full token set from SPEC §6.1 including Argus semantic tokens;
`AppShell.vue` (sidebar with 4 nav items, topbar, `ThemeToggle`); `uiStore` with persisted theme
and the anti-flash inline script in `index.html`; four placeholder route views; Vite dev proxy for
`/api`, `/v1`, `/ingest`.
Files: everything under `web/` (`package.json`, `pnpm-lock.yaml`, `vite.config.ts`,
`tsconfig*.json`, `eslint.config.js`, `index.html`, `src/main.ts`, `src/App.vue`, `src/router/`,
`src/stores/ui.ts`, `src/assets/theme.css`, `src/components/layout/`, `src/components/ui/`,
`src/views/`, `src/**/__tests__/`).
AC: `pnpm build` emits `web/dist`; `pnpm unit` passes with a test asserting `uiStore.toggle()`
flips `document.documentElement.classList` between dark/light and persists to `localStorage`, and
a mount test of `AppShell` asserting 4 nav links; `pnpm type-check` and `pnpm lint` clean; dark is
the default with no `localStorage` entry and no `prefers-color-scheme` match.

**P1-04 — Store skeleton and `001_core.sql`**
Scope: `internal/store/store.go` with the **complete** interface from SPEC §3.3 (every method
declared; unimplemented methods in postgres return `ErrNotImplemented` for now — later tickets
fill them in without touching the interface); `internal/store/postgres` with pgxpool construction
(max conns, app_name, statement cache), `Health`, `Close`, goose-over-`embed.FS` `Migrate` guarded
by `pg_advisory_lock`; `db/migrations/001_core.sql` exactly as SPEC §2.1; `internal/store/testing`
harness that provides a Postgres via `ARGUS_TEST_DATABASE_URL` if set, else testcontainers, and
gives each test an isolated schema.
Files: `server/internal/store/{store.go,errors.go}`,
`server/internal/store/postgres/{pool.go,migrate.go,migrations_embed.go,pool_test.go}`,
`server/internal/store/testing/harness.go`, `server/db/migrations/001_core.sql`,
`server/internal/model/{session.go,turn.go}` (only the types the interface needs).
AC: integration test brings up Postgres, runs `Migrate`, asserts `sessions`/`turns` exist with the
expected columns and that a second concurrent `Migrate` from two goroutines does not error
(advisory lock); `Migrate` is idempotent when run twice; `argusd migrate status` prints applied
versions; harness gives two parallel tests non-colliding schemas.

**P1-05 — HTTP skeleton and embedded SPA**
Scope: `internal/httpapi` with chi router; middleware chain (request id, recoverer, slog access
log with sampling, real-ip, timeout, optional CORS from config, `RequireIngestToken` /
`RequireAPIToken` seams that are no-ops when the tokens are empty); RFC 9457 problem+json helper
with the URN scheme; `GET /healthz`, `/readyz` (DB ping + goose version current + queue-not-
saturated hook point), `/metrics` (promhttp on a private registry), `GET /api/v1/meta`; SPA serving
from `go:embed` with SPA-fallback routing (unknown non-API path → `index.html`), correct
content-types, immutable cache headers on hashed assets and `no-cache` on `index.html`;
`internal/app` wiring config → store → httpapi and `serve` with the graceful-shutdown sequence
from SPEC §3.8; `argusd healthcheck` subcommand for the container healthcheck.
Files: `server/internal/httpapi/{router.go,middleware.go,problem.go,ops.go,meta.go,assets.go,
router_test.go,assets_test.go}`, `server/internal/app/{app.go,serve.go}`,
`server/cmd/argusd/main.go` (wire `serve`, `healthcheck`).
AC: `httptest` tests: `/healthz` 200 without a DB; `/readyz` 503 problem+json when the DB is down
and 200 when up; unknown `/api/v1/nope` → 404 `application/problem+json` with a URN `type`;
`/frontend/route` → 200 `text/html`; `/assets/x.js` gets `Cache-Control: immutable`; with
`ARGUS_API_TOKEN` set, `/api/v1/meta` is 401 without the bearer and 200 with it; a shutdown test
asserts an in-flight request completes and `Shutdown` returns before the grace deadline.

**P1-06 — CI, lint config, dependabot**
Scope: `.github/workflows/ci.yml` with the six jobs from SPEC §8.3 (`compose` job may assert only
`/readyz` in this phase — the sim smoke assertions land in P2-09); `.golangci.yml` (v2 schema, the
enable/disable lists and the `depguard` import-direction rules from SPEC §8.4);
`.github/dependabot.yml`; a `scripts/coverage-floor.sh` that reads per-package floors from a small
config and fails below them (floors set to current reality in this phase, raised by later tickets);
`scripts/smoke.sh` used by the compose job.
Files: `.github/workflows/ci.yml`, `.github/dependabot.yml`, `.golangci.yml`,
`scripts/{coverage-floor.sh,coverage-floors.txt,smoke.sh}`, `Makefile` (add `ci`, `lint`).
AC: a pushed branch shows all jobs green in Actions; `golangci-lint run` clean locally;
deliberately adding an import of `internal/httpapi` inside `internal/store` makes `go-lint` fail
with a depguard error (verify, then revert); `scripts/coverage-floor.sh` fails when a floor is
raised above actual coverage.

**P1-07 — Dockerfile, compose, smoke**
Scope: multi-stage `server/Dockerfile` per SPEC §8.1 (web stage → go stage → distroless nonroot,
static build, ldflags version stamping, non-root, `EXPOSE 8080`); `.dockerignore`;
`deploy/docker-compose.yml` per SPEC §8.2 including healthchecks and the named volume;
`deploy/docker-compose.dev.yml` (publishes 5432, `build:` instead of `image:`);
`deploy/docker-compose.override.example.yml` (token, retention, one-shot sim service);
`scripts/smoke.sh` fleshed out: bring the stack up, poll `/readyz` for 60 s, assert `/api/v1/meta`
JSON, tear down.
Files: `server/Dockerfile`, `.dockerignore`, `deploy/*.yml`, `scripts/smoke.sh`,
`Makefile` (add `compose-up`, `compose-smoke`), `README.md` (quickstart block).
AC: `docker build -f server/Dockerfile .` succeeds and the image is < 40 MB;
`docker run --rm <img> version` prints the stamped version (not `dev`);
`bash scripts/smoke.sh` exits 0 from a clean state including volume removal; the container runs as
uid 65532 (`docker inspect`); the compose job in CI is green.

---

## Phase 2 — Ingestion

**Goal**: both wire surfaces accept real traffic, normalize it, and persist it durably and
idempotently with all four projections correct — verified by the simulator against a live server.

**Exit criteria**
1. `argusd sim --mode=demo --sessions=5 --flush-immediately --target http://localhost:8080` exits 0
   with all HTTP statuses 2xx.
2. `SELECT count(*), count(DISTINCT session_id) FROM events` > 0 and matches the sim's reported
   send count minus reported dedup suppressions.
3. `SELECT kind, count(*) FROM events GROUP BY 1` shows ≥ 12 distinct kinds and `unknown` = 0 with
   chaos flags off.
4. `SELECT count(*) FROM sessions WHERE started_at IS NULL` = 0 after a clean run; > 0 with
   `--chaos-orphans` and returns to a fully-populated row once the late `SessionStart` arrives.
5. Re-running the same seeded sim twice adds zero new rows on the second run (dedup), and
   `argus_ingest_deduped_total` equals the resent count.
6. `SELECT decision_source, count(*) FROM tool_calls GROUP BY 1` shows all 6 sources.
7. `SELECT * FROM subagents` shows depth-2 rows with non-null `parent_agent_id`.
8. Load mode at a rate above capacity returns 503/429 with `Retry-After` and never panics or grows
   memory unboundedly (`--rate=5000 --duration=60s`, RSS graphed via `/metrics`).

| ID | Title | Depends on |
|---|---|---|
| P2-01 | `internal/model`: canonical event + kind taxonomy | P1-02 |
| P2-02 | Normalizer: Claude Code OTel log events | P2-01 |
| P2-03 | Normalizer: Claude Code hook events | P2-01 |
| P2-04 | Normalizer: OTLP metrics → `metric_samples` | P2-01 |
| P2-05 | Migrations `002`/`003` + partition manager | P1-04 |
| P2-06 | `WriteBatch`: idempotent event insert + session/turn projections | P2-01, P2-05 |
| P2-07 | Tool-call projection + correlation | P2-06, P2-02, P2-03 |
| P2-08 | Subagent projection + tree edges | P2-06, P2-03 |
| P2-09 | Ingest pipeline: queue, batcher, workers, backpressure, metrics | P2-01, P2-06 |
| P2-10 | OTLP/HTTP receiver endpoints | P2-02, P2-04, P2-09 |
| P2-11 | Hooks webhook endpoint | P2-03, P2-09 |
| P2-12 | `argus-sim` core generator + wire output | P2-10, P2-11 |
| P2-13 | Chaos modes + end-to-end ingestion integration test | P2-12, P2-07, P2-08 |

Parallel: `[P] P2-02, P2-03, P2-04, P2-05` after P2-01 → `P2-06` → `[P] P2-07, P2-08, P2-09` →
`[P] P2-10, P2-11` → `P2-12` → `P2-13`.

---

**P2-01 — `internal/model`**
Scope: `Event` struct with every column from SPEC §1.3 (pointer or `sql.Null*`-free: use typed
nullable wrappers or pointers consistently — pick pointers, document once); `Kind` as a defined
string type with one constant per SPEC §1.4 row plus `KindUnknown`, an `AllKinds()` slice, and a
`Valid()` method; `Source`, `Decision`, `DecisionSource`, `Vendor` typed constants; projection
structs (`SessionSummary`, `SessionDetail`, `Turn`, `ToolCall`, `SubagentNode`, `Facets`, `Summary`,
`Series`, `Breakdown`); `DedupKey` helpers (the three forms in SPEC §1.7) with canonical-JSON
hashing; `ClampTimestamp`.
Files: `server/internal/model/*.go` + tests.
AC: tests assert every `Kind` constant is in `AllKinds()` and round-trips through JSON; canonical
JSON hashing is stable across map iteration order (100 iterations, same hash) and across key
insertion order; `ClampTimestamp` table covers in-window, 40 d old, 2 h future, zero time;
`exhaustive` linter is configured for `Kind` and passes.

**P2-02 — Normalizer: OTel log events**
Scope: `internal/ingest/normalize` — `FromOTLPLogs(*logspb.LogsData) ([]model.Event, []Rejection)`
implementing the SPEC §1.5.1 table for all 15 `claude_code.*` events plus the `unknown` fallback;
resource+scope+record attribute merging (record wins); `LogRecord.TimeUnixNano` vs
`event.timestamp` preference and skew flagging; `cost_usd_micros` preference; full raw payload into
`attrs`; missing `session.id` → `Rejection` (the only rejection case).
Files: `server/internal/ingest/normalize/{otel_logs.go,attrs.go,otel_logs_test.go}`,
`server/internal/ingest/normalize/testdata/*.json`.
AC: one table-driven test case **per row** of the §1.5.1 mapping asserting every promoted column;
a case with unknown `event.name` → `KindUnknown` with `event_name` preserved and `attrs` intact; a
case with an unknown *attribute* → present in `attrs`, no error; a record with no `session.id` →
rejected, others in the same batch still returned; a case where record and resource define the same
key → record wins; skew case sets `clock_skewed`.

**P2-03 — Normalizer: hook events**
Scope: `FromHookPayload([]byte) ([]model.Event, error)` implementing the SPEC §1.5.2 table for all
listed `hook_event_name` values plus `unknown`; defensive field reads (missing → nil, never error);
`MessageDisplay` dropped unless configured; accepts a single object or an array; the full body
always into `attrs`.
Files: `server/internal/ingest/normalize/{hooks.go,hooks_test.go}`, `…/testdata/hooks/*.json`.
AC: one case per hook event in the table; a payload with only `session_id` +
`hook_event_name` normalizes without error; an unknown `hook_event_name` → `KindUnknown`; missing
`session_id` → error (400 material); `PermissionDenied` yields `decision=reject` and
`decision_source='unknown'` (asserting we do **not** invent provenance); `MessageDisplay` returns
zero events by default and one when enabled; an array body yields N events.

**P2-04 — Normalizer: OTLP metrics**
Scope: `FromOTLPMetrics(*metricspb.MetricsData) ([]model.MetricSample, []Rejection)` — walk
resource/scope/metric, support `Sum` (delta and cumulative), `Gauge`, and `Histogram` (store
`sum`/`count` as two samples); `series_hash` over name + sorted attribute pairs; temporality
mapping; the accept-list/store-anything policy from SPEC §1.8 (unknown names stored, not rolled
up); `session.id` extraction when present.
Files: `server/internal/ingest/normalize/{otel_metrics.go,otel_metrics_test.go}`,
`…/testdata/metrics/*.json`.
AC: cases for each of the 7 `claude_code.*` metrics with their documented attribute sets;
`series_hash` is stable and differs when one attribute value changes; cumulative and delta sums are
labelled correctly; a histogram yields the `_sum`/`_count` pair; a metric with no `session.id` is
still accepted with `session_id = NULL`.

**P2-05 — Migrations `002`/`003` + partition manager**
Scope: `002_events.sql` and `003_projections.sql` exactly as SPEC §2.2/§2.3;
`postgres.EnsurePartitions(from, to)` creating monthly partitions + the per-partition index set
idempotently, plus the `DEFAULT` partition and a `CountDefaultPartitionRows` check; the hourly
partition job in `internal/app` (current month + 2 ahead); `argus_partition_default_rows` gauge.
Files: `server/db/migrations/{002_events.sql,003_projections.sql}`,
`server/internal/store/postgres/{partitions.go,partitions_test.go}`,
`server/internal/app/jobs.go`.
AC: integration test: after `EnsurePartitions` over a 3-month range, `pg_class` shows the
partitions and each carries all 7 indexes from SPEC §2.2; calling it twice is a no-op; inserting an
event with a `ts` outside all ranges lands in `DEFAULT` and the gauge reports 1; `uuidv7()` default
works (proves the PG-18 assumption — the test fails loudly on an older server with a clear
message).

**P2-06 — `WriteBatch`: events + session/turn projections**
Scope: the single-transaction write from SPEC §1.6/§1.7: idempotent event insert
(`ON CONFLICT (ts, dedup_key) DO NOTHING`, `RETURNING seq, id` so callers know what persisted),
stub-on-reference session/turn upserts, `field_ranks`-based precedence application, aggregate
counter maintenance (`event_count`, token/cost sums, `turn_count`, `models` array union),
`turn_index` assignment, status derivation SQL, `too_old` rejection for events whose partition is
gone; `BatchResult` with persisted/deduped/rejected splits. Batched inserts via `pgx.Batch`
(`CopyFrom` is not usable because of `ON CONFLICT`; document the choice).
Files: `server/internal/store/postgres/{write.go,upsert_session.go,upsert_turn.go,write_test.go}`,
`server/db/queries/write.sql`, `sqlc.yaml`, generated `server/internal/store/postgres/gen/*.go`.
AC: integration tests — (a) 500 mixed events insert in one tx and `sessions.event_count` matches;
(b) re-writing the identical batch persists 0 and reports 500 deduped; (c) a `turn.start` arriving
**after** the turn's `llm.request` events yields correct `started_at` and correct token sums;
(d) an `otel_log` write of `sessions.cwd` does not overwrite a value previously set by a `hook`
write (rank), while the reverse does overwrite; (e) a session with no `session.end` and a stale
`last_event_at` reports `status='abandoned'`, and `active` when fresh; (f) a batch failure rolls
back completely (no partial events, no counter drift) — asserted by injecting a constraint
violation.

**P2-07 — Tool-call projection + correlation**
Scope: `correlateToolCall` and the `tool_calls` upsert: create-or-update on
`(session_id, tool_use_id)`, the 4 `correlation` values, the hook-without-`tool_use_id`
one-to-one nearest-open-call heuristic within 60 s, `wait_ms`/`duration_ms` derivation, decision
precedence (`tool_decision` > `tool_result` > hook), `session.tool_call_count` /
`tool_reject_count` maintenance.
Files: `server/internal/ingest/normalize/correlate.go`,
`server/internal/store/postgres/{upsert_toolcall.go,upsert_toolcall_test.go}`,
`server/db/queries/toolcalls.sql`.
AC: integration cases — pre+decision+result all with `tool_use_id` → one row, `correlation=exact`
when a hook also matched, `otel_only` when not; hooks-only with no `tool_use_id` → `hook_only`;
hook without `tool_use_id` + concurrent OTel calls of the same tool in the same turn → each hook
matches exactly one call, never two (assert one-to-one with 3 concurrent `Edit` calls); a hook
arriving 5 minutes late does not match (`hook_only` row); `tool_decision` `source=user_reject`
overrides a `decision_source` previously written by `tool_result`; `wait_ms` computed from
`PreToolUse`→`tool_decision`.

**P2-08 — Subagent projection**
Scope: `subagents` upsert from `subagent.start`/`subagent.stop`; `parent_agent_id` from the payload
when present, else inferred from the spawning `tool.result` with `agent_type` set within the same
turn (documented as best-effort); `depth` computed from the parent chain; `status` derivation;
per-subagent aggregates attributed via `events.agent_id`; the recursive-CTE tree read
(`SubagentTree`) including the synthetic `root` node with session-minus-children aggregates.
Files: `server/internal/store/postgres/{upsert_subagent.go,subagent_tree.go,subagent_test.go}`,
`server/db/queries/subagents.sql`, `server/internal/model/subagent.go`.
AC: integration tests — a 2-level tree returns nested `children` with correct `depth`; a
`SubagentStop` without a matching start creates a row with `status='unknown'` and `started_at IS
NULL`; a cycle in `parent_agent_id` (malicious/buggy input) terminates with a depth cap and logs,
does not hang the CTE; root aggregates equal session totals minus the sum of children.

**P2-09 — Ingest pipeline**
Scope: `internal/ingest/pipeline.go` per SPEC §3.6: bounded batch channel, N workers, size/time
flush, non-blocking enqueue returning `ErrQueueFull`, 3× jittered retry then counted drop, all
Prometheus metrics (queue depth, batch size, per-source counters, dedup, write duration,
`argus_ingest_lag_seconds`, dropped), `Close()` draining within a deadline, and a `Publisher`
interface hook for the stream hub (no-op implementation in this phase).
Files: `server/internal/ingest/{pipeline.go,pipeline_test.go,metrics.go}`,
`server/internal/app/{app.go,serve.go}` (wire + drain in shutdown).
AC: unit tests with a fake store — enqueue beyond capacity returns `ErrQueueFull` and does not
block (asserted with a timeout); batches flush on size and on the timer; a store error retries 3×
then increments the drop counter; `Close()` drains queued batches before returning and returns an
error if the deadline is hit; `-race` clean with 8 concurrent producers; a fake store that blocks
forever does not leak goroutines after `Close`.

**P2-10 — OTLP/HTTP receiver**
Scope: `internal/ingest/otlp` handlers for `POST /v1/logs`, `/v1/metrics`, `/v1/traces` per SPEC
§3.4: content negotiation (protobuf/`protojson` with `DiscardUnknown`), gzip with a decompressed
size cap, `MaxBytesReader`, correct `Export*ServiceResponse` bodies, `partial_success` semantics,
503 + `Retry-After` on queue-full, 400 `Status` on malformed input, 415 on unknown content type,
`RequireIngestToken` applied.
Files: `server/internal/ingest/otlp/{logs.go,metrics.go,traces.go,codec.go,handler_test.go}`,
`server/internal/httpapi/router.go` (mount).
AC: `httptest` tests — a protobuf `ExportLogsServiceRequest` and the equivalent JSON body produce
byte-identical normalized events; response body unmarshals as `ExportLogsServiceResponse`; a body
with 3 records where 1 lacks `session.id` → 200 with `partial_success.rejected_log_records = 1`
and 2 events enqueued; a 20 MiB body → 413; a gzip bomb that decompresses past the cap → 413 and
bounded memory; malformed protobuf → 400 with a `Status` body; `/v1/traces` → 200 with an empty
response and the discard counter incremented; queue-full → 503 with `Retry-After: 1`.

**P2-11 — Hooks webhook**
Scope: `internal/ingest/hooks` handler for `POST /ingest/hook`: JSON only, single object or array,
validate `session_id`, normalize, enqueue, `202 {"ok":true,"event":"<hook_event_name>"}` with **no
store access**; 429 + `Retry-After` on queue-full with the drop counter; body cap;
`argus_hook_handler_duration_seconds`; `RequireIngestToken`.
Files: `server/internal/ingest/hooks/{handler.go,handler_test.go}`,
`server/internal/httpapi/router.go` (mount).
AC: `httptest` tests — a `SessionEnd` payload returns 202 in under 20 ms measured with a fake
pipeline, and a test using a store spy asserts **zero** store calls from the handler; missing
`session_id` → 400 problem+json; queue full → 429 with `Retry-After` and
`argus_ingest_dropped_total{source="hook"}` incremented; an array of 5 payloads enqueues 5 events;
a 2 MiB body → 413.

**P2-12 — `argus-sim` core generator + wire output**
Scope: `internal/sim` implementing SPEC §7 minus chaos flags: session/turn/tool/subagent
generation with the stated distributions and probabilities, the built-in price table, simulated
clock with `--speed` and `--backfill`, OTLP protobuf/JSON encoding using
`go.opentelemetry.io/proto/otlp`, hook POSTs, Claude-Code-like batching intervals with
`--flush-immediately`, `--seed` determinism via `math/rand/v2.PCG` derived per session,
`--out=dir/` fixture writing, `--mode=demo|load`, `--rate`, `--concurrency`, `--duration`,
`--sessions`, `--cost-mode`, `--tool-use-id-in-hooks`, the exit report;
`cmd/argus-sim` shim; the `sim` subcommand wired.
Files: `server/internal/sim/*.go`, `server/cmd/argus-sim/main.go`,
`server/cmd/argusd/main.go` (wire `sim`), `Makefile` (`sim` target).
AC: `argusd sim --out=/tmp/f --seed=7 --sessions=3` twice produces byte-identical files (golden
test in CI over a 1-session run); the emitted OTLP payloads round-trip through P2-02/P2-04
normalizers with zero `unknown` kinds and zero rejections (a unit test wires the generator
directly to the normalizer — no server needed); against a live server, `--mode=demo --sessions=5
--flush-immediately` exits 0 with an all-2xx status histogram; `--mode=load --rate=200
--duration=10s` reports throughput within 15 % of the target.

**P2-13 — Chaos modes + end-to-end ingestion test**
Scope: the 5 chaos flags from SPEC §7.1; a Go integration test (`//go:build integration`) that
starts the real app against a real Postgres, runs the sim in-process against it, and asserts every
Phase-2 exit criterion as SQL assertions; the `compose` CI job's smoke script extended to run the
demo sim and assert session/analytics-free row counts; the coverage floors raised for `normalize`
to 90 %.
Files: `server/internal/sim/chaos.go`, `server/internal/app/e2e_ingest_test.go`,
`scripts/smoke.sh`, `scripts/coverage-floors.txt`.
AC: the e2e test asserts, in one run: kinds ≥ 12; `unknown` = 0 without chaos; duplicates
suppressed with the counter matching; `--chaos-orphans` produces `partial` sessions that become
complete when the late `SessionStart` lands; `--chaos-clock-skew` sets `clock_skewed` and leaves
`DEFAULT` partition empty; `--chaos-unknown` produces `kind='unknown'` rows with `event_name`
preserved and no rejections; all 6 `decision_source` values present in `tool_calls`; depth-2
subagents present. CI runs it in the `go-test` job.

---

## Phase 3 — Storage/query + REST API

**Goal**: the full v1 read API, spec-first, backed by rollups, with the OpenAPI contract enforced
and the TS client generated.

**Exit criteria**
1. `redocly lint`/spec validation passes on `server/api/openapi.yaml`, and the conformance test
   validates ~40 request/response pairs against it.
2. After a demo sim run: `curl '…/api/v1/sessions?limit=2'` returns 2 sessions with a `next_cursor`
   that fetches the next 2 with no overlap and no gap.
3. `curl '…/api/v1/sessions/{id}/timeline?fields=slim&limit=50'` returns events in
   `(ts, vendor_seq, seq)` order; `?order=desc` reverses exactly.
4. `curl '…/api/v1/analytics/summary?from=-14d'` returns non-zero cost, and its `cost.usd` equals
   `SELECT sum(cost_reported_usd + cost_estimated_usd) FROM rollup_hourly WHERE source='event'`
   over the same window (assertion in the integration test).
5. `--cost-mode=omit` sim data yields `estimated_share > 0` and a non-zero `cost.estimated_usd`.
6. `EXPLAIN` guard test: no analytics endpoint's plan references `events`.
7. `pnpm gen:api` produces a committed `schema.d.ts` and CI's drift check is green.
8. `argusd retention --dry-run` lists the partitions it would drop; a test with a fabricated old
   partition proves the real run drops it and leaves rollups intact.

| ID | Title | Depends on |
|---|---|---|
| P3-01 | Author `openapi.yaml` for the whole v1 surface | Phase 2 |
| P3-02 | Cursor codec, filters, `ListSessions`/`GetSession`/`ListTurns` | P2-06 |
| P3-03 | `ListEvents` (timeline + cross-session) and `ListToolCalls` | P2-06, P2-07 |
| P3-04 | Migration `004`, price table + seed, cost estimation | P2-05 |
| P3-05 | Rollup job (hourly + daily, event + metric sources) | P3-04 |
| P3-06 | Analytics queries: summary, timeseries, breakdown, decisions | P3-05 |
| P3-07 | Session/timeline/tool/subagent HTTP handlers | P3-01, P3-02, P3-03 |
| P3-08 | Analytics HTTP handlers + `/facets` + `/meta` exporter flags | P3-01, P3-06 |
| P3-09 | Fake store + OpenAPI conformance harness | P3-01, P3-07, P3-08 |
| P3-10 | Retention job, `rebuild-projections`, `EXPLAIN` guard | P3-05 |
| P3-11 | TS client generation + CI drift job | P3-01 |

Parallel: `P3-01` and `[P] P3-02, P3-03, P3-04` first; then `P3-05` → `P3-06`; `[P] P3-07` (after
02/03) ; then `[P] P3-08, P3-10, P3-11` → `P3-09` last (it validates everything).

---

**P3-01 — Author `openapi.yaml`**
Scope: OpenAPI 3.1 covering every endpoint, parameter, and schema in SPEC §4.2/§4.3 plus the
`StreamEvent` schemas from §5.1 (SSE documented as a `text/event-stream` response), the problem+json
error schema with the URN `type` enumeration, the pagination envelope, and examples for every
response (the examples double as frontend fixtures).
Files: `server/api/openapi.yaml`, `server/internal/tools/specvalidate/main.go`, `Makefile` (`gen`).
AC: `go run ./internal/tools/specvalidate` loads the spec with kin-openapi and reports zero errors;
every operation has an `operationId`, a 4xx response, and at least one example; a deliberate typo
in a `$ref` makes the validator fail (verify, then revert); CI job `openapi` green.

**P3-02 — Cursor codec, filters, session reads**
Scope: `httpapi/cursor.go` (opaque base64 encode/decode, sort-key binding, tamper rejection);
`postgres/filter.go` whitelist clause builder (placeholders only); `ListSessions` with every filter
and all 4 sort keys, `GetSession` (detail incl. `permission_mode_history`, `top_tools`,
`decision_summary`, `sources_seen`, `raw_events_expired`), `ListTurns`.
Files: `server/internal/httpapi/{cursor.go,cursor_test.go}`,
`server/internal/store/postgres/{filter.go,filter_test.go,read_sessions.go,read_sessions_test.go}`,
`server/db/queries/read_sessions.sql`.
AC: cursor round-trips for all sort keys; a mutated cursor → `ErrInvalidCursor`; filter builder
test feeds every filter permutation and asserts the generated SQL uses only placeholders (a test
asserting no user string appears in the SQL text); integration test with 25 seeded sessions
paginates through them in pages of 7 with zero duplicates and zero omissions **while a new session
is inserted mid-pagination** (the keyset property that offsets lack); `raw_events_expired` true
when a session's `first_seen_at` precedes the oldest partition.

**P3-03 — Event and tool-call reads**
Scope: `ListEvents` (session-scoped and cross-session, `kinds`/`prompt_id`/`agent_id`/`tool`/
`decision_source` filters, `order` asc/desc, `fields=slim|full`, keyset on `(ts, seq)`),
`GetEvent(id)`, `ListToolCalls` (session-scoped and cross-session).
Files: `server/internal/store/postgres/{read_events.go,read_events_test.go,read_toolcalls.go}`,
`server/db/queries/read_events.sql`.
AC: integration tests — ordering is exactly `(ts, vendor_seq NULLS LAST, seq)` including a case
with two events at the identical `ts` distinguished only by `vendor_seq`; `order=desc` returns the
exact reverse of `asc` for the same window; `fields=slim` omits `attrs` and the response is
measurably smaller; keyset pagination across a partition boundary (two months of events) returns
every row exactly once; `EXPLAIN` on the session timeline query shows an index scan on
`events_*(session_id, ts, seq)` and prunes to the relevant partitions.

**P3-04 — Migration `004`, prices, estimation**
Scope: `004_rollups.sql` per SPEC §2.4; `db/prices/model_prices.json` in-repo (Claude model
families, with `effective_from`) and a migration/`argusd prices import` path that seeds it
idempotently; `pricing.Estimate(model, tokens, at)` with exact-match-then-longest-prefix lookup and
an `ErrNoPrice` path (which must leave cost NULL, never zero — a silent zero is a lie).
Files: `server/db/migrations/004_rollups.sql`, `server/db/prices/model_prices.json`,
`server/internal/store/postgres/prices.go`, `server/internal/query/pricing/{pricing.go,pricing_test.go}`,
`server/cmd/argusd/main.go` (`prices import`).
AC: unit table test: exact model match; a versioned suffix resolving by prefix; a model with two
`effective_from` rows resolving by event date; an unknown model → `ErrNoPrice` and no cost;
cache-read/write tokens priced separately; integration test asserts re-importing prices twice is a
no-op.

**P3-05 — Rollup job**
Scope: the SPEC §2.4 job: watermark read, dirty-bucket discovery capped by
`ARGUS_ROLLUP_MAX_BUCKETS`, full per-bucket recompute upsert for `source='event'`, the
`metric_samples` pass with cumulative diffing via `metric_series_state`, `rollup_daily` recompute,
watermark advance in-transaction, `pg_try_advisory_lock` single-flight, scheduler wiring, metrics
(duration, buckets processed, lag).
Files: `server/internal/store/postgres/{rollups.go,rollups_test.go}`,
`server/db/queries/rollups.sql`, `server/internal/app/jobs.go`.
AC: integration tests — after ingesting known events, `rollup_hourly` totals equal the direct
`events` aggregates; running the job twice changes nothing (idempotent); an event inserted with a
`ts` 20 minutes in the past causes exactly its bucket to be recomputed and the total to correct
itself; cumulative metric points produce correct deltas and a counter reset (value decrease) takes
the raw value; a second concurrent job invocation returns immediately without duplicating work;
`source='event'` and `source='metric'` rows for the same bucket coexist and are never summed by
the job.

**P3-06 — Analytics queries**
Scope: `AnalyticsSummary`, `AnalyticsSeries` (dense zero-filled buckets, `group_by`,
`limit_series` + `other` folding), `AnalyticsBreakdown`, and the decisions matrix (counts by
`tool_name` × `decision_source` + `wait_ms` percentiles from `tool_calls`); bucket auto-selection
(`hour` for ≤ 7 d windows, `day` beyond) and the `metrics_only_projects` detection for the banner.
Files: `server/internal/store/postgres/{read_analytics.go,read_analytics_test.go}`,
`server/db/queries/read_analytics.sql`, `server/internal/model/analytics.go`.
AC: integration tests — summary cost equals the rollup sum over the window (exit criterion 4);
series buckets are contiguous with zeros for empty hours and length matches the window;
`group_by=model` with 12 models and `limit_series=8` yields 8 series plus an `other` whose total
closes the gap exactly; `estimated_share` is non-zero for `--cost-mode=omit` data and zero
otherwise; the decisions matrix reproduces all 6 sources with percentiles; a window with no data
returns zeros, not an error or an empty body.

**P3-07 — Session/timeline/tool/subagent handlers**
Scope: chi handlers + request binding/validation for `/api/v1/sessions`, `/{id}`, `/{id}/timeline`,
`/{id}/turns`, `/{id}/tool-calls`, `/{id}/subagents`, `/api/v1/events`, `/api/v1/tool-calls`,
`/api/v1/events/{id}`; time-param parsing (RFC 3339 + relative shorthand); `limit` clamping;
`ETag`/`If-None-Match`; problem+json for every failure mode.
Files: `server/internal/httpapi/{sessions.go,events.go,toolcalls.go,params.go,params_test.go,
sessions_test.go}`, `server/internal/query/{sessions.go,events.go}`.
AC: `httptest` tests against the fake store — 404 problem+json for an unknown session id; `limit=9999`
clamps to 500 (not an error); `from=-7d` parses and `from=garbage` → 400 naming the parameter;
repeated `?project=a&project=b` ORs; `If-None-Match` with a matching ETag → 304 with an empty body;
the pagination envelope shape matches the OpenAPI schema (checked again in P3-09).

**P3-08 — Analytics handlers, `/facets`, `/meta` flags**
Scope: handlers for the four analytics endpoints; `GET /api/v1/facets` (cached 60 s in-process);
`/api/v1/meta` extended with `retention_days`, `vendors[]`, `logs_exporter_seen`,
`metrics_exporter_seen`, `hooks_seen`, `estimated_cost_present`, feature flags.
Files: `server/internal/httpapi/{analytics.go,facets.go,meta.go,analytics_test.go}`,
`server/internal/query/analytics.go`.
AC: `httptest` tests — invalid `metric=` / `bucket=` / `dimension=` values → 400 listing the
allowed values; `source=metric` returns the metric-sourced rows and is never mixed with
`source=event` in one response; `/facets` served from cache on the second call (store spy sees one
call); `/meta` reports `hooks_seen=false` on a database that received only OTLP.

**P3-09 — Fake store + OpenAPI conformance harness**
Scope: `store/testing.Fake` (in-memory, deterministic, enough fidelity for handler tests);
`httpapi/conformance_test.go` — load `openapi.yaml` with kin-openapi, route and validate a table of
~40 requests and their responses against the schemas, including error responses and the SSE frame
schemas via direct schema validation.
Files: `server/internal/store/testing/fake.go`,
`server/internal/httpapi/{conformance_test.go,testdata/requests.yaml}`.
AC: the conformance test passes for every operation in the spec (a test asserts the request table
covers 100 % of `operationId`s — an unlisted operation fails the build); removing a field from a
handler's response makes it fail with a schema error (verify, then revert); coverage floor for
`httpapi` raised to 75 % and enforced.

**P3-10 — Retention, projection rebuild, `EXPLAIN` guard**
Scope: `ApplyRetention(cutoff, dryRun)` dropping fully-expired `events`/`metric_samples`
partitions with the `--precise` batched-delete variant; the daily scheduler entry;
`RebuildProjections(fromSeq)` replaying events in `seq` order into truncated projections (with a
progress log and a resumable watermark); `argusd retention`, `argusd rebuild-projections`
subcommands; the `EXPLAIN` guard test.
Files: `server/internal/store/postgres/{retention.go,retention_test.go,rebuild.go,rebuild_test.go,
explain_test.go}`, `server/internal/app/jobs.go`, `server/cmd/argusd/main.go`.
AC: integration tests — with a fabricated 6-month-old partition, `--dry-run` lists it and changes
nothing, the real run drops it, and `rollup_hourly` plus `sessions` rows for that period survive;
an event whose partition was dropped is rejected at write with the `too_old` counter;
`RebuildProjections` after truncating `sessions`/`turns`/`tool_calls`/`subagents` reproduces
byte-identical projection rows (compared by a checksum query) — this is the test that makes the
whole append-only-log design trustworthy; the `EXPLAIN` guard fails if any analytics query's plan
mentions `events`.

**P3-11 — TS client generation + CI drift**
Scope: `web/package.json` `gen:api` script (`openapi-typescript@7.13.0` → `src/api/schema.d.ts`);
`src/api/client.ts` wrapping `openapi-fetch@0.17.0` with base-URL resolution, the optional API
token, an error mapper turning problem+json into a typed `ApiError`, and abort-signal support; the
CI `openapi` job's drift check.
Files: `web/package.json`, `web/src/api/{client.ts,schema.d.ts,errors.ts,client.test.ts}`,
`.github/workflows/ci.yml`.
AC: `pnpm gen:api && git diff --exit-code web/src/api/schema.d.ts` is clean; `client.test.ts`
(fake fetch) asserts a problem+json 400 becomes an `ApiError` with `type`/`title`/`detail`, that a
successful call returns typed data, and that an aborted request rejects with an abort error;
`pnpm type-check` catches a deliberately wrong field access on a response type (verify, revert).

---

## Phase 4 — UI: session explorer + analytics

**Goal**: the two headline read features, usable against sim data, dark-first.

**Exit criteria** (all verified in a browser against a server loaded by `argusd sim --mode=demo`)
1. `/sessions` lists ≥ 20 sessions; filtering by project and by status changes the result set and
   is reflected in the URL; a reload restores the same filtered view.
2. Clicking a row opens `/sessions/:id` showing a KPI strip whose cost matches the list row.
3. The Timeline tab shows turn-grouped events; every tool call with a decision shows a
   `DecisionBadge` whose tooltip names the provenance (`config`/`user_reject`/…); clicking an event
   opens the drawer with the raw `attrs`.
4. The Subagents tab renders a depth-2 tree with per-node cost and tool counts.
5. `/analytics` renders a cost timeseries, a model breakdown, and the decision matrix; changing the
   date range refetches and the charts re-render; charts follow the theme toggle.
6. With `--cost-mode=omit` data, the estimated-cost notice is visible.
7. Empty database → `/sessions` shows the setup instructions (env vars + hook JSON + sim command),
   not a blank table.
8. `pnpm unit` covers `collapseEvents`, the session store's filter/URL sync, and 3 component
   render tests; `pnpm build` output loads in the embedded-asset server.

| ID | Title | Depends on |
|---|---|---|
| P4-01 | `useApi` composable + `metaStore` + `facets` + fixtures from OpenAPI examples | P3-11 |
| P4-02 | `sessionsStore` + `SessionFilterBar` + `SessionTable` + URL sync | P4-01 |
| P4-03 | `SessionDetailView` shell, `SessionKpiStrip`, tabs, `sessionDetailStore` | P4-01 |
| P4-04 | `Timeline`, `collapseEvents`, `EventRow`, `DecisionBadge`, `EventDetailSheet` | P4-03 |
| P4-05 | `SubagentTree` | P4-03 |
| P4-06 | `ToolCallTable` (session tab + cross-session view) | P4-03 |
| P4-07 | ECharts theme bridge + `TimeSeriesChart` + `BreakdownChart` + `DecisionMatrix` + `StatTile` | P4-01 |
| P4-08 | `AnalyticsView` + `analyticsStore` | P4-07 |
| P4-09 | Empty/error/loading states, setup snippet, estimated-cost notice | P4-02, P4-08 |

Parallel: `P4-01` → `[P] P4-02, P4-03, P4-07` → `[P] P4-04, P4-05, P4-06, P4-08` → `P4-09`.

---

**P4-01 — Data layer and shared fixtures**
Scope: `useApi` composable (loading/error/data, abort on unmount and on re-request, retry-once on
network error); `metaStore` (meta + facets, boot fetch, 5-min refresh); `src/test/fixtures.ts`
generated from the OpenAPI examples (`pnpm gen:fixtures`) so component tests never hand-roll
shapes; `src/lib/format.ts` (cost, tokens with SI suffixes, duration, relative time via `Intl`).
Files: `web/src/composables/{useApi.ts,useApi.test.ts}`, `web/src/stores/meta.ts`,
`web/src/test/{fixtures.ts,setup.ts}`, `web/src/lib/{format.ts,format.test.ts}`, `web/package.json`.
AC: `useApi` tests: abort on unmount cancels the request; a second call supersedes the first (no
stale-write of `data`); an `ApiError` surfaces in `error` and clears on success; `format` tests
cover `$0.0004`, `$12.34`, `1.2M` tokens, `2h 04m`, "3 minutes ago"; fixtures type-check against
`schema.d.ts` (this is the guard that keeps them honest).

**P4-02 — Session list**
Scope: `sessionsStore` (filters ↔ URL query, keyset pagination with "load more", sort, in-place row
updates); `SessionFilterBar` (project/vendor/model/status selects from facets, date range,
debounced search); `SessionTable`/`SessionRow` (cost, tokens, turns, tools, reject rate, duration,
status dot, relative time) with row virtualization above 200 rows; `SessionListView`.
Files: `web/src/stores/sessions.ts`, `web/src/views/SessionListView.vue`,
`web/src/components/session/{SessionFilterBar.vue,SessionTable.vue,SessionRow.vue,StatusDot.vue}`,
`web/src/**/__tests__/sessions*.spec.ts`.
AC: store tests — setting a filter updates the route query and triggers exactly one refetch
(debounced); "load more" appends using `next_cursor` and never duplicates ids; a `has_more=false`
response hides the button; component test renders 3 fixture sessions with correctly formatted cost
and a `reject_rate` badge; a 404/500 renders `ErrorState` with a retry that refetches.

**P4-03 — Session detail shell**
Scope: `sessionDetailStore` (session, turns, tool calls, subagents, timeline pages, per-event cache,
LRU of 3 sessions); `SessionDetailView` with header, `SessionKpiStrip` (cost/tokens/turns/tools/
reject-rate/duration/models), tab routing via the URL (`?tab=timeline|subagents|tools`), a `partial`
badge for stub sessions and a `raw events expired` notice.
Files: `web/src/stores/sessionDetail.ts`, `web/src/views/SessionDetailView.vue`,
`web/src/components/session/SessionKpiStrip.vue`, tests.
AC: tab state survives reload via the query param; a `partial: true` fixture renders the badge and
does not show `NaN`/`Invalid Date` anywhere (explicit assertion — null `started_at` is the common
case); `raw_events_expired: true` renders the notice instead of an empty timeline; navigating away
and back within the LRU does not refetch (store spy).

**P4-04 — Timeline**
Scope: `collapseEvents(events, {window: 2000}) → TimelineItem[]` implementing SPEC §1.5.3(b) as a
pure function; `Timeline` (windowed rendering, sticky turn headers with per-turn cost/tokens, kind
filter chips, infinite scroll on the keyset cursor, `collapse` toggle); `TimelineGroup`; `EventRow`
(icon+color by kind, monospace one-line summary, source pills); `DecisionBadge` (accept/reject ×
source, tooltip explaining provenance, dotted underline + caveat when
`correlation === 'heuristic'`); `EventDetailSheet` with a `JsonViewer` and copy-to-clipboard.
Files: `web/src/components/timeline/*.vue`, `web/src/lib/{collapseEvents.ts,collapseEvents.test.ts,
eventKinds.ts}`, `web/src/components/common/{JsonViewer.vue,CopyBlock.vue}`, tests.
AC: `collapseEvents` tests against sim-derived fixtures — an OTel `tool.result` and a hook
`tool.result` 300 ms apart with the same `tool_use_id` collapse to one item listing 2 sources; the
same pair 5 s apart do not collapse; two different `tool_use_id`s never collapse; events with no
correlation key collapse only on identical `session_id`+`kind` within the window; `collapse=false`
returns the input 1:1; ordering is preserved in every case. Component tests: all 6
`decision_source` values render a distinct label; a `heuristic` badge renders the caveat; the sheet
shows the raw `attrs`.

**P4-05 — Subagent tree**
Scope: `SubagentTree`/`SubagentNode` — recursive component, indent guides, expand/collapse with
state, per-node badges (agent type, tool count, cost, duration bar relative to the session), root
node labelled as the main agent, click-to-filter that scopes the Timeline tab to that `agent_id`.
Files: `web/src/components/subagent/{SubagentTree.vue,SubagentNode.vue}`, tests.
AC: renders a depth-2 fixture with correct nesting and indentation; a `status: unknown` node with a
null `started_at` renders without errors; clicking a node navigates to `?tab=timeline&agent_id=…`
and the store applies the filter; a 50-node fixture renders without exceeding a recursion guard.

**P4-06 — Tool call table**
Scope: `ToolCallTable` (tool, decision + source badge, `wait_ms`, `duration_ms`, success,
file path, correlation indicator; sortable, keyset paginated) used both in the session Tools tab
and at `/tools` as the cross-session decision drill-down (route + view).
Files: `web/src/components/tools/ToolCallTable.vue`, `web/src/views/ToolExplorerView.vue`,
`web/src/router/index.ts`, tests.
AC: renders fixtures with all correlation values and a distinct visual for `hook_only`; sorting by
`wait_ms` desc issues the right query params; a filter link from the analytics decision matrix
lands on `/tools?decision_source=user_reject` with the filter applied; null `wait_ms` renders `—`,
not `0ms` (a zero here would be a factual lie).

**P4-07 — Chart components**
Scope: `src/lib/echartsTheme.ts` reading CSS custom properties via `getComputedStyle` and building
an ECharts theme, re-applied on theme change (watch on `uiStore.theme`); tree-shaken ECharts
registration; `TimeSeriesChart` (line/area, multi-series, brush zoom, shared tooltip, dense-bucket
input); `BreakdownChart` (horizontal bar with share labels); `DecisionMatrix` (heatmap tool ×
`decision_source`, click emits a filter); `StatTile` (value, unit, delta vs previous window).
Files: `web/src/components/analytics/{TimeSeriesChart.vue,BreakdownChart.vue,DecisionMatrix.vue,
StatTile.vue}`, `web/src/lib/{echarts.ts,echartsTheme.ts}`, tests.
AC: a mount test asserts the option object passed to ECharts contains the expected series count and
axis config (assert on the option, not on canvas pixels); toggling the theme produces a different
`backgroundColor`/`textStyle.color` in the regenerated option; `DecisionMatrix` emits a
`{tool_name, decision_source}` payload on cell click; `StatTile` renders a negative delta with the
correct sign and semantic color; charts resize with the container (ResizeObserver stub test).

**P4-08 — Analytics view**
Scope: `analyticsStore` (window preset + custom range, filters, coalesced parallel fetches of
summary/series/breakdowns/decisions, abort on change, URL sync); `AnalyticsView` layout: KPI tiles,
cost timeseries with `group_by` switch, token timeseries, model + project breakdowns, decision
matrix, tool leaderboard, error panel.
Files: `web/src/stores/analytics.ts`, `web/src/views/AnalyticsView.vue`, tests.
AC: store test — changing the range aborts in-flight requests and issues exactly one new set;
`group_by` change refetches only the series, not the summary; an error in one of the four requests
renders that panel's error state while the others still render; URL round-trip restores the window
and filters exactly.

**P4-09 — States, setup snippet, notices**
Scope: `EmptyState`/`ErrorState`/skeleton loaders across all views; the empty-database setup card
(copyable OTel env block and hook JSON, both read from `/api/v1/meta` so the URL is correct, plus
the `argusd sim` command); `EstimatedCostNotice` shown when `estimated_share > 0`; the
"logs exporter appears off" banner driven by `metrics_only_projects`; a global toast for API
failures; 404 view.
Files: `web/src/components/common/{EmptyState.vue,ErrorState.vue,SetupCard.vue,Skeleton*.vue}`,
`web/src/components/analytics/EstimatedCostNotice.vue`, `web/src/views/NotFoundView.vue`, the
views that host them, tests.
AC: with a fake store returning zero sessions, `/sessions` renders `SetupCard` with the endpoint
URL taken from meta and a working copy button; `estimated_share: 0.02` renders the notice with the
percentage; `metrics_only_projects: ["x"]` renders the banner naming the project; every view has a
skeleton state asserted by a test with a pending promise.

---

## Phase 5 — Live view

**Goal**: an in-flight session is watchable in real time, and the firehose shows fleet activity,
with correct behaviour across reconnects and slow clients.

**Exit criteria**
1. With `argusd sim --mode=load --rate=20` running, `/live` shows events arriving continuously and
   `events_per_sec` within 20 % of the sim's rate.
2. Opening `/sessions/:id` for a session the sim is actively generating shows new timeline rows
   appearing without a manual refresh, and the KPI strip counters incrementing.
3. Killing the server and restarting it: the browser reconnects automatically and, when the gap is
   within the replay window, no events are missing (verified by comparing the UI's event count for
   a session against `SELECT count(*)`); when the gap exceeds it, the UI shows a reset notice and
   refetches.
4. A subscriber stalled with a paused debugger does not slow ingestion (`argus_ingest_lag_seconds`
   p99 unchanged) and receives a `lag` frame on resume.
5. `curl -N localhost:8080/api/v1/stream` prints framed SSE with heartbeats; `-H 'Last-Event-ID: N'`
   replays from N.
6. Exactly one `EventSource` per browser tab regardless of navigation (asserted in a store test and
   observable in devtools).

| ID | Title | Depends on |
|---|---|---|
| P5-01 | `internal/stream`: Hub, subscriptions, topics, never-block guarantee | P2-09 |
| P5-02 | SSE HTTP endpoints, framing, heartbeat, Last-Event-ID replay | P5-01, P3-03 |
| P5-03 | Ingest → hub fan-out, `session` debouncing, `stats` frames | P5-01, P2-09 |
| P5-04 | `liveStore`: single EventSource, reconnect, ring buffer, reset handling | P5-02, P4-01 |
| P5-05 | `LiveView`, `LiveFeed`, `ActiveSessionCards`, `HealthStrip` | P5-04, P4-07 |
| P5-06 | Live mode inside session detail + list live badges | P5-04, P4-02, P4-03 |

Parallel: `P5-01` → `[P] P5-02, P5-03` → `P5-04` → `[P] P5-05, P5-06`.

---

**P5-01 — Stream hub**
Scope: `internal/stream` per SPEC §5.3: `Hub` with `Subscribe(topic, filter)`/`Publish`, per-session
topic map, per-subscriber buffered channel with drop-oldest + `dropped` counter, subscriber cap,
`Unsubscribe` with no goroutine leak, Prometheus metrics (subscribers, published, dropped).
Files: `server/internal/stream/{hub.go,subscription.go,filter.go,hub_test.go}`.
AC: `-race` tests — `Publish` with a subscriber whose channel is never read returns within 1 ms
(the never-block guarantee, measured); the overflowed subscriber's `dropped` counter equals the
overflow count and it later receives the newest events; a session-topic subscriber receives only
its session's events (asserted with 3 sessions); 100 concurrent subscribe/unsubscribe cycles leak
no goroutines (`runtime.NumGoroutine` before/after with a settle window); exceeding the cap returns
`ErrTooManySubscribers`.

**P5-02 — SSE endpoints**
Scope: `httpapi/sse.go` — `GET /api/v1/stream` (with `kinds`/`project`/`vendor` filters) and
`GET /api/v1/sessions/{id}/stream`; the frame writer for `event`/`session`/`stats`/`lag`/`reset`/
`shutdown`; `retry:` on open; heartbeat comments; correct headers; `Last-Event-ID`/`from_seq`
replay via `EventsSince` with the **attach-before-query** ordering and `seq` dedupe on flush;
`reset` when outside the replay window; clean teardown on client disconnect and on server
shutdown.
Files: `server/internal/httpapi/{sse.go,sse_test.go}`, `server/internal/httpapi/router.go`.
AC: `httptest` tests — the response has `Content-Type: text/event-stream` and a `retry:` line;
published events appear as `id:`/`event: event`/`data:` frames in order; a heartbeat comment
arrives within the configured interval (with a shortened test config); with `Last-Event-ID` set,
the backlog is replayed once and events published *during* the replay query are delivered exactly
once (this test must be written to actually race — publish from a goroutine while the fake store's
`EventsSince` blocks); an out-of-window id yields a `reset` frame first; cancelling the request
context closes the handler and unsubscribes.

**P5-03 — Ingest fan-out**
Scope: implement the `Publisher` seam from P2-09 with the real hub: publish **only** persisted
events after commit, with the slim projection; per-session 500 ms debounce for `session` frames
built from the projection rows the write touched; a 2 s `stats` broadcaster fed by the pipeline's
metrics (events/sec, active sessions, queue depth, ingest lag, dropped total).
Files: `server/internal/ingest/publish.go`, `server/internal/stream/stats.go`,
`server/internal/app/app.go`, tests.
AC: integration test — a batch containing 10 new and 10 duplicate events publishes exactly 10
frames; a session receiving 50 events in 500 ms produces at most 2 `session` frames; `stats`
frames report a non-zero `events_per_sec` under sim load and zero after it stops; publishing
happens after commit (a test with a failing transaction asserts zero frames — the UI must never
show an event that isn't stored).

**P5-04 — `liveStore`**
Scope: single `EventSource` per tab with reference counting; topic switching (firehose ↔ session)
without dropping frames on the switch; automatic reconnect with exponential backoff and jitter
(cap 30 s) plus `Last-Event-ID` continuity via the browser's native behaviour and an explicit
`from_seq` fallback; capped ring buffer (2000) with pause/resume; `reset` handling that clears
local state and triggers a REST refetch; `lag` handling that refetches; connection state exposed
for the UI indicator.
Files: `web/src/stores/live.ts`, `web/src/lib/sse.ts`, tests with a fake `EventSource`.
AC: store tests — two components subscribing yield one `EventSource`; the last unsubscribe closes
it; a simulated `error` reconnects with increasing delays and stops growing at the cap; a `reset`
frame clears the buffer and calls the refetch callback once; the ring buffer never exceeds 2000
entries under 5000 pushed frames and keeps the newest; `paused` stops buffer mutation but keeps the
connection alive.

**P5-05 — Live view**
Scope: `LiveView` layout; `LiveFeed` (streaming rows reusing `EventRow`, kind filter, pause,
auto-scroll with a "jump to latest" affordance when scrolled up, click → `EventDetailSheet`);
`ActiveSessionCards` (per-session live tiles with sparkline, cost, current tool, "follow" link);
`HealthStrip` (queue depth, ingest lag, dropped total in red when non-zero, exporters seen,
connection state).
Files: `web/src/views/LiveView.vue`, `web/src/components/live/*.vue`,
`web/src/components/layout/HealthStrip.vue`, tests.
AC: feeding 100 fake frames renders rows in reverse-chronological order, capped, without layout
thrash; pause freezes the list and the resume badge shows the number buffered; a non-zero
`dropped_total` renders the red indicator with a tooltip explaining what was lost (visible data
loss is a requirement, not a nicety); a disconnected state renders the reconnect indicator.

**P5-06 — Live mode in the explorer**
Scope: `sessionDetailStore` subscribes to the session topic while mounted and appends live events
into the timeline (respecting the active kind filter and the collapse function) and applies
`session` frames to the KPI strip; `SessionTable` rows update from firehose `session` frames for
rows currently displayed; a live dot on `active` sessions; a "live"/"paused" toggle in the detail
header.
Files: `web/src/stores/{sessionDetail.ts,sessions.ts}`,
`web/src/views/SessionDetailView.vue`, `web/src/components/session/{SessionRow.vue,LiveDot.vue}`,
tests.
AC: store test — a live event for the open session appends exactly once and is not duplicated when
the same `seq` also arrives via a REST page fetch (the dedupe that matters); an event for a
different session is ignored; a `session` frame updates the KPI counters; toggling live off stops
appending but keeps the REST view usable; the timeline stays scroll-anchored when the user has
scrolled up.

---

## Phase 6 — Polish, docs, release

**Goal**: something a stranger can run in two minutes and a reviewer can judge as senior work.

**Exit criteria**
1. A clean clone → `docker compose up -d` → open `localhost:8080` → the README's copy-paste
   Claude Code config produces visible data within one turn. Verified by the owner on a fresh
   machine or a fresh Docker volume, following the README literally with no prior knowledge.
2. `docs/` contains `SPEC.md`, `PLAN.md`, `DECISIONS.md`, `ARCHITECTURE.md` (the diagram + the
   3 non-obvious invariants), and `OPERATIONS.md` (config reference, retention, backup, upgrade).
3. README has screenshots/GIF of the four views and states the differentiator in the first
   paragraph.
4. `v0.1.0` tag → `release.yml` publishes `ghcr.io/<owner>/argus:0.1.0` for amd64+arm64 and binary
   artefacts; `docker run ghcr.io/<owner>/argus:0.1.0 version` prints `0.1.0`.
5. `argusd sim --mode=load --rate=1000 --duration=120s` sustains ingest with p99 write latency and
   ingest lag recorded in `docs/OPERATIONS.md`, and no dropped events at that rate.
6. Coverage floors: `normalize` ≥ 90 %, `store/postgres` ≥ 70 %, `httpapi` ≥ 75 %, web ≥ 60 %,
   all enforced in CI.
7. Keyboard navigation works for the session list → detail → timeline path; `axe` reports no
   critical violations on the four views.

| ID | Title | Depends on |
|---|---|---|
| P6-01 | README + `ARCHITECTURE.md` + `OPERATIONS.md` + screenshots | Phase 5 |
| P6-02 | Release workflow, GHCR, versioning, CHANGELOG | Phase 5 |
| P6-03 | Load test pass: measure, tune, document; index verification | Phase 5 |
| P6-04 | A11y + keyboard + responsive pass | Phase 4, 5 |
| P6-05 | Data-quality surfacing: partial sessions, expired raw, dropped events, `unknown` kinds | Phase 5 |
| P6-06 | Fixture/golden hardening + flake hunt | Phase 5 |
| P6-07 | *(conditional)* `ARGUS_ATTRS_RETENTION_DAYS` — only if P6-03 shows `attrs` dominating storage | P6-03 |

Parallel: `[P] P6-01, P6-02, P6-03, P6-04, P6-05, P6-06` (disjoint file sets) → `P6-07` if
triggered.

---

**P6-01 — Documentation**
Scope: README (what it is + the differentiator in paragraph one; quickstart; the exact env block
and hook JSON; screenshots of the 4 views; what it does *not* do; MIT + a note that it is
unaffiliated with Anthropic); `docs/ARCHITECTURE.md` (the SPEC §0 diagram plus the three
invariants: append-only log + rebuildable projections, publish-after-commit, never-block-publish);
`docs/OPERATIONS.md` (full config reference generated from the config struct by `argusd config
--markdown`, retention behaviour and its coarse partition granularity, backup/restore with
`pg_dump`, upgrade procedure, troubleshooting: no data → check `CLAUDE_CODE_ENABLE_TELEMETRY`,
`CLAUDE_CODE_OTEL_DIAG_STDERR=1`, `/api/v1/meta` exporter flags).
Files: `README.md`, `docs/ARCHITECTURE.md`, `docs/OPERATIONS.md`, `docs/img/*`,
`server/cmd/argusd/main.go` (`config --markdown`).
AC: a link checker passes; `argusd config --markdown` output matches the committed reference
(CI-checked, so the docs cannot drift from the code); the quickstart was executed verbatim from a
clean state and the transcript is in the ticket's report.

**P6-02 — Release**
Scope: `.github/workflows/release.yml` (tag-triggered buildx multi-arch push to GHCR, binary
artefacts for linux/darwin × amd64/arm64, release notes); version stamping via ldflags from the
tag; `CHANGELOG.md` (Keep-a-Changelog); a `latest`/semver tag policy; image labels (OCI source,
revision, license).
Files: `.github/workflows/release.yml`, `CHANGELOG.md`, `server/Dockerfile` (labels), `Makefile`.
AC: a `v0.1.0-rc1` prerelease tag produces the multi-arch image and artefacts; `docker manifest
inspect` shows both platforms; the pulled image's `version` matches the tag; `docker inspect`
shows the OCI labels.

**P6-03 — Load test and tuning**
Scope: run `--mode=load` at 100/500/1000/2000 events/s; record ingest lag, write p50/p99, queue
depth, dropped counts, CPU/RSS, and DB size per million events (with an `attrs`-share breakdown for
OQ-5); verify each index from SPEC §2.5 is actually used at scale (`EXPLAIN (ANALYZE, BUFFERS)` on
the hot reads with a multi-million-row table) and drop any that isn't; tune batch size/workers/pool
size from the measurements; write the numbers into `OPERATIONS.md` and the defaults into config.
Files: `docs/OPERATIONS.md`, `server/internal/config/config.go` (tuned defaults),
`server/db/migrations/00X_indexes.sql` (only if an index proves unnecessary or missing),
`scripts/loadtest.sh`.
AC: the report includes a table of rate → lag/p99/drops; the sustained-rate figure appears in the
README; every index in SPEC §2.5 is either shown used by an `EXPLAIN` output in the report or
removed with a stated reason; no dropped events at 1000 events/s.

**P6-04 — Accessibility and responsiveness**
Scope: focus management (visible focus rings, logical tab order, focus trap in sheets/dialogs);
keyboard shortcuts (`/` search, `j`/`k` list navigation, `Esc` close, `?` help); ARIA roles on
tables/tabs/tree; chart accessible fallbacks (a data table behind a disclosure); responsive
breakpoints down to 1024 px (a laptop is the target — no phone layout); `vitest-axe` checks on the
four views; reduced-motion respect.
Files: `web/src/components/**` (focused edits), `web/src/composables/useShortcuts.ts`,
`web/src/**/__tests__/a11y.spec.ts`.
AC: `vitest-axe` reports zero critical/serious violations on all four views; a keyboard-only walk
from list → detail → open an event → close is possible and asserted in a test; the subagent tree
exposes `role="tree"`/`treeitem` with correct `aria-level`; charts have a toggleable data table.

**P6-05 — Data-quality surfacing**
Scope: a `/api/v1/meta` `data_quality` block (`partial_sessions`, `unknown_kind_events_24h`,
`clock_skewed_events_24h`, `dropped_events_total`, `heuristic_tool_calls_share`,
`oldest_raw_event`) and a UI panel rendering it, plus a `kind='unknown'` inspector view (grouped by
`event_name`, with a raw sample) so a new Claude Code release that adds an event is visible within
minutes rather than silently unmapped.
Files: `server/internal/httpapi/meta.go`, `server/internal/store/postgres/read_quality.go`,
`web/src/views/DataQualityView.vue`, `web/src/components/layout/HealthStrip.vue`,
`web/src/router/index.ts`, tests.
AC: with chaos-mode sim data, every counter is non-zero and the panel renders each with an
explanation of what it means and what to do; `unknown_kind_events_24h > 0` shows a link to the
inspector, which lists the unmapped `event_name`s with counts and one raw payload each;
integration test asserts the query is bounded to 24 h and uses the partition-pruned path.

**P6-06 — Fixture hardening and flake hunt**
Scope: regenerate all Go and web fixtures from one seeded sim run and commit them with the seed
recorded; add the golden determinism test to CI; run `go test -count=5 -race ./...` and
`pnpm unit --repeat 3` to hunt flakes and fix them at the root (never by adding sleeps or retries);
remove any `time.Sleep` in tests in favour of synchronization; raise the coverage floors to the
Phase-6 exit values.
Files: `server/**/testdata/**`, `web/src/test/fixtures.ts`, affected test files,
`scripts/coverage-floors.txt`, `.github/workflows/ci.yml`.
AC: `go test -count=5 -race ./...` green three consecutive runs; `grep -rn 'time.Sleep' --include='*_test.go'`
returns only cases with a written justification comment; fixtures regenerate byte-identically from
the recorded seed; coverage floors at exit values and enforced.

**P6-07 — *(conditional)* `attrs` retention**
Trigger: P6-03 shows `attrs` > 60 % of `events` table size.
Scope: `ARGUS_ATTRS_RETENTION_DAYS` and a daily job that nulls `attrs` on events older than the
cutoff in bounded batches; an `attrs_stripped` boolean so the UI can say so instead of showing an
empty payload.
Files: `server/db/migrations/00X_attrs_retention.sql`,
`server/internal/store/postgres/retention.go`, `server/internal/config/config.go`,
`web/src/components/timeline/EventDetailSheet.vue`, `docs/OPERATIONS.md`, tests.
AC: integration test — with the setting at 7 days, events older than 7 days have `attrs = '{}'` and
`attrs_stripped = true` while their normalized columns are unchanged; the job is batched (no
single statement over 10 k rows) and resumable; the UI shows "raw payload expired" for those
events; default 0 leaves everything untouched.

---

## Cross-phase notes

**Ticket handoff protocol.** Each implementation agent reports: what it built, the exact commands
it ran with their real output, any acceptance criterion it could not meet and why, and any
assumption it had to invent (which becomes a spec amendment, not a silent decision). A ticket whose
acceptance criteria cannot be met as written is stopped and reported — not reinterpreted.

**Commit discipline.** One branch per ticket (`p2-07-toolcall-correlation`), conventional commit
subjects, small commits at every working state, squash-merge to `main` with the ticket ID in the
subject. `main` is always green and always deployable.

**When a phase ends.** Owner review at the boundary. The reviewing session should be handed: the
exit-criteria checklist with real command output for each item, the diff stat, and a screenshot or
transcript per UI criterion. Any exit criterion that was not literally observed is reported as
unmet.
