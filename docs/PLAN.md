# Argus — Phased Implementation Plan

*Companion to `docs/SPEC.md` (revision 3). Phases match the orchestration sequence in
`docs/DECISIONS.md`: spec → scaffold+CI → ingestion → storage/query+API → UI explorer+analytics →
live view → polish/README. Owner reviews at phase boundaries only.*

**Revision 3 (2026-08-12)** — amended for `docs/review/spec-review-1.md` and the live capture
(`docs/research/live-capture-2026-08-11.md`). Mechanical review defects fixed: file collisions
between parallel tickets (M9), Phase-1 CI job count (M8), unreachable or invalid acceptance
criteria (B3, M6, M7, m6, m11). Tickets that were gated on the live capture (P2-02, P2-07) are
**unblocked**; their ACs now assert the verified behaviour.

## How to read this

- Every ticket is sized for **one Sonnet implementation agent in one session**: a bounded file set,
  a stated contract, and acceptance criteria it can verify itself by running something.
- **Files touched** is normative and non-overlapping between tickets marked parallel. If an
  implementer needs a file outside its list, it stops and reports instead of widening scope.
- Acceptance criteria are observable. "Compiles" is never an acceptance criterion.
- `[P]` marks ticket IDs that may run concurrently.
- Every ticket ends with `go test ./...` (server) or `pnpm unit && pnpm type-check && pnpm lint`
  (web) green, plus its own new tests. Implicit, not repeated per ticket.
- Ticket IDs are stable; later tickets reference earlier ones by ID.

**Conventions the implementer must not re-decide**: SPEC §3.1 package layout, §3.2 pinned
dependency versions, §3.3 the `Store` interface, §1.4 the kind taxonomy, §1.6 the lock-ordering
invariant, §1.7 the three idempotency rules, §4.1 API conventions, and §0's rule that **no
vendor-supplied vocabulary is ever constrained** by a CHECK, a Postgres enum, a Go enum, or a
TypeScript union. Any deviation is a report-back, not a judgement call.

**Gate before Phase 1 starts**: OQ-1 is resolved — owner `YohannHommet`, module path
`github.com/YohannHommet/argus/server`, container images lowercase (`ghcr.io/yohannhommet/argus`).

---

## Phase 1 — Scaffold + CI (walking skeleton)

**Goal**: a public repo where `docker compose up` serves an empty-but-real UI backed by a migrated
Postgres, and CI is green — before any feature exists.

**Exit criteria**
1. `make ci` passes locally; the GitHub Actions `ci.yml` run on `main` is green for **all jobs
   defined in this phase** (`go-lint`, `go-test`, `go-build`, `web`, `compose` — five; the
   `openapi` job arrives in Phase 3 with the spec and the generator it needs).
2. `docker compose -f deploy/docker-compose.yml up -d` → `curl -fsS localhost:8080/readyz` returns
   `200 {"status":"ok","migrations":"current"}`.
3. `curl -fsS localhost:8080/api/v1/meta` returns `{"version":"…","commit":"…","retention_days":90,…}`.
4. `http://localhost:8080/` serves the Vue SPA from embedded assets, in dark mode, with a working
   theme toggle and a sidebar containing the six routes.
5. `psql \dt` shows `sessions`, `turns`, `ingest_dedup`, `goose_db_version`.
6. LICENSE (MIT) and a README with the quickstart skeleton are committed.

| ID | Title | Depends on |
|---|---|---|
| P1-01 | Repo scaffold, license, Makefile (**sole owner of `Makefile` in this phase**) | — |
| P1-02 | Go module, `argusd` subcommand skeleton, config, logging | P1-01 |
| P1-03 | Web app scaffold: Vite + Vue + Tailwind 4 + shadcn-vue + router + Pinia + dark mode | P1-01 |
| P1-04 | Store skeleton: pgxpool, embedded goose migrations, `001_core.sql`, `Store` interface | P1-02 |
| P1-05 | HTTP skeleton: chi, middleware, problem+json, ops endpoints, `/api/v1/meta`, embedded SPA | P1-02, P1-04 |
| P1-06 | CI workflows (five jobs), golangci-lint config, dependabot | P1-02, P1-03 |
| P1-07 | Dockerfile, docker-compose, **`scripts/smoke.sh` (sole owner)** | P1-03, P1-05 |

Parallel: `[P] P1-02, P1-03` → `P1-04` → `P1-05` → `[P] P1-06, P1-07`.

> **File-ownership note (review M9).** `Makefile` is written once, completely, by P1-01 — including
> the targets later tickets use (`ci`, `lint`, `compose-up`, `compose-smoke`, `gen`, `sim`). No
> other Phase-1 ticket edits it. `scripts/smoke.sh` belongs to P1-07 only; P1-06 *references* it
> from the workflow but does not create or edit it. `README.md` is created by P1-01 with the
> quickstart placeholder; P1-07 does not touch it (the real quickstart text lands in P6-01).

---

**P1-01 — Repo scaffold, license, Makefile**
Scope: directory skeleton; MIT LICENSE (owner's personal identity); README with the quickstart
outline and a "status: pre-alpha" note; `.gitignore` (Go, node, dist, .env); `.editorconfig` and
`.gitattributes` (LF enforced — never CRLF); the **complete** `Makefile` (`dev`, `build`, `test`,
`lint`, `ci`, `gen`, `migrate`, `sim`, `compose-up`, `compose-smoke`); `CONTRIBUTING.md` stub;
empty `server/`, `web/`, `deploy/`, `scripts/`.
Files: `LICENSE`, `README.md`, `.gitignore`, `.editorconfig`, `.gitattributes`, `Makefile`,
`CONTRIBUTING.md`.
AC: `make` with no target prints the target list; targets whose inputs do not exist yet fail with a
clear "not implemented until <ticket>" message rather than a confusing error; `git ls-files` shows
no build artefacts; `.gitattributes` sets `* text=auto eol=lf`.

**P1-02 — Go module, subcommand skeleton, config, logging**
Scope: `go.mod` (module path per SPEC §3.1 using the **answered OQ-1** slug; `go 1.25`);
`cmd/argusd/main.go` dispatching `serve|migrate|sim|retention|rebuild-projections|prices|config|
version|healthcheck` (stubs allowed except `version`, `config`, `healthcheck`); `internal/config`
implementing **the complete key table in SPEC §3.7** via koanf (defaults ← YAML ← `ARGUS_` env),
validation, `--config`, `config --print` with secret redaction, `config --markdown`, and an
unknown-`ARGUS_*` warning; `internal/telemetry` slog setup (`json`/`text` via tint) and build-info
vars set by ldflags.
Files: `server/go.mod`, `server/go.sum`, `server/cmd/argusd/main.go`,
`server/internal/config/{config.go,config_test.go}`,
`server/internal/telemetry/{log.go,buildinfo.go}`.
AC: a table-driven test asserts **every key in SPEC §3.7** has the documented default (a test that
enumerates the struct and fails when a key is missing — this is how the table stays honest), plus
env-overrides-YAML, invalid duration → error, missing `ARGUS_DATABASE_URL` → error naming the key;
`go run ./cmd/argusd version` prints version+commit+go version; `config --print` redacts
`ARGUS_DATABASE_URL` and both tokens; `config --markdown` output round-trips into the same key set;
`argusd nonsense` exits 2 with usage.

**P1-03 — Web app scaffold**
Scope: pnpm project (`vue@3.5.41`, `vite@8.2.1`, `typescript@7.0.2`, `vue-router@5`, `pinia@4`,
`tailwindcss@4.3.3` via `@tailwindcss/vite`, `vitest@4.1.10`, `@vue/test-utils`, eslint flat config,
`vue-tsc`); `shadcn-vue` init with 4 primitives (button, card, badge, switch);
`src/assets/theme.css` with the full token set from SPEC §6.1 including Argus semantic tokens;
`AppShell.vue` (sidebar with the **six** nav items from SPEC §6.2, topbar, `ThemeToggle`); `uiStore`
with persisted theme plus the anti-flash inline script in `index.html`; six placeholder route views
+ `NotFoundView`; Vite dev proxy for `/api`, `/v1`, `/ingest`.
Files: everything under `web/` (`package.json`, `pnpm-lock.yaml`, `vite.config.ts`, `tsconfig*.json`,
`eslint.config.js`, `index.html`, `src/main.ts`, `src/App.vue`, `src/router/`, `src/stores/ui.ts`,
`src/assets/theme.css`, `src/components/layout/`, `src/components/ui/`, `src/views/`,
`src/**/__tests__/`).
AC: `pnpm build` emits `web/dist`; `pnpm unit` passes with a test asserting `uiStore.toggle()` flips
`document.documentElement.classList` between dark/light and persists to `localStorage`, and a mount
test of `AppShell` asserting **six** nav links resolving to the six routes; `pnpm type-check` and
`pnpm lint` clean; dark is the default with no `localStorage` entry and no `prefers-color-scheme`
match.

**P1-04 — Store skeleton and `001_core.sql`**
Scope: `internal/store/store.go` with the **complete** interface from SPEC §3.3 (every method
declared; unimplemented postgres methods return `ErrNotImplemented` so later tickets fill them in
without touching the interface); `internal/store/postgres` with pgxpool construction (max conns,
app_name, statement cache), `Health`, `Close`, goose-over-`embed.FS` `Migrate` guarded by
`pg_advisory_lock`; `db/migrations/001_core.sql` exactly as SPEC §2.1 (**including `sessions.status`,
`cost_by_query_source`, the eight `sessions_*` indexes, `turns` with `cost_estimated_usd` and no
`cost_source`, and `ingest_dedup`**); `internal/store/testing` harness providing Postgres via
`ARGUS_TEST_DATABASE_URL` or testcontainers, with an isolated schema per test.
Files: `server/internal/store/{store.go,errors.go}`,
`server/internal/store/postgres/{pool.go,migrate.go,migrations_embed.go,pool_test.go}`,
`server/internal/store/testing/harness.go`, `server/db/migrations/001_core.sql`,
`server/internal/model/{session.go,turn.go,eventref.go}`.
AC: integration test brings up Postgres, runs `Migrate`, and asserts: `sessions`/`turns`/
`ingest_dedup` exist with the expected columns; all eight `sessions_*` indexes exist (queried from
`pg_indexes` by name); **no `CHECK` constraint exists on any vendor-vocabulary column** (a query over
`pg_constraint` asserting zero rows for `sessions`/`turns` — SPEC §0); two concurrent `Migrate`
calls from two goroutines both succeed (advisory lock); `Migrate` is idempotent; `argusd migrate
status` prints applied versions; the harness gives two parallel tests non-colliding schemas.

**P1-05 — HTTP skeleton and embedded SPA**
Scope: `internal/httpapi` with chi router; middleware chain (request id, recoverer, sampled slog
access log, real-ip, timeout, optional CORS, `RequireIngestToken`/`RequireAPIToken` no-op-when-empty
seams); RFC 9457 problem+json helper with the URN scheme; `GET /healthz`, `/readyz`, `/metrics`,
`GET /api/v1/meta`; SPA serving from `go:embed` with SPA-fallback routing, correct content types,
immutable cache headers on hashed assets and `no-cache` on `index.html`; `internal/app` wiring
config → store → httpapi and `serve` with the SPEC §3.8 shutdown sequence; `argusd healthcheck`.
Files: `server/internal/httpapi/{router.go,middleware.go,problem.go,ops.go,meta.go,assets.go,
router_test.go,assets_test.go}`, `server/internal/app/{app.go,serve.go}`,
`server/cmd/argusd/main.go` (wire `serve`, `healthcheck`).
AC: `httptest` tests — `/healthz` 200 without a DB; `/readyz` 503 problem+json when the DB is down
and 200 when up; unknown `/api/v1/nope` → 404 `application/problem+json` with a URN `type`;
`/frontend/route` → 200 `text/html`; `/assets/x.js` gets `Cache-Control: immutable`; with
`ARGUS_API_TOKEN` set, `/api/v1/meta` is 401 without the bearer and 200 with it; a shutdown test
asserts an in-flight request completes and `Shutdown` returns before the grace deadline.

**P1-06 — CI, lint config, dependabot**
Scope: `.github/workflows/ci.yml` with **five** jobs (`go-lint`, `go-test`, `go-build`, `web`,
`compose`) per SPEC §8.3 — the `openapi` job is deliberately absent until P3-01/P3-11 create
`openapi.yaml` and `gen:api`; `.golangci.yml` (v2 schema, the enable/disable lists and the
`depguard` import-direction rules from SPEC §8.4, `exhaustive` scoped to `Kind` only);
`.github/dependabot.yml`; `scripts/coverage-floor.sh` + `scripts/coverage-floors.txt` (floors set to
current reality, raised by later tickets).
Files: `.github/workflows/ci.yml`, `.github/dependabot.yml`, `.golangci.yml`,
`scripts/{coverage-floor.sh,coverage-floors.txt}`.
AC: a pushed branch shows all five jobs green in Actions; `golangci-lint run` clean locally;
deliberately importing `internal/httpapi` inside `internal/store` makes `go-lint` fail with a
depguard error (verify, then revert); `scripts/coverage-floor.sh` fails when a floor is raised above
actual coverage; the workflow file contains no job referencing a path that does not exist.

**P1-07 — Dockerfile, compose, smoke**
Scope: multi-stage `server/Dockerfile` per SPEC §8.1; `.dockerignore`;
`deploy/docker-compose.yml` per SPEC §8.2 (postgres **18**-alpine, healthchecks, named volume, no
published DB port); `deploy/docker-compose.dev.yml` (publishes 5432, `build:` instead of `image:`);
`deploy/docker-compose.override.example.yml`; `scripts/smoke.sh` (bring the stack up, poll `/readyz`
for 60 s, assert `/api/v1/meta` JSON, tear down including the volume).
Files: `server/Dockerfile`, `.dockerignore`, `deploy/*.yml`, `scripts/smoke.sh`.
AC: `docker build -f server/Dockerfile .` succeeds and the image is < 40 MB; `docker run --rm <img>
version` prints the stamped version (not `dev`); `bash scripts/smoke.sh` exits 0 from a clean state;
the container runs as uid 65532 (`docker inspect`); a `psql -c "select uuidv7()"` inside the compose
network succeeds (proves the PG-18 pin is real before Phase 2 depends on it); the `compose` CI job
is green.

---

## Phase 2 — Ingestion

**Goal**: both wire surfaces accept real traffic, normalize it, and persist it durably and
idempotently with all four projections correct — verified by the simulator against a live server,
and by fixtures extracted from the real live capture.

**Exit criteria**
1. `argusd sim --mode=demo --sessions=5 --flush-immediately --target http://localhost:8080` exits 0
   with all HTTP statuses 2xx.
2. `SELECT count(*), count(DISTINCT session_id) FROM events` > 0 and matches the sim's reported send
   count minus reported dedup suppressions.
3. `SELECT kind, count(*) FROM events GROUP BY 1` shows ≥ 14 distinct kinds (the taxonomy now
   includes the three `hook.*` kinds) and `unknown` = 0 with chaos flags off.
4. `SELECT count(*) FROM sessions WHERE started_at IS NULL` = 0 after a clean run; > 0 with
   `--chaos-orphans`, returning to fully-populated once the late `SessionStart` arrives.
5. Re-running the same seeded sim (**same `--seed` and `--clock-origin`**) twice inserts zero new
   rows on the second run, and `argus_ingest_deduped_total` equals the resent count. This holds for
   **hook** events too, which is what the `ingest_dedup` ledger exists for.
6. `SELECT decision_source, count(*) FROM tool_calls GROUP BY 1` shows all 6 documented sources plus
   the sim's invented one, and `SELECT count(*) FROM tool_calls WHERE correlation='exact'` > 0.
7. `SELECT * FROM subagents` shows depth-2 rows with non-null `parent_agent_id`, `cost_usd IS NULL`
   on every row, and non-null `tool_call_count`.
8. `SELECT jsonb_object_keys(cost_by_query_source) FROM sessions` includes at least one value Argus
   has no constant for (proving the unconstrained-vocabulary path works end to end).
9. Load mode above capacity returns 503/429 with `Retry-After`, never panics, and RSS stays bounded
   (`--rate=5000 --duration=60s`, watched via `/metrics`).

| ID | Title | Depends on |
|---|---|---|
| P2-01 | `internal/model`: canonical event, kind taxonomy, dedup keys, `EventRef` | P1-02 |
| P2-02 | Normalizer: Claude Code OTel log events (incl. the 3 `hook_*` events) | P2-01 |
| P2-03 | Normalizer: Claude Code hook events | P2-01 |
| P2-04 | Normalizer: OTLP metrics → `metric_samples` | P2-01 |
| P2-05 | Migrations `002`/`003` + partition manager | P1-04 |
| P2-06 | `WriteBatch`: dedup ledger, event insert, session/turn projections, `rollup_dirty` | P2-01, P2-05 |
| P2-07 | Tool-call projection + correlation | P2-06, P2-02, P2-03 |
| P2-08 | Subagent projection + tree edges | P2-06, P2-03 |
| P2-09 | Ingest pipeline: queue, batcher, workers, backpressure, retry classes, metrics | P2-01, P2-06 |
| P2-10 | OTLP/HTTP receiver endpoints | P2-02, P2-04, P2-09 |
| P2-11 | Hooks webhook endpoint | P2-03, P2-09 |
| P2-12 | `argus-sim` core generator + wire output | P2-10, P2-11 |
| P2-13 | Chaos modes + end-to-end ingestion integration test | P2-12, P2-07, P2-08 |

Parallel: `[P] P2-02, P2-03, P2-04, P2-05` after P2-01 → `P2-06` → `[P] P2-07, P2-08, P2-09` →
`[P] P2-10, P2-11` → `P2-12` → `P2-13`.

> **File-ownership note (review M9).** P2-10 and P2-11 both need to mount routes. Neither edits
> `httpapi/router.go`: each ingest package exposes `func Mount(r chi.Router, …)` in its own
> `mount.go`, and `router.go` gained the two (already-written) call sites in P1-05 behind an
> `ingest.Mounter` interface with a no-op default. If P1-05's call sites are missing, P2-10 adds
> them and P2-11 does not.

---

**P2-01 — `internal/model`**
Scope: `Event` struct with every column from SPEC §1.3 (pointers for nullables, consistently);
`Kind` as a defined string type with one constant per SPEC §1.4 row (**including `hook.registered`,
`hook.execution_start`, `hook.execution_end`; and *no* `metric.sample`**) plus `KindUnknown`,
`AllKinds()`, `Valid()`; `Source` constants; **free-form vendor vocabularies as plain `string` with
no constant sets that can reject a value** (SPEC §0/§1.9); projection structs (`SessionSummary`,
`SessionDetail`, `Turn`, `ToolCall`, `SubagentTree`, `Facets`, `Summary`, `Series`, `Breakdown`,
`DecisionMatrix`, `DataQuality`, `UnknownKindGroup`, `HookLatency`); `EventRef` encode/decode
(base64url of `ts`+`seq`); `DedupKey` helpers for the three forms plus the `otel` hash-fallback form
(SPEC §1.7 rule 2) with canonical-JSON hashing; `ClampTimestamp` taking the retention window.
Files: `server/internal/model/*.go` + tests.
AC: tests assert every `Kind` constant is in `AllKinds()` and JSON round-trips; **no `metric.sample`
kind exists**; canonical JSON hashing is stable across map iteration order (100 iterations, same
hash) and across key insertion order; the `otel` dedup key with and without `vendor_seq` produce the
documented two forms and differ; `EventRef` round-trips and rejects tampered input;
`ClampTimestamp` table covers in-window, older-than-retention, 2 h future, zero time — with the
lower bound driven by the passed retention window, not a constant; `exhaustive` is configured for
`Kind` and passes.

**P2-02 — Normalizer: OTel log events**
Scope: `internal/ingest/normalize` — `FromOTLPLogs(*logspb.LogsData) ([]model.Event, []Rejection)`
implementing the SPEC §1.5.1 table for all 18 mapped event names plus the `unknown` fallback;
**event-name resolution `EventName` → `event.name` attr → `body`, then strip the `claude_code.`
prefix** (the capture shows `event.name` is unprefixed while `body` is prefixed); resource+scope+
record attribute merging (record wins) with resource attrs kept under a `resource.` prefix;
`vendor` from `service.name`; `app_version` falling back to resource `service.version`;
`TimeUnixNano` vs `event.timestamp` preference and skew flagging; `cost_usd_micros` preference;
`query_source` passthrough **as unconstrained text**; full payload into `attrs`; missing `session.id`
→ `Rejection` (the only rejection case).
Files: `server/internal/ingest/normalize/{otel_logs.go,attrs.go,eventname.go,otel_logs_test.go}`,
`server/internal/ingest/normalize/testdata/otel/*.json` (**including fixtures extracted from
`docs/research/live-capture-2026-08-11.md`'s raw logs**).
AC: one table-driven case **per row** of the §1.5.1 mapping asserting every promoted column; a case
with the **unprefixed** `event.name` and a case with only the prefixed `body` both normalize to the
same `event_name`; a `tool_decision` fixture taken from the live capture yields a non-null
`tool_use_id` (this is the assertion that pins review finding M10's resolution into the test suite);
an `api_request` fixture from the capture yields `query_source="generate_session_title"` stored
verbatim with **no error and no mapping**, and `agent_id` NULL; `hook_execution_complete` maps
`total_duration_ms → duration_ms` and computes `success`; an unknown `event.name` → `KindUnknown`
with `event_name` preserved and `attrs` intact; an unknown *attribute* lands in `attrs` without
error; a record with no `session.id` is rejected while others in the batch are still returned;
record-vs-resource key collision → record wins; skew case sets `clock_skewed`.

**P2-03 — Normalizer: hook events**
Scope: `FromHookPayload([]byte) ([]model.Event, error)` implementing the SPEC §1.5.2 table for all
listed `hook_event_name` values plus `unknown`; defensive field reads (missing → nil, never error);
`MessageDisplay` dropped unless configured; accepts a single object or an array; the full body always
into `attrs`.
Files: `server/internal/ingest/normalize/{hooks.go,hooks_test.go}`, `…/testdata/hooks/*.json`.
AC: one case per hook event; a payload with only `session_id` + `hook_event_name` normalizes without
error; unknown `hook_event_name` → `KindUnknown`; missing `session_id` → error (400 material);
`PermissionDenied` yields `decision=reject` and `decision_source='unknown'` (asserting we do **not**
invent provenance); `MessageDisplay` returns zero events by default and one when enabled; an array
body yields N events; a payload with an unrecognised `permission_mode` string passes it through
verbatim.

**P2-04 — Normalizer: OTLP metrics**
Scope: `FromOTLPMetrics(*metricspb.MetricsData) ([]model.MetricSample, []Rejection)` — walk
resource/scope/metric, support `Sum` (delta and cumulative), `Gauge`, `Histogram` (store `sum`/`count`
as two samples); `series_hash` over name + sorted attribute pairs; temporality mapping; store-anything
policy (unknown names stored, not rolled up); `session.id` when present.
Files: `server/internal/ingest/normalize/{otel_metrics.go,otel_metrics_test.go}`,
`…/testdata/metrics/*.json`.
AC: cases for each of the 7 `claude_code.*` metrics with their documented attribute sets;
`series_hash` stable, and different when one attribute value changes; cumulative vs delta labelled
correctly; a histogram yields the `_sum`/`_count` pair; a metric with no `session.id` is accepted
with `session_id = NULL`; an unknown metric name is accepted.

**P2-05 — Migrations `002`/`003` + partition manager**
Scope: `002_events.sql` and `003_projections.sql` exactly as SPEC §2.2/§2.3 — **`events` declares
`UNIQUE (ts, dedup_key)` on the parent** (a per-partition unique index does not satisfy
`ON CONFLICT`; reproduced on PG 18.4), includes `query_source`, and there is **no `DEFAULT`
partition**; `postgres.EnsurePartitions(from, to)` creating monthly partitions plus **6**
per-partition indexes for `events` (the unique index is inherited — do not create it) and 2 for
`metric_samples`, idempotently; the hourly partition job (current month + 2 ahead).
Files: `server/db/migrations/{002_events.sql,003_projections.sql}`,
`server/internal/store/postgres/{partitions.go,partitions_test.go}`, `server/internal/app/jobs.go`.
AC: integration tests — after `EnsurePartitions` over a 3-month range, `pg_indexes` shows exactly
the 6 created indexes **plus** the inherited `*_ts_dedup_key_key` per partition; an
`INSERT … ON CONFLICT (ts, dedup_key) DO NOTHING` against the parent **succeeds** (the direct
regression test for review blocker B1, which fails with *"there is no unique or exclusion constraint
matching the ON CONFLICT specification"* if the constraint is put on the partitions instead);
calling `EnsurePartitions` twice is a no-op; inserting an event with a `ts` outside all ranges
**errors** (no `DEFAULT` partition to swallow it) and the error is classifiable as `too_old`;
`uuidv7()` defaults work and the test fails with a clear message on a pre-18 server.

**P2-06 — `WriteBatch`: dedup ledger, events, session/turn projections**
Scope: the single-transaction write from SPEC §1.6/§1.7 in the **fixed lock order**
(`ingest_dedup` → `sessions` → `turns` → `events` → `tool_calls` → `subagents` → `rollup_dirty`),
rows sorted by PK within each statement: the `ingest_dedup` `INSERT … ON CONFLICT DO NOTHING
RETURNING` gate; event insert with the parent-level `ON CONFLICT` as defence in depth;
stub-on-reference session/turn upserts with **stored `status`**; `field_ranks` precedence;
aggregate counters (`event_count`, tokens, `cost_usd`, `cost_estimated_usd`,
**`cost_by_query_source` jsonb accumulation keyed by the raw value**, `turn_count`, `models` union);
`turn_index` assignment; `rollup_dirty` marking for every touched hour **and** the
project-change re-mark (capped by `ARGUS_ROLLUP_SESSION_REMARK_MAX`); `too_old` classification;
`BatchResult` with persisted/deduped/rejected splits. Batched via `pgx.Batch` (`CopyFrom` is
unusable with `ON CONFLICT`; document the choice).
Files: `server/internal/store/postgres/{write.go,dedup.go,upsert_session.go,upsert_turn.go,
dirty.go,write_test.go}`, `server/db/queries/write.sql`, `sqlc.yaml`,
generated `server/internal/store/postgres/gen/*.go`.
AC: integration tests —
(a) 500 mixed events insert in one tx and `sessions.event_count` matches;
(b) re-writing the identical batch persists 0 and reports 500 deduped, **including hook-sourced
events whose `ts` differs between the two deliveries** (the direct regression test for review
blocker B2: same `dedup_key`, `ts` 1 s apart, `count(*) = 1`);
(c) a `turn.start` arriving **after** its turn's `llm.request` events yields correct `started_at`
and correct token sums;
(d) an `otel_log` write does not overwrite a `cwd` previously set by a `hook` write (rank), while
the reverse does;
(e) status is a **stored column**: a stub row reads `unknown`, becomes `active` on
`session.start`, `ended` on `session.end`, and `SweepAbandoned` moves a stale `active` row to
`abandoned` — asserted by reading the column, no derivation SQL needed (review m11);
(f) a batch failure rolls back completely (no partial events, no counter drift, no dedup-ledger
rows left behind — inject a constraint violation);
(g) **two concurrent overlapping batches** (sessions {A,B} and {B,A}) both commit with no dropped
batch and no `40P01` escaping the retry, run 20× under `-race` (review M13);
(h) `cost_by_query_source` accumulates an unseen value (`"a_future_query_source"`) as a plain key;
(i) writing a batch whose events fall in 3 different hours marks exactly 3 `rollup_dirty` rows, in
the same transaction (assert by reading `rollup_dirty` inside an uncommitted concurrent tx? no —
assert after commit, plus a test that a rolled-back batch leaves **zero** dirty rows).

**P2-07 — Tool-call projection + correlation**
Scope: `correlateToolCall` and the `tool_calls` upsert: **deterministic UUIDv5 `id`** per SPEC §1.6;
create-or-update on `(session_id, tool_use_id)`; the 4 `correlation` values; the
hook-without-`tool_use_id` one-to-one nearest-open-call heuristic within 60 s; `wait_ms`/`duration_ms`
derivation; `input_size_bytes`/`result_size_bytes` read from `attrs->>'tool_input_size_bytes'` /
`'tool_result_size_bytes'` (review m2); decision precedence (`tool_decision` > `tool_result` > hook);
`session.tool_call_count`/`tool_reject_count` maintenance.
Files: `server/internal/ingest/normalize/correlate.go`,
`server/internal/store/postgres/{upsert_toolcall.go,upsert_toolcall_test.go}`,
`server/db/queries/toolcalls.sql`.
AC: integration cases — pre+decision+result all with `tool_use_id` → one row, `correlation=exact`
when a hook also matched and `otel_only` when not; **a decision-provenance case built from the live
capture's `tool_decision` payload resolves to `correlation != 'heuristic'`** (pinning the verified
exact join); hooks-only without `tool_use_id` → `hook_only`; a hook without `tool_use_id` alongside
**three concurrent `Edit` calls in one turn** matches exactly one call each, never two (one-to-one
assertion); a hook arriving 5 minutes late does not match (`hook_only` row);
`tool_decision source=user_reject` overrides a `decision_source` previously written by
`tool_result`; an unrecognised `source` value is stored verbatim; `wait_ms` computed from
`PreToolUse`→`tool_decision`; `input_size_bytes`/`result_size_bytes` populated from a live-capture
`tool_result` fixture; the same input replayed twice produces the **same `id`** (determinism, which
P3-10's rebuild test depends on).

**P2-08 — Subagent projection**
Scope: `subagents` upsert from `subagent.start`/`subagent.stop`; `parent_agent_id` from the payload
when present, else inferred from the spawning hook `tool.*` event carrying `agent_type` in the same
turn (documented best-effort); `depth` from the parent chain with a cap; `status` derivation;
`tool_call_count` from **hook-sourced** tool events carrying `agent_id`, left `NULL` when the session
has no hook coverage; `cost_usd`/`input_tokens`/`output_tokens` **left NULL always** (SPEC §1.9);
`SubagentTree` read — recursive CTE (depth cap 16) + nesting pass + the synthetic `root` node, plus
the `cost_attribution` block assembled from `sessions.cost_by_query_source`.
Files: `server/internal/store/postgres/{upsert_subagent.go,subagent_tree.go,subagent_test.go}`,
`server/db/queries/subagents.sql`, `server/internal/model/subagent.go`.
AC: integration tests — a 2-level tree returns nested `children` with correct `depth`; a
`SubagentStop` without a matching start creates a row with `status='unknown'` and
`started_at IS NULL`; a `parent_agent_id` cycle terminates at the depth cap, logs, and does not hang;
**every node has `cost_usd IS NULL`, and the response's `cost_attribution.per_node_available` is
`false`** (review B3 — the old "root aggregates = session minus children" assertion is gone because
it was unimplementable); `tool_call_count` is `NULL` for a session with only OTel events and non-null
once hook tool events with `agent_id` exist; `cost_attribution.by_query_source` reproduces the
session's jsonb map including unknown keys.

**P2-09 — Ingest pipeline**
Scope: `internal/ingest/pipeline.go` per SPEC §3.6: bounded batch channel, N workers, size/time
flush, non-blocking enqueue returning `ErrQueueFull`, **retry classification** (`40P01`/`40001` up to
`ARGUS_INGEST_RETRY_CONFLICT`; transient up to `ARGUS_INGEST_RETRY_TRANSIENT`; `23…`/`42…` never
retried), all Prometheus metrics (queue depth, batch size, per-source counters, dedup, write
duration, retry counters by class, `argus_ingest_lag_seconds`, dropped, `too_old`), `Close()` draining
within a deadline, and a `Publisher` seam for the stream hub (no-op here).
Files: `server/internal/ingest/{pipeline.go,pipeline_test.go,retry.go,retry_test.go,metrics.go}`,
`server/internal/app/{app.go,serve.go}`.
AC: unit tests with a fake store — enqueue beyond capacity returns `ErrQueueFull` without blocking
(timeout-asserted); batches flush on size and on the timer; a `40P01` error retries up to 8 times
and then succeeds on the 8th (no data loss); a `23505` error is **not** retried and increments
`class="permanent"`; a transient error retries 3× then drops; `Close()` drains queued batches before
returning and errors if the deadline is hit; `-race` clean with 8 concurrent producers; a store that
blocks forever leaks no goroutines after `Close`.

**P2-10 — OTLP/HTTP receiver**
Scope: `internal/ingest/otlp` handlers for `POST /v1/logs`, `/v1/metrics`, `/v1/traces` per SPEC
§3.4: content negotiation (protobuf / `protojson` with `DiscardUnknown`), gzip with a decompressed
size cap, `MaxBytesReader`, correct `Export*ServiceResponse` bodies, `partial_success`, 503 +
`Retry-After` on queue-full, 400 `Status` on malformed input, 415 on unknown content type,
`RequireIngestToken`; a `mount.go` exposing `Mount(r chi.Router, …)`.
Files: `server/internal/ingest/otlp/{logs.go,metrics.go,traces.go,codec.go,mount.go,handler_test.go}`.
AC: `httptest` tests — a protobuf `ExportLogsServiceRequest` and the equivalent JSON body produce
byte-identical normalized events; the response unmarshals as `ExportLogsServiceResponse`; 3 records
where 1 lacks `session.id` → 200 with `partial_success.rejected_log_records = 1` and 2 events
enqueued; a 20 MiB body → 413; a gzip bomb decompressing past the cap → 413 with bounded memory;
malformed protobuf → 400 with a `Status` body; `/v1/traces` → 200 empty + discard counter;
queue-full → 503 with `Retry-After: 1`; a payload containing an unknown protobuf field (simulating a
future OTLP version) is accepted.

**P2-11 — Hooks webhook**
Scope: `internal/ingest/hooks` handler for `POST /ingest/hook`: JSON only, single object or array,
validate `session_id`, normalize, enqueue, `202 {"ok":true,"event":"<hook_event_name>"}` with **no
store access**; 429 + `Retry-After` on queue-full with the drop counter; body cap;
`argus_hook_handler_duration_seconds`; `RequireIngestToken`; its own `mount.go`.
Files: `server/internal/ingest/hooks/{handler.go,mount.go,handler_test.go}`.
AC: `httptest` tests — a `SessionEnd` payload returns 202 in under 20 ms with a fake pipeline, and a
store-spy test asserts **zero** store calls from the handler (the 1.5 s shared `SessionEnd` budget
is the reason this endpoint exists in this shape); missing `session_id` → 400 problem+json;
queue full → 429 with `Retry-After` and `argus_ingest_dropped_total{source="hook"}` incremented; an
array of 5 payloads enqueues 5 events; a 2 MiB body → 413; the response body never contains a
decision/permission field (Argus is observe-only).

**P2-12 — `argus-sim` core generator + wire output**
Scope: `internal/sim` implementing SPEC §7 minus chaos flags, honouring the **fidelity rule**: no
`agent_id` on `api_request`/`tool_result`; `query_source` drawn from the mixed distribution
including live-observed, documented-but-unobserved, and invented values; **both the prefixed `body`
and the unprefixed `event.name`**; zero-based dense per-session `event.sequence`;
`tool_use_id` on `tool_decision` by default; `hook_registered` / `hook_execution_*` emission;
`tool_input_size_bytes` / `tool_result_size_bytes`; the built-in price table; simulated clock with
**`--clock-origin`** (fixed epoch default under `--out`/`--deterministic`), `--speed`, `--backfill`;
OTLP protobuf/JSON encoding; hook POSTs; `--seed` determinism via `math/rand/v2.PCG` derived per
session; `--out=dir/`; `--mode=demo|load`; `--rate`, `--concurrency`, `--duration`, `--sessions`,
`--cost-mode`, `--tool-details`, `--tool-use-id-in-hooks`, `--tool-use-id-in-decision`; the exit
report; `cmd/argus-sim` shim; the `sim` subcommand wired.
Files: `server/internal/sim/*.go`, `server/cmd/argus-sim/main.go`,
`server/cmd/argusd/main.go` (wire `sim`).
AC: `argusd sim --out=/tmp/f --seed=7 --sessions=3` twice produces **byte-identical** files (golden
test in CI over a 1-session run) — which is only true because `--clock-origin` defaults to a fixed
epoch under `--out` (review M7); the emitted payloads round-trip through the P2-02/P2-04 normalizers
with zero `unknown` kinds and zero rejections (a unit test wiring generator → normalizer, no server);
a test asserts **no emitted OTel `api_request`/`tool_result` payload contains `agent_id`** (review
B3's simulator half — this is the test that prevents the demo from lying); a test asserts at least
one emitted `query_source` value is outside any Argus constant; against a live server,
`--mode=demo --sessions=5 --flush-immediately` exits 0 with an all-2xx histogram;
`--mode=load --rate=200 --duration=10s` reports throughput within 15 % of target.

**P2-13 — Chaos modes + end-to-end ingestion test**
Scope: the 5 chaos flags from SPEC §7.1; a build-tagged integration test starting the real app
against real Postgres, running the sim in-process, and asserting every Phase-2 exit criterion as SQL
assertions; `scripts/smoke.sh` extended to run the demo sim and assert row counts; the `normalize`
coverage floor raised to 90 %.
Files: `server/internal/sim/chaos.go`, `server/internal/app/e2e_ingest_test.go`,
`scripts/smoke.sh`, `scripts/coverage-floors.txt`.
AC: the e2e test asserts, in one run: ≥ 14 kinds; `unknown` = 0 without chaos; duplicates suppressed
with the counter matching **for hook events specifically**; `--chaos-orphans` produces `partial`
sessions that complete when the late `SessionStart` lands **and** re-marks their rollup buckets;
`--chaos-clock-skew` sets `clock_skewed`, and its beyond-retention event is rejected with
`argus_ingest_too_old_total` incremented (review M6 — this is now reachable because the clamp is tied
to retention and there is no `DEFAULT` partition); `--chaos-unknown` produces `kind='unknown'` rows
with `event_name` preserved and no rejections; all 6 documented `decision_source` values plus the
invented one present; depth-2 subagents present with NULL per-node cost. CI runs it in `go-test`.

---

## Phase 3 — Storage/query + REST API

**Goal**: the full v1 read API, spec-first, backed by rollups, with the OpenAPI contract enforced and
the TS client generated.

**Exit criteria**
1. Spec validation passes on `server/api/openapi.yaml`, and the conformance test validates ~50
   request/response pairs covering 100 % of `operationId`s.
2. After a demo sim run: `curl '…/api/v1/sessions?limit=2'` returns 2 sessions with a `next_cursor`
   that fetches the next 2 with no overlap and no gap.
3. `curl '…/api/v1/sessions/{id}/timeline?fields=slim&limit=50'` returns events ordered by
   `(ts, vendor_seq, seq)`; `?order=desc` reverses exactly; `event_name` values are unprefixed.
4. `curl '…/api/v1/events/{ref}'` returns that one event **with** `attrs`, and its `EXPLAIN` shows a
   PK lookup on a single partition.
5. `curl '…/api/v1/analytics/summary?from=-14d'` returns non-zero cost equal to
   `SELECT sum(cost_reported_usd + cost_estimated_usd) FROM rollup_hourly WHERE source='event'` over
   the same window.
6. `curl '…/api/v1/analytics/summary?model=claude-sonnet-4-5'` returns `null` (not `0`) for
   `sessions`/`turns`/`tool_calls` and lists them in `not_attributable[]`.
7. `--cost-mode=omit` data yields `estimated_share > 0` and non-zero `cost.estimated_usd`.
8. `EXPLAIN` guard test: no analytics endpoint's plan references `events` (data-quality and
   hook-latency queries are allow-listed and window-bounded).
9. A rollup race test proves the transactional dirty queue works: two interleaved-commit
   transactions both end up counted.
10. `pnpm gen:api` produces a committed `schema.d.ts`; CI's `openapi` job (new in this phase) is
    green.
11. `argusd retention --dry-run` lists the partitions it would drop; the real run drops them and
    leaves rollups intact; `ingest_dedup` is pruned to the window.

| ID | Title | Depends on |
|---|---|---|
| P3-01 | Author `openapi.yaml` for the whole v1 surface + add the CI `openapi` job (spec lint) | Phase 2 |
| P3-02 | Cursor codec, filters, `ListSessions`/`GetSession`/`ListTurns` | P2-06 |
| P3-03 | `ListEvents`, `GetEvent(ref)`, `ListToolCalls` | P2-06, P2-07 |
| P3-04 | Migration `004`, price table + seed, cost estimation | P2-05 |
| P3-05 | Rollup job (hourly + daily, event + metric, `rollup_dirty`-driven) | P3-04 |
| P3-06 | Analytics queries: summary, timeseries, breakdown, decisions | P3-05 |
| P3-07 | Session/timeline/event/tool/subagent HTTP handlers | P3-01, P3-02, P3-03 |
| P3-08 | Analytics handlers + `/facets` + `/meta` + `/quality/*` | P3-01, P3-06, P2-13 |
| P3-09 | Fake store + OpenAPI conformance harness | P3-01, P3-07, P3-08 |
| P3-10 | Retention, dedup pruning, `rebuild-projections`, `EXPLAIN` guard | P3-05 |
| P3-11 | TS client generation + CI drift check | P3-01 |

Parallel: `P3-01` and `[P] P3-02, P3-03, P3-04` first; then `P3-05` → `P3-06`; `P3-07` (after
02/03); then `[P] P3-08, P3-10, P3-11` → `P3-09` last (it validates everything).

---

**P3-01 — Author `openapi.yaml` (+ the `openapi` CI job)**
Scope: OpenAPI 3.1 covering every endpoint, parameter, and schema in SPEC §4.2/§4.3 plus the
`StreamEvent` schemas from §5.1, the problem+json error schema with the URN enumeration, the
pagination envelope, and examples for every response (they double as frontend fixtures). **Every
vendor-supplied vocabulary is typed `string`, never an `enum`** — a generated TS union would make an
unseen `query_source` a type error (SPEC §4.4). Nullable counters are `nullable: true`. Adds the
`openapi` job to `ci.yml` with the spec-lint step only (the client-drift step lands in P3-11) —
this is the job P1-06 deliberately omitted (review M8).
Files: `server/api/openapi.yaml`, `server/internal/tools/specvalidate/main.go`,
`.github/workflows/ci.yml` (add the `openapi` job).
AC: `go run ./internal/tools/specvalidate` reports zero errors; every operation has an
`operationId`, a 4xx response, and ≥1 example; a grep-based test asserts **no `enum:` appears on
`query_source`, `decision_source`, `tool_source`, `terminal_type`, `start_type`, or
`permission_mode`**; a deliberate `$ref` typo fails the validator (verify, revert); the `openapi`
job is green in Actions.

**P3-02 — Cursor codec, filters, session reads**
Scope: `httpapi/cursor.go` (opaque base64, sort-key binding, tamper rejection);
`postgres/filter.go` whitelist clause builder (placeholders only); `ListSessions` with every filter
and all 4 sort keys (using the §2.1 indexes); `GetSession` detail (incl.
`permission_mode_history`, `top_tools`, `decision_summary` with `exact_share`, `sources_seen`,
`raw_events_expired`, `hook_latency`); `ListTurns`.
Files: `server/internal/httpapi/{cursor.go,cursor_test.go}`,
`server/internal/store/postgres/{filter.go,filter_test.go,read_sessions.go,read_sessions_test.go}`,
`server/db/queries/read_sessions.sql`.
AC: cursor round-trips for all sort keys; a mutated cursor → `ErrInvalidCursor`; the filter-builder
test feeds every permutation and asserts the SQL contains only placeholders (no user string appears
in the SQL text); integration test with 25 seeded sessions paginates in pages of 7 with zero
duplicates and zero omissions **while a new session is inserted mid-pagination**;
`EXPLAIN` for each of the 4 sorts uses the matching `sessions_*` index (review M1 — the indexes now
exist to be used); `status` filtering uses the stored column; `raw_events_expired` is true when a
session's `first_seen_at` precedes the oldest partition.

**P3-03 — Event and tool-call reads**
Scope: `ListEvents` (session-scoped and cross-session; `kinds`/`prompt_id`/`agent_id`/`tool`/
`decision_source` filters; `order`; `fields=slim|full`; keyset on `(ts, seq)`);
**`GetEvent(ref model.EventRef)`** as a `(ts, seq)` PK lookup; `ListToolCalls` (both scopes).
Files: `server/internal/store/postgres/{read_events.go,read_events_test.go,read_toolcalls.go}`,
`server/db/queries/read_events.sql`.
AC: integration tests — ordering is exactly `(ts, vendor_seq NULLS LAST, seq)`, including two events
at the identical `ts` distinguished only by `vendor_seq`; `order=desc` is the exact reverse;
`fields=slim` omits `attrs` and the response is measurably smaller; keyset pagination across a
partition boundary (two months) returns every row exactly once; `GetEvent` by `event_ref` returns
the right event with `attrs` and its `EXPLAIN` shows an **Index Scan on the PK of one partition**
(review M2 — no `events.id` index exists, so a uuid lookup would have been a full scan);
`EXPLAIN` on the session timeline shows an index scan on `events_*(session_id, ts, seq)` with
partition pruning.

**P3-04 — Migration `004`, prices, estimation**
Scope: `004_rollups.sql` per SPEC §2.4 (**incl. `rollup_dirty`; no `query_source` dimension**);
`db/prices/model_prices.json` + an idempotent `argusd prices import`; `pricing.Estimate(model,
tokens, at)` with exact-match-then-longest-prefix lookup and an `ErrNoPrice` path that leaves cost
**NULL, never zero** (a silent zero is a lie).
Files: `server/db/migrations/004_rollups.sql`, `server/db/prices/model_prices.json`,
`server/internal/store/postgres/prices.go`,
`server/internal/query/pricing/{pricing.go,pricing_test.go}`, `server/cmd/argusd/main.go`.
AC: unit table test — exact model match; a versioned suffix resolving by prefix; two
`effective_from` rows resolving by event date; unknown model → `ErrNoPrice` and NULL cost;
cache-read/write priced separately; integration test asserts a second `prices import` is a no-op.

**P3-05 — Rollup job**
Scope: the SPEC §2.4 job — advisory lock, claim dirty buckets by `DELETE … RETURNING` inside the
transaction, unconditional current+previous hour, full per-bucket recompute upsert for
`source='event'` (joining `sessions` for `project`), the `metric_samples` pass with cumulative
diffing via `metric_series_state`, `rollup_daily` recompute, single-transaction commit, scheduler
wiring, metrics. **No `events.seq` watermark anywhere.**
Files: `server/internal/store/postgres/{rollups.go,rollups_test.go}`, `server/db/queries/rollups.sql`,
`server/internal/app/jobs.go`.
AC: integration tests — rollup totals equal direct `events` aggregates; running twice changes
nothing; an event inserted 20 minutes in the past causes exactly its bucket to be recomputed and the
total to self-correct; **the concurrency test for review blocker B4**: open two transactions, have
them insert events into the same and different hours, commit them in the *reverse* order of their
`seq` allocation, run the job between the commits, and assert that after the second commit the
earlier transaction's bucket is still corrected (with a `seq` watermark this test fails, which is the
point); a rolled-back job leaves the dirty rows intact for the next run; **the late-project test for
review M4**: events arrive before `SessionStart`, the job runs and attributes them to `project=''`,
then the `SessionStart` hook lands and the next job run moves them to the real project;
cumulative metric points produce correct deltas and a value decrease takes the raw value; a second
concurrent job invocation returns immediately; `source='event'` and `source='metric'` rows coexist
and are never summed.

**P3-06 — Analytics queries**
Scope: `AnalyticsSummary`, `AnalyticsSeries` (dense zero-filled buckets, `group_by`, `limit_series`
+ `other`), `AnalyticsBreakdown` (incl. `dimension=query_source`), `AnalyticsDecisions` (counts by
`tool_name` × `decision_source` + `wait_ms` percentiles + `exact_share`); bucket auto-selection;
`metrics_only_projects` detection; **the model-attributability rule** — non-attributable counters
returned as `null` with `not_attributable[]` populated, and a `400` for `metric=sessions` under a
model filter.
Files: `server/internal/store/postgres/{read_analytics.go,read_analytics_test.go}`,
`server/db/queries/read_analytics.sql`, `server/internal/model/analytics.go`.
AC: integration tests — summary cost equals the rollup sum over the window; series buckets are
contiguous with zeros for empty hours and the length matches the window; `group_by=model` with 12
models and `limit_series=8` yields 8 series plus an `other` closing the gap exactly;
**`?model=X` returns `null` for `sessions`/`turns`/`tool_calls`/`loc`/`active_seconds` and lists
them in `not_attributable[]`, and `timeseries?metric=sessions&model=X` returns 400** (review M3);
`estimated_share` non-zero for `--cost-mode=omit` data and zero otherwise; the decisions matrix
reproduces all 6 documented sources **plus an unseen one as its own key** and reports
`exact_share = 1.0` for OTel-derived decisions; `breakdown?dimension=query_source` returns raw keys;
an empty window returns zeros, not an error.

**P3-07 — Session/timeline/event/tool/subagent handlers**
Scope: chi handlers + binding/validation for `/api/v1/sessions`, `/{id}`, `/{id}/timeline`,
`/{id}/turns`, `/{id}/tool-calls`, `/{id}/subagents`, `/api/v1/events`, **`/api/v1/events/{ref}`**,
`/api/v1/tool-calls`; time-param parsing (RFC 3339 + relative shorthand); `limit` clamping;
`ETag`/`If-None-Match`; problem+json everywhere.
Files: `server/internal/httpapi/{sessions.go,events.go,toolcalls.go,params.go,params_test.go,
sessions_test.go}`, `server/internal/query/{sessions.go,events.go}`.
AC: `httptest` tests against the fake store — 404 problem+json for an unknown session id and for an
unknown `event_ref`; a malformed `event_ref` → 400 `urn:argus:error:invalid-event-ref`;
`limit=9999` clamps to 500 (not an error); `from=-7d` parses and `from=garbage` → 400 naming the
parameter; repeated `?project=a&project=b` ORs; `If-None-Match` with a matching ETag → 304 with an
empty body; subagent responses carry `cost_attribution.per_node_available: false`.

**P3-08 — Analytics handlers, `/facets`, `/meta`, `/quality/*`**
Scope: handlers for the four analytics endpoints; `GET /api/v1/facets` (incl. `query_sources[]`,
cached 60 s in-process); `/api/v1/meta` extended with `retention_days`, `vendors[]`,
`logs_exporter_seen`, `metrics_exporter_seen`, `hooks_seen`, `tool_details_seen`,
`estimated_cost_present`, the `data_quality` block, feature flags; `GET /api/v1/quality/unknown-kinds`
and `GET /api/v1/quality/hook-latency` with their store queries.
Files: `server/internal/httpapi/{analytics.go,facets.go,meta.go,quality.go,analytics_test.go}`,
`server/internal/query/{analytics.go,quality.go}`,
`server/internal/store/postgres/{read_quality.go,read_quality_test.go}`.
AC: `httptest` tests — invalid `metric=`/`bucket=`/`dimension=` → 400 listing allowed values;
`source=metric` returns metric-sourced rows and is never mixed with `source=event` in one response;
`/facets` served from cache on the second call (store spy sees one call); `/meta` reports
`hooks_seen=false` on a database that received only OTLP and `tool_details_seen=false` when no event
carries `tool_parameters`; `/quality/unknown-kinds` groups by `event_name` with a raw sample and is
bounded to the requested window; `/quality/hook-latency` returns percentiles per `hook_event` from
`hook.execution_end` events.

**P3-09 — Fake store + OpenAPI conformance harness**
Scope: `store/testing.Fake` (in-memory, deterministic, enough fidelity for handler tests);
`httpapi/conformance_test.go` — load `openapi.yaml` with kin-openapi, route and validate ~50
requests and their responses, including error responses and the SSE frame schemas by direct schema
validation.
Files: `server/internal/store/testing/fake.go`,
`server/internal/httpapi/{conformance_test.go,testdata/requests.yaml}`.
AC: the conformance test passes for every operation, with a meta-assertion that the request table
covers 100 % of `operationId`s (an unlisted operation fails the build); removing a field from a
handler's response fails with a schema error (verify, revert); a response containing an unknown
`query_source` string validates (it must, since the schema is `string`); the `httpapi` coverage
floor is raised to 75 % and enforced.

**P3-10 — Retention, dedup pruning, rebuild, `EXPLAIN` guard**
Scope: `ApplyRetention(cutoff, dryRun)` dropping fully-expired partitions with a `--precise`
batched-delete variant; `PruneDedup(cutoff)`; the daily scheduler entry;
`RebuildProjections(fromTS)` replaying events in `(ts, seq)` order into truncated projections
(progress log, resumable watermark in `job_state`); `argusd retention` and
`argusd rebuild-projections`; the `EXPLAIN` guard test with its documented allow-list.
Files: `server/internal/store/postgres/{retention.go,retention_test.go,rebuild.go,rebuild_test.go,
explain_test.go}`, `server/internal/app/jobs.go`, `server/cmd/argusd/main.go`.
AC: integration tests — with a fabricated 6-month-old partition, `--dry-run` lists it and changes
nothing, the real run drops it, and `rollup_hourly` plus `sessions` rows for that period survive;
**an event with `ts` older than the retention cutoff is rejected at write with
`argus_ingest_too_old_total` incremented** (review M6 — restated from the old, unreachable
"partition was dropped" phrasing, and reachable now that the clamp is retention-tied and no
`DEFAULT` partition exists); `PruneDedup` removes rows older than `ARGUS_DEDUP_WINDOW` in bounded
batches and leaves newer ones; **`RebuildProjections` after truncating the four projection tables
reproduces identical rows compared by a full-row checksum including `tool_calls.id`** — possible
because those ids are deterministic UUIDv5 (review m6); the `EXPLAIN` guard fails if any analytics
query's plan mentions `events`, and passes for the allow-listed quality queries.

**P3-11 — TS client generation + CI drift**
Scope: `web/package.json` `gen:api` (`openapi-typescript@7.13.0` → `src/api/schema.d.ts`);
`src/api/client.ts` wrapping `openapi-fetch@0.17.0` with base-URL resolution, the optional API token,
a problem+json → typed `ApiError` mapper, and abort support; add the client-drift step to the
`openapi` CI job created in P3-01.
Files: `web/package.json`, `web/src/api/{client.ts,schema.d.ts,errors.ts,client.test.ts}`,
`.github/workflows/ci.yml` (drift step only).
AC: `pnpm gen:api && git diff --exit-code web/src/api/schema.d.ts` is clean; `client.test.ts` (fake
fetch) asserts a problem+json 400 becomes an `ApiError` with `type`/`title`/`detail`, a success
returns typed data, and an aborted request rejects with an abort error; `pnpm type-check` catches a
deliberately wrong field access (verify, revert); a type-level test asserts `query_source` is `string`
and **not** a union, so an unseen value compiles.

---

## Phase 4 — UI: session explorer + analytics

**Goal**: the headline read features, usable against sim data, dark-first, honest about nulls.

**Exit criteria** (verified in a browser against a server loaded by `argusd sim --mode=demo`)
1. `/sessions` lists ≥ 20 sessions; filtering by project and status changes the result set, is
   reflected in the URL, and survives a reload.
2. Clicking a row opens `/sessions/:id` with a KPI strip whose cost matches the list row.
3. The Timeline tab shows turn-grouped events (with out-of-turn events under their own header); every
   tool call with a decision shows a `DecisionBadge` whose tooltip names the provenance; clicking an
   event opens the drawer with raw `attrs` fetched from `/events/{ref}`.
4. The Subagents tab renders a depth-2 tree with per-node tool counts, **`—` for per-node cost with
   the explanatory tooltip**, and a `CostAttributionCard` showing the `by_query_source` split
   including a value like `sdk`.
5. `/tools` lists cross-session tool calls and is reachable by clicking a cell in the analytics
   decision matrix, arriving with the filter applied.
6. `/analytics` renders a cost timeseries, a model breakdown, and the decision matrix; changing the
   date range refetches; charts follow the theme toggle; a model filter renders `—` tiles rather
   than zeros.
7. `/data-quality` renders the quality tiles, the unknown-kind table, and the hook-latency panel.
8. With `--cost-mode=omit` data, the estimated-cost notice is visible.
9. Empty database → `/sessions` shows the setup instructions (env vars incl.
   `OTEL_LOG_TOOL_DETAILS=1`, the hook JSON, the sim command), not a blank table.
10. `pnpm unit` covers `collapseEvents`, the session store's filter/URL sync, the null-rendering
    rule, the unknown-vocabulary rule, and ≥3 component render tests; `pnpm build` output loads from
    the embedded-asset server.

| ID | Title | Depends on |
|---|---|---|
| P4-01 | `useApi` + `metaStore` + fixtures from OpenAPI examples + formatters + `NullValue`/`RawValue` | P3-11 |
| P4-02 | `sessionsStore` + `SessionFilterBar` + `SessionTable` + URL sync | P4-01 |
| P4-03 | `SessionDetailView` shell, `SessionKpiStrip`, tabs, `sessionDetailStore` | P4-01 |
| P4-04 | `Timeline`, `collapseEvents`, `EventRow`, `DecisionBadge`, `EventDetailSheet` | P4-03 |
| P4-05 | `SubagentTree` + `CostAttributionCard` | P4-03 |
| P4-06 | `ToolCallTable` + `/tools` view + `toolsStore` | P4-03 |
| P4-07 | ECharts theme bridge + `TimeSeriesChart` + `BreakdownChart` + `DecisionMatrix` + `StatTile` | P4-01 |
| P4-08 | `AnalyticsView` + `analyticsStore` | P4-07 |
| P4-09 | `DataQualityView` + `qualityStore` + `HookLatencyPanel` + `UnknownKindTable` | P4-01, P4-07 |
| P4-10 | Empty/error/loading states, setup card, estimated-cost + metrics-only notices | P4-02, P4-08 |

Parallel: `P4-01` → `[P] P4-02, P4-03, P4-07` → `[P] P4-04, P4-05, P4-06, P4-08, P4-09` → `P4-10`.

---

**P4-01 — Data layer, shared fixtures, honesty primitives**
Scope: `useApi` (loading/error/data, abort on unmount and on re-request, retry-once on network
error); `metaStore` (meta + facets, boot fetch, 5-min refresh); `src/test/fixtures.ts` generated from
the OpenAPI examples (`pnpm gen:fixtures`) so component tests never hand-roll shapes;
`src/lib/format.ts` (cost, tokens with SI suffixes, duration, relative time via `Intl`);
`common/NullValue.vue` (renders `—` + a reason tooltip) and `common/RawValue.vue` (renders an
unknown vendor string verbatim with neutral styling) — the two components that make SPEC §6.1's
product rules mechanical rather than aspirational.
Files: `web/src/composables/{useApi.ts,useApi.test.ts}`, `web/src/stores/meta.ts`,
`web/src/test/{fixtures.ts,setup.ts}`, `web/src/lib/{format.ts,format.test.ts}`,
`web/src/components/common/{NullValue.vue,RawValue.vue}` + tests, `web/package.json`.
AC: `useApi` tests — abort on unmount cancels; a second call supersedes the first (no stale write of
`data`); an `ApiError` surfaces in `error` and clears on success. `format` tests cover `$0.0004`,
`$12.34`, `1.2M` tokens, `2h 04m`, "3 minutes ago". `NullValue` renders `—` and its tooltip text;
`RawValue` renders `"a_future_query_source"` unchanged and does not throw. Fixtures type-check
against `schema.d.ts`.

**P4-02 — Session list**
Scope: `sessionsStore` (filters ↔ URL query, keyset pagination with "load more", sort, in-place row
updates); `SessionFilterBar` (project/vendor/model/status selects from facets, date range, debounced
search); `SessionTable`/`SessionRow` (cost, tokens, turns, tools, reject rate, duration, status dot,
relative time) with virtualization above 200 rows; `SessionListView`.
Files: `web/src/stores/sessions.ts`, `web/src/views/SessionListView.vue`,
`web/src/components/session/{SessionFilterBar.vue,SessionTable.vue,SessionRow.vue,StatusDot.vue}`,
`web/src/**/__tests__/sessions*.spec.ts`.
AC: store tests — setting a filter updates the route query and triggers exactly one (debounced)
refetch; "load more" appends via `next_cursor` and never duplicates ids; `has_more=false` hides the
button. Component tests — 3 fixture sessions render with correctly formatted cost and a reject-rate
badge; a `partial: true` row renders without `NaN`/`Invalid Date`; a 500 renders `ErrorState` with a
retry that refetches.

**P4-03 — Session detail shell**
Scope: `sessionDetailStore` (session, turns, tool calls, subagents, timeline pages, per-event cache,
LRU of 3); `SessionDetailView` with header, `SessionKpiStrip`, tab routing via the URL
(`?tab=timeline|subagents|tools`), a `partial` badge and a `raw events expired` notice.
Files: `web/src/stores/sessionDetail.ts`, `web/src/views/SessionDetailView.vue`,
`web/src/components/session/SessionKpiStrip.vue`, tests.
AC: tab state survives reload; a `partial: true` fixture renders the badge and shows no
`NaN`/`Invalid Date` anywhere (explicit assertion — null `started_at` is the common case);
`raw_events_expired: true` renders the notice instead of an empty timeline; back-navigation within
the LRU does not refetch (store spy).

**P4-04 — Timeline**
Scope: `collapseEvents(events, {window: 2000}) → TimelineItem[]` as a pure function implementing
SPEC §1.5.3(b); `Timeline` (windowed rendering, sticky turn headers with per-turn cost/tokens, an
explicit "no turn" group for `prompt_id === null` events, kind filter chips, infinite scroll on the
cursor, `collapse` toggle); `TimelineGroup`; `EventRow`; `DecisionBadge`; `EventDetailSheet` fetching
`/events/{ref}` with a `JsonViewer` and copy-to-clipboard.
Files: `web/src/components/timeline/*.vue`,
`web/src/lib/{collapseEvents.ts,collapseEvents.test.ts,eventKinds.ts}`,
`web/src/components/common/{JsonViewer.vue,CopyBlock.vue}`, tests.
AC: `collapseEvents` tests against sim-derived fixtures — an OTel and a hook `tool.result` 300 ms
apart with the same `tool_use_id` collapse to one item listing 2 sources; the same pair 5 s apart do
not; two different `tool_use_id`s never collapse; events with no correlation key collapse only on
identical `session_id`+`kind` within the window; `collapse=false` returns the input 1:1; ordering is
preserved in every case. Component tests — all 6 documented `decision_source` values render distinct
labels **and an unknown value renders verbatim via `RawValue`**; a non-exact correlation renders the
caveat; `eventKinds.ts` has an icon/label for every `Kind` including the three `hook.*` kinds and
`unknown`; the sheet shows raw `attrs` fetched by `event_ref`.

**P4-05 — Subagent tree + cost attribution**
Scope: `SubagentTree`/`SubagentNode` (recursive, indent guides, expand/collapse state, badges for
agent type / tool count / duration bar, **`NullValue` for cost with the §1.9 tooltip**, root node
labelled as the main agent, click-to-filter scoping the Timeline tab to that `agent_id`);
`CostAttributionCard` rendering `by_query_source` as a table of raw keys with the honest caveat text.
Files: `web/src/components/subagent/{SubagentTree.vue,SubagentNode.vue,CostAttributionCard.vue}`,
tests.
AC: renders a depth-2 fixture with correct nesting; **every node's cost renders `—` with the tooltip
"Claude Code does not emit per-agent cost"**, and no test asserts a per-node number (review B3);
`tool_call_count: null` renders `—`, `0` renders `0`; a `status: unknown` node with null `started_at`
renders without errors; `CostAttributionCard` renders keys `""` (as "unattributed"), `sdk`, and an
invented value without special-casing; clicking a node navigates to `?tab=timeline&agent_id=…` and
the store applies the filter; a 50-node fixture renders within the recursion guard.

**P4-06 — Tool explorer**
Scope: `ToolCallTable` (tool, decision + source badge, `wait_ms`, `duration_ms`, success, file path,
correlation indicator; sortable, keyset paginated) used in the session Tools tab and at `/tools`;
`toolsStore`; the route.
Files: `web/src/components/tools/ToolCallTable.vue`, `web/src/views/ToolExplorerView.vue`,
`web/src/stores/tools.ts`, `web/src/router/index.ts`, tests.
AC: renders fixtures with all four correlation values and a distinct visual for `hook_only`; sorting
by `wait_ms` desc issues the right query params; a filter link from the decision matrix lands on
`/tools?decision_source=user_reject` with the filter applied; `wait_ms: null` renders `—`, **not
`0ms`** (a zero here would be a factual lie).

**P4-07 — Chart components**
Scope: `src/lib/echartsTheme.ts` reading CSS custom properties via `getComputedStyle` and rebuilding
the ECharts theme on `uiStore.theme` change; tree-shaken registration; `TimeSeriesChart`;
`BreakdownChart`; `DecisionMatrix` (heatmap, click emits a filter); `StatTile` (value, unit, delta vs
previous window, `—` for null).
Files: `web/src/components/analytics/{TimeSeriesChart.vue,BreakdownChart.vue,DecisionMatrix.vue,
StatTile.vue}`, `web/src/lib/{echarts.ts,echartsTheme.ts}`, tests.
AC: mount tests assert the option object passed to ECharts (series count, axis config) rather than
canvas pixels; toggling the theme changes `backgroundColor`/`textStyle.color` in the regenerated
option; `DecisionMatrix` emits `{tool_name, decision_source}` on cell click and renders an unknown
source as its own row/column; `StatTile` with `value: null` renders `—` plus the reason and does not
render a delta; charts resize with the container (ResizeObserver stub).

**P4-08 — Analytics view**
Scope: `analyticsStore` (window presets + custom range, filters, coalesced parallel fetches of
summary/series/breakdowns/decisions, abort on change, URL sync); `AnalyticsView` layout: KPI tiles,
cost timeseries with `group_by` switch, token timeseries, model + project breakdowns, decision
matrix, tool leaderboard, error panel.
Files: `web/src/stores/analytics.ts`, `web/src/views/AnalyticsView.vue`, tests.
AC: store test — changing the range aborts in-flight requests and issues exactly one new set;
`group_by` change refetches only the series; an error in one of four requests renders that panel's
error state while the others render; URL round-trip restores window and filters; **with a model
filter active, the non-attributable tiles render `—` and the view does not request
`timeseries?metric=sessions`** (which would 400).

**P4-09 — Data-quality view**
Scope: `qualityStore`; `DataQualityView` with `QualityTiles` (partial sessions, unknown-kind events
24 h, clock-skewed 24 h, dropped total, heuristic tool-call share, oldest raw event),
`UnknownKindTable` (grouped `event_name` + count + raw sample viewer), `HookLatencyPanel` (per
`hook_event` percentiles).
Files: `web/src/views/DataQualityView.vue`, `web/src/stores/quality.ts`,
`web/src/components/quality/*.vue`, tests.
AC: every tile renders with an explanation of what it means and what to do about it; a non-zero
dropped count renders in the warn color; `UnknownKindTable` lists an unmapped `event_name` with its
count and opens the raw sample in the `JsonViewer`; `HookLatencyPanel` renders percentiles and an
empty state when no `hook.execution_end` events exist.

**P4-10 — States, setup card, notices**
Scope: `EmptyState`/`ErrorState`/skeletons across all views; the empty-database `SetupCard`
(copyable OTel env block **including `OTEL_LOG_TOOL_DETAILS=1`** and the hook JSON with
`SessionEnd` timeout 1, both using the endpoint URL from `/api/v1/meta`, plus the `argusd sim`
command); `EstimatedCostNotice` when `estimated_share > 0`; the "logs exporter appears off" banner
from `metrics_only_projects`; a global toast for API failures; `NotFoundView`.
Files: `web/src/components/common/{EmptyState.vue,ErrorState.vue,SetupCard.vue,Skeleton*.vue}`,
`web/src/components/analytics/EstimatedCostNotice.vue`, `web/src/views/NotFoundView.vue`, the
hosting views, tests.
AC: with a fake store returning zero sessions, `/sessions` renders `SetupCard` with the meta-derived
URL, `OTEL_LOG_TOOL_DETAILS=1` present in the copied block, and a working copy button;
`estimated_share: 0.02` renders the notice with the percentage; `metrics_only_projects: ["x"]`
renders the banner naming the project; every view has a skeleton state asserted with a pending
promise.

---

## Phase 5 — Live view

**Goal**: an in-flight session is watchable in real time, and the firehose shows fleet activity, with
correct behaviour across reconnects and slow clients.

**Exit criteria**
1. With `argusd sim --mode=load --rate=20` running, `/live` shows events arriving continuously and
   `events_per_sec` within 20 % of the sim's rate.
2. Opening `/sessions/:id` for a session the sim is actively generating shows new timeline rows
   appearing without a manual refresh, and the KPI counters incrementing.
3. Restarting the server: the browser reconnects automatically and, when the gap is inside the replay
   window, no events are missing (UI count for a session vs `SELECT count(*)`); when the gap exceeds
   it, the UI shows a reset notice and refetches.
4. A subscriber stalled in a paused debugger does not slow ingestion (`argus_ingest_lag_seconds` p99
   unchanged) and receives a `lag` frame on resume.
5. `curl -N localhost:8080/api/v1/stream` prints framed SSE with heartbeats;
   `-H 'Last-Event-ID: <event_ref>'` replays from that position, and **only `event: event` frames
   carry `id:`**.
6. Exactly one `EventSource` per browser tab regardless of navigation.

| ID | Title | Depends on |
|---|---|---|
| P5-01 | `internal/stream`: Hub, `Envelope`, subscriptions, topics, never-block guarantee | P2-09 |
| P5-02 | SSE HTTP endpoints, framing, heartbeat, `event_ref` replay | P5-01, P3-03 |
| P5-03 | Ingest → hub fan-out, project on the envelope, `session` debouncing, `stats` frames | P5-01, P2-09 |
| P5-04 | `liveStore`: single EventSource, reconnect, ring buffer, reset/lag handling | P5-02, P4-01 |
| P5-05 | `LiveView`, `LiveFeed`, `ActiveSessionCards`, `HealthStrip` | P5-04, P4-07 |
| P5-06 | Live mode inside session detail + list live badges | P5-04, P4-02, P4-03 |

Parallel: `P5-01` → `[P] P5-02, P5-03` → `P5-04` → `[P] P5-05, P5-06`.

---

**P5-01 — Stream hub**
Scope: `internal/stream` per SPEC §5.3: `Hub` with `Subscribe(topic, filter)`/`Publish`, the
`Envelope{Event, Project}` type (so the firehose `?project=` filter is implementable — `model.Event`
has no project, review m5), per-session topic map, per-subscriber buffered channel with drop-oldest +
`dropped` counter, subscriber cap, leak-free `Unsubscribe`, Prometheus metrics.
Files: `server/internal/stream/{hub.go,subscription.go,filter.go,envelope.go,hub_test.go}`.
AC: `-race` tests — `Publish` with a subscriber whose channel is never read returns within 1 ms (the
never-block guarantee, measured); the overflowed subscriber's `dropped` counter equals the overflow
count and it later receives the newest events; a session-topic subscriber receives only its session's
events (3 sessions); a `?project=` filter matches on `Envelope.Project` and an envelope with `""`
matches no project filter; 100 concurrent subscribe/unsubscribe cycles leak no goroutines; exceeding
the cap returns `ErrTooManySubscribers`.

**P5-02 — SSE endpoints**
Scope: `httpapi/sse.go` — `GET /api/v1/stream` (with `kinds`/`project`/`vendor`) and
`GET /api/v1/sessions/{id}/stream`; the frame writer for
`event`/`session`/`stats`/`lag`/`reset`/`shutdown` with **`id:` only on `event` frames**; `retry:` on
open; heartbeat comments; correct headers; `Last-Event-ID`/`?after=` replay via
`EventsSince(after, windowStart, limit)` with the **attach-before-query** ordering and `event_ref`
dedupe on flush; `reset` outside the window; clean teardown on disconnect and on shutdown.
Files: `server/internal/httpapi/{sse.go,sse_test.go}`, `server/internal/httpapi/router.go`.
AC: `httptest` tests — `Content-Type: text/event-stream` plus a `retry:` line; published events
appear as `id:`/`event: event`/`data:` in order; **a `session`, `stats`, or `lag` frame carries no
`id:` line** (review m4 — an `id:` there would corrupt the browser's `Last-Event-ID`); a heartbeat
arrives within the configured interval (shortened test config); with `Last-Event-ID` set, the backlog
replays once and events published *during* the replay query are delivered exactly once (the test
publishes from a goroutine while the fake store's `EventsSince` blocks — it must actually race); an
out-of-window ref yields a `reset` frame first; cancelling the request context closes the handler and
unsubscribes.

**P5-03 — Ingest fan-out**
Scope: implement the P2-09 `Publisher` seam with the real hub: publish **only persisted** events
after commit, as `Envelope`s carrying the project from the session row the write touched; per-session
500 ms debounce for `session` frames; a 2 s `stats` broadcaster fed by pipeline metrics.
Files: `server/internal/ingest/publish.go`, `server/internal/stream/stats.go`,
`server/internal/app/app.go`, tests.
AC: integration test — a batch of 10 new + 10 duplicate events publishes exactly 10 frames; a
session receiving 50 events in 500 ms produces at most 2 `session` frames; `stats` reports non-zero
`events_per_sec` under sim load and zero after; **a failing transaction publishes zero frames** (the
UI must never show an event that isn't stored); an envelope for a session whose project is unknown
carries `""` and later envelopes carry the real project once `SessionStart` lands.

**P5-04 — `liveStore`**
Scope: one `EventSource` per tab with reference counting; topic switching (firehose ↔ session)
without dropping frames; automatic reconnect with jittered exponential backoff (cap 30 s) plus an
explicit `?after=<event_ref>` fallback; capped ring buffer (2000) with pause/resume; `reset` handling
that clears state and triggers a REST refetch; `lag` handling that refetches; exposed connection
state.
Files: `web/src/stores/live.ts`, `web/src/lib/sse.ts`, tests with a fake `EventSource`.
AC: store tests — two components subscribing yield one `EventSource`; the last unsubscribe closes it;
a simulated `error` reconnects with increasing delays capped at 30 s; a `reset` frame clears the
buffer and calls the refetch callback once; the ring buffer never exceeds 2000 under 5000 pushed
frames and keeps the newest; `paused` stops buffer mutation but keeps the connection alive.

**P5-05 — Live view**
Scope: `LiveView`; `LiveFeed` (streaming rows reusing `EventRow`, kind filter, pause, auto-scroll
with "jump to latest" when scrolled up, click → `EventDetailSheet`); `ActiveSessionCards` (live
tiles with cost, current tool, "follow" link); `HealthStrip` (queue depth, ingest lag, dropped total
in the warn color when non-zero, exporters seen, connection state).
Files: `web/src/views/LiveView.vue`, `web/src/components/live/*.vue`,
`web/src/components/layout/HealthStrip.vue`, tests.
AC: 100 fake frames render in reverse-chronological order, capped, without layout thrash; pause
freezes the list and the resume badge shows the buffered count; a non-zero `dropped_total` renders
the warn indicator with a tooltip explaining what was lost (visible data loss is a requirement, not
a nicety); a disconnected state renders the reconnect indicator.

**P5-06 — Live mode in the explorer**
Scope: `sessionDetailStore` subscribes to the session topic while mounted, appending live events into
the timeline (respecting the kind filter and the collapse function) and applying `session` frames to
the KPI strip; `SessionTable` rows update from firehose `session` frames for displayed rows; a live
dot on `active` sessions; a live/paused toggle in the detail header.
Files: `web/src/stores/{sessionDetail.ts,sessions.ts}`, `web/src/views/SessionDetailView.vue`,
`web/src/components/session/{SessionRow.vue,LiveDot.vue}`, tests.
AC: store test — a live event for the open session appends exactly once and is **not** duplicated
when the same `event_ref` also arrives via a REST page fetch (the dedupe that matters); an event for
another session is ignored; a `session` frame updates the KPI counters; toggling live off stops
appending but keeps the REST view usable; the timeline stays scroll-anchored when the user has
scrolled up.

---

## Phase 6 — Polish, docs, release

**Goal**: something a stranger can run in two minutes and a reviewer can judge as senior work.

**Exit criteria**
1. A clean clone → `docker compose up -d` → open `localhost:8080` → the README's copy-paste Claude
   Code config produces visible data within one turn. Verified by the owner on a fresh Docker volume,
   following the README literally.
2. `docs/` contains `SPEC.md`, `PLAN.md`, `DECISIONS.md`, `ARCHITECTURE.md`, `OPERATIONS.md`, the
   research docs, and the review.
3. README has screenshots of the six views and states the differentiator in the first paragraph.
4. `v0.1.0` tag → `release.yml` publishes `ghcr.io/yohannhommet/argus:0.1.0` for amd64+arm64 plus binary
   artefacts; `docker run … version` prints `0.1.0`.
5. `argusd sim --mode=load --rate=1000 --duration=120s` sustains ingest with p99 write latency and
   ingest lag recorded in `OPERATIONS.md`, and zero dropped events at that rate.
6. Coverage floors enforced: `normalize` ≥ 90 %, `store/postgres` ≥ 70 %, `httpapi` ≥ 75 %, web
   ≥ 60 %.
7. Keyboard navigation works for session list → detail → timeline; `axe` reports no critical
   violations on **all six views**.

| ID | Title | Depends on | Serialization |
|---|---|---|---|
| P6-01 | README + `ARCHITECTURE.md` + `OPERATIONS.md` (**creates** it) + screenshots | Phase 5 | before P6-03 |
| P6-02 | Release workflow, GHCR, versioning, CHANGELOG | Phase 5 | — |
| P6-03 | Load test pass: measure, tune, **append** to `OPERATIONS.md`; index verification | P6-01 | after P6-01 |
| P6-04 | A11y + keyboard + responsive pass | Phase 4, 5 | before P6-05 |
| P6-05 | Data-quality hardening + dropped-event surfacing polish | P6-04 | after P6-04 |
| P6-06 | Fixture/golden hardening + flake hunt | P6-05 | last |
| P6-07 | *(conditional)* `ARGUS_ATTRS_RETENTION_DAYS` | P6-03 | if triggered |

Parallel: `[P] P6-01, P6-02, P6-04` → then `[P] P6-03, P6-05` → `P6-06` → `P6-07` if triggered.

> **File-ownership note (review M9).** The earlier "all of Phase 6 in parallel" claim was false.
> `OPERATIONS.md` is created by P6-01 and only appended to by P6-03, so those two are serialized.
> `Makefile` is not edited in Phase 6 (P1-01 wrote it complete); P6-02 touches only
> `release.yml`, `CHANGELOG.md`, and the Dockerfile's labels. P6-04 owns broad
> `web/src/components/**` edits, so P6-05 (which touches `HealthStrip.vue` and the quality
> components) runs after it, and P6-06 (which rewrites fixtures and tests repo-wide) runs last.

---

**P6-01 — Documentation**
Scope: README (the differentiator in paragraph one; quickstart with the exact env block **including
`OTEL_LOG_TOOL_DETAILS=1`** and the hook JSON with `SessionEnd` timeout 1; screenshots of all six
views; what it does *not* do — pointing at SPEC §9.1, notably no per-subagent cost and why; MIT; a
note that it is unaffiliated with Anthropic); `docs/ARCHITECTURE.md` (the SPEC §0 diagram plus the
five invariants: append-only log + rebuildable projections, the lock order, publish-after-commit,
never-block-publish, and never-constrain-a-vendor-vocabulary); `docs/OPERATIONS.md` (config
reference generated by `argusd config --markdown`, retention behaviour and its coarse partition
granularity, the dedup-window guarantee, backup/restore with `pg_dump`, upgrade procedure, and
troubleshooting: no data → check `CLAUDE_CODE_ENABLE_TELEMETRY`, `CLAUDE_CODE_OTEL_DIAG_STDERR=1`,
`/api/v1/meta` exporter flags, `/data-quality`).
Files: `README.md`, `docs/ARCHITECTURE.md`, `docs/OPERATIONS.md`, `docs/img/*`.
AC: a link checker passes; `argusd config --markdown` output matches the committed reference
(CI-checked, so the docs cannot drift from the code); the quickstart was executed verbatim from a
clean state and the transcript is in the ticket report; the README's env block is byte-identical to
the one `SetupCard.vue` renders (asserted by a test that reads both).

**P6-02 — Release**
Scope: `.github/workflows/release.yml` (tag-triggered buildx multi-arch push to GHCR, binaries for
linux/darwin × amd64/arm64, release notes); ldflags version stamping from the tag; `CHANGELOG.md`
(Keep-a-Changelog); tag policy; OCI image labels.
Files: `.github/workflows/release.yml`, `CHANGELOG.md`, `server/Dockerfile` (labels only).
AC: a `v0.1.0-rc1` prerelease tag produces the multi-arch image and artefacts; `docker manifest
inspect` shows both platforms; the pulled image's `version` matches the tag; `docker inspect` shows
the OCI labels.

**P6-03 — Load test and tuning**
Scope: run `--mode=load` at 100/500/1000/2000 events/s; record ingest lag, write p50/p99, queue
depth, dropped counts, deadlock-retry counts, CPU/RSS, and DB size per million events (with the
`attrs` share broken out for OQ-5); verify each index in SPEC §2.5 is used at scale
(`EXPLAIN (ANALYZE, BUFFERS)` on the hot reads against a multi-million-row table) and drop any that
is not; measure the `ingest_dedup` ledger's growth against `ARGUS_DEDUP_WINDOW`; tune batch
size/workers/pool from the measurements; write the numbers into `OPERATIONS.md` and the defaults into
config.
Files: `docs/OPERATIONS.md` (append only), `server/internal/config/config.go` (tuned defaults),
`server/db/migrations/00X_indexes.sql` (only if an index proves unnecessary or missing),
`scripts/loadtest.sh`.
AC: the report includes a rate → lag/p99/drops/deadlock-retries table; the sustained-rate figure
appears in the README; every SPEC §2.5 index is either shown used by an `EXPLAIN` in the report or
removed with a stated reason; zero dropped events at 1000 events/s; the `attrs`-share measurement
either triggers P6-07 or records that it does not.

**P6-04 — Accessibility and responsiveness**
Scope: focus management (visible rings, logical tab order, focus trap in sheets/dialogs); keyboard
shortcuts (`/` search, `j`/`k` list navigation, `Esc` close, `?` help); ARIA roles on
tables/tabs/tree; chart accessible fallbacks (a data table behind a disclosure); responsive down to
1024 px (a laptop is the target — no phone layout); `vitest-axe` on **all six views**;
reduced-motion respect.
Files: `web/src/components/**` (focused edits), `web/src/composables/useShortcuts.ts`,
`web/src/**/__tests__/a11y.spec.ts`.
AC: `vitest-axe` reports zero critical/serious violations on all six views (review M14 — the count
now matches the router); a keyboard-only walk list → detail → open an event → close is asserted in a
test; the subagent tree exposes `role="tree"`/`treeitem` with correct `aria-level`; charts have a
toggleable data table.

**P6-05 — Data-quality hardening**
Scope: complete the `/api/v1/meta` `data_quality` block (`partial_sessions`,
`unknown_kind_events_24h`, `clock_skewed_events_24h`, `dropped_events_total`, `too_old_events_total`,
`heuristic_tool_calls_share`, `oldest_raw_event`, `dedup_ledger_rows`) and its `HealthStrip` +
`QualityTiles` rendering; make the unknown-kind inspector link from the health strip; verify the
`EXPLAIN` allow-list still holds for the added queries.
Files: `server/internal/httpapi/meta.go`, `server/internal/store/postgres/read_quality.go`,
`web/src/components/layout/HealthStrip.vue`, `web/src/components/quality/QualityTiles.vue`, tests.
AC: with chaos-mode sim data every counter is non-zero and each renders with an explanation of what
it means and what to do; `unknown_kind_events_24h > 0` links to the inspector, which lists the
unmapped `event_name`s with counts and one raw payload each; an integration test asserts each query
is window-bounded and partition-pruned; the `EXPLAIN` guard still passes.

**P6-06 — Fixture hardening and flake hunt**
Scope: regenerate all Go and web fixtures from one seeded sim run (`--seed` and `--clock-origin`
recorded in a README next to them) and keep the live-capture-derived fixtures alongside, clearly
labelled as captured-not-generated; add the golden determinism test to CI; run
`go test -count=5 -race ./...` and `pnpm unit --repeat 3` to hunt flakes and fix them at the root
(never with sleeps or retries); remove `time.Sleep` from tests in favour of synchronization; raise
coverage floors to the exit values.
Files: `server/**/testdata/**`, `web/src/test/fixtures.ts`, affected test files,
`scripts/coverage-floors.txt`, `.github/workflows/ci.yml`.
AC: `go test -count=5 -race ./...` green three consecutive runs;
`grep -rn 'time.Sleep' --include='*_test.go'` returns only cases with a written justification
comment; generated fixtures regenerate byte-identically from the recorded seed + clock origin;
capture-derived fixtures are unchanged and still parse; floors at exit values and enforced.

**P6-07 — *(conditional)* `attrs` retention**
Trigger: P6-03 shows `attrs` > 60 % of the `events` table size.
Scope: `ARGUS_ATTRS_RETENTION_DAYS` (already in the config table) plus a daily job nulling `attrs`
on events older than the cutoff in bounded batches, and an `attrs_stripped` boolean so the UI can
say so instead of showing an empty payload.
Files: `server/db/migrations/00X_attrs_retention.sql`,
`server/internal/store/postgres/retention.go`, `server/internal/config/config.go`,
`web/src/components/timeline/EventDetailSheet.vue`, `docs/OPERATIONS.md`, tests.
AC: with the setting at 7 days, events older than 7 days have `attrs = '{}'` and
`attrs_stripped = true` while normalized columns are unchanged; the job is batched (no single
statement over 10 k rows) and resumable; the UI shows "raw payload expired"; default 0 leaves
everything untouched.

---

## Cross-phase notes

**Ticket handoff protocol.** Each implementation agent reports: what it built, the exact commands it
ran with their real output, any acceptance criterion it could not meet and why, and any assumption it
had to invent (which becomes a spec amendment, not a silent decision). A ticket whose acceptance
criteria cannot be met as written is stopped and reported — not reinterpreted.

**When the agent surface changes.** If a new Claude Code release adds or renames a log event, the
`unknown` kind and `/data-quality` surface it within minutes; the fix is a normalizer mapping plus
`argusd rebuild-projections`, never a migration. The live capture in
`docs/research/live-capture-2026-08-11.md` is the template for re-verifying: its exact command line
is recorded there, including the `--permission-mode acceptEdits` flag without which no tool events
fire in `-p` mode.

**Commit discipline.** One branch per ticket (`p2-07-toolcall-correlation`), conventional commit
subjects, small commits at every working state, squash-merge to `main` with the ticket ID in the
subject. `main` is always green and always deployable.

**When a phase ends.** Owner review at the boundary. The reviewing session is handed: the
exit-criteria checklist with real command output for each item, the diff stat, and a screenshot or
transcript per UI criterion. Any exit criterion that was not literally observed is reported as unmet.
