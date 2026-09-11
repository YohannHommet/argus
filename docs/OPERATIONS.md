# Argus — Operations

Audience: someone self-hosting Argus via `docker compose`. For design rationale see
[`ARCHITECTURE.md`](ARCHITECTURE.md); for the normative spec see [`SPEC.md`](SPEC.md).

## Config reference

The full key/default/notes table lives in [`config-reference.md`](config-reference.md). It is
**generated, not hand-written** — `argusd config --markdown` prints it directly from the
`Config` struct's tags (`server/internal/config/config.go`), and CI fails the build if the
committed file drifts from what that command emits (see "CI" below). Do not hand-edit it;
regenerate it instead:

```bash
cd server && go run ./cmd/argusd config --markdown > ../docs/config-reference.md
```

A few keys are worth explaining beyond their one-line note:

- **Retention** — `ARGUS_RETENTION_RAW_DAYS` (default 90) is how long raw `events`/`metric_samples`
  survive; `ARGUS_RETENTION_SESSION_DAYS` (default 0 = never) optionally also deletes old
  `sessions` rows (cascading to `turns`/`tool_calls`/`subagents`); `ARGUS_RETENTION_HOUR` (default
  4, local time) is when the daily retention job runs; `ARGUS_ATTRS_RETENTION_DAYS` (default 0 =
  no separate cutoff) would let the raw `jsonb` `attrs` column be pruned earlier than the rest of
  a row, if ever implemented. See "Retention behaviour" below.
- **Dedup window** — `ARGUS_DEDUP_WINDOW` (default `7d`) is the `ingest_dedup` ledger's own
  retention, which *is* the exact-dedup guarantee's scope. See "Dedup-window guarantee" below.
- **Ingest queue/workers/batch** — `ARGUS_INGEST_QUEUE` (batch capacity of the pipeline's bounded
  channel, default 1024), `ARGUS_INGEST_WORKERS` (default 4 batching goroutines),
  `ARGUS_INGEST_BATCH_SIZE` (default 500 events) / `ARGUS_INGEST_FLUSH` (default 250ms) — a
  worker flushes a batch to Postgres when either bound is hit, whichever comes first. A full
  queue sheds load with a correct HTTP status (`503` OTLP, `429` hooks) rather than blocking the
  caller; it is a shock absorber, not a buffer for a slow database.
- **Shutdown grace** — `ARGUS_SHUTDOWN_GRACE` (default `15s`) budgets *two separate* phases on
  SIGINT/SIGTERM: draining in-flight HTTP requests, then draining the ingest queue — each gets
  its own full budget rather than sharing one, so a slow in-flight request can't starve the
  queue drain. Worst-case shutdown is therefore up to **2×** this value; the shipped
  `deploy/docker-compose.yml` sets `stop_grace_period: 40s` to give real headroom above that
  doubled figure, not just above the raw 15s default.

## Retention behaviour

Retention operates at **monthly partition granularity**, not per-row, because the `events` and
`metric_samples` tables are `PARTITION BY RANGE (ts)` with one partition per calendar month — 90
days of a personal/team deployment is only ~4 partitions, so daily partitions would mean tens of
planning-time partitions for a marginal pruning win (SPEC
[§2.2](SPEC.md#22-events-partitioned)). The daily retention job drops every partition whose upper
bound is at or before `now() - ARGUS_RETENTION_RAW_DAYS`, wholesale, inside one transaction — a
partition is only ever dropped once **entirely** past the horizon (SPEC
[§2.4](SPEC.md#24-rollups-prices-jobs)). Practically: raw events for a given month disappear all
at once, up to ~30 days after they individually crossed the retention horizon, not on a rolling
per-event basis. `argusd retention --precise` additionally `DELETE`s, in bounded batches, the
stale rows within the *one* partition straddling the cutoff that a coarse drop cannot touch — it
never drops a partition itself, only rows inside one.

Rollups (`rollup_hourly`, `rollup_daily`) and every projection table
(`sessions`/`turns`/`tool_calls`/`subagents`) are **never** touched by raw retention — "rollups
kept" is not a default that can be turned off by the raw-events cutoff. A session older than the
retention horizon keeps its row and its aggregates with an empty timeline (the UI reports "raw
events expired"). Both the raw-events horizon and the optional session deletion
(`ARGUS_RETENTION_SESSION_DAYS`, default 0 = never) are independently configurable.

Dry-run before trusting this in production: `argusd retention --dry-run` lists exactly which
partitions would be dropped and changes nothing — not even `ingest_dedup` pruning.

## Dedup-window guarantee

`ARGUS_DEDUP_WINDOW` (default `7d`) is the retention of the `ingest_dedup` ledger — the single
gate every ingested event or metric is checked against before being written (SPEC
[§1.7](SPEC.md#17-out-of-order-arrival-unknown-parents-idempotency)). Within the window, dedup is exact: a retransmitted OTLP batch
or a duplicate hook-config scope inserting the same logical event twice is caught and suppressed
(counted in `argus_ingest_deduped_total`), because receipt-time timestamps on hook events make any
`ts`-based uniqueness check useless on its own. Widening the window buys a longer exact-dedup
guarantee at the cost of ledger size — roughly 90 bytes per event kept in `ingest_dedup` for the
full window, pruned in bounded batches by the daily retention job. Beyond the window, a re-sent
**hook** event may duplicate (hooks carry no stable server-independent identity once their ledger
entry is gone); a re-sent **un-clamped** OTel event still cannot duplicate, because its `ts` is
agent-supplied and stable and a parent-level `UNIQUE (ts, dedup_key)` constraint on `events` still
catches it as defence in depth. The one residual risk, accepted for this release: an out-of-window
`ts` gets clamped to the server's `now()` at ingest, which differs between re-deliveries of the
same underlying event — once *that* delivery's ledger entry is also pruned, a replay of the
clamped event can double-count. See SPEC §1.7 for the full reasoning.

## Backup and restore

Against the compose Postgres (service `postgres`, database `argus`, user `argus`):

```bash
# Backup — custom format (-Fc), compressed, works with pg_restore's selective/parallel options
docker compose -f deploy/docker-compose.yml exec -T postgres \
  pg_dump -U argus -d argus -Fc > argus-$(date +%Y%m%d-%H%M).dump

# Restore into that same database, dropping conflicting objects first
docker compose -f deploy/docker-compose.yml exec -T postgres \
  pg_restore -U argus -d argus --clean --if-exists < argus-20260911-0000.dump
```

Take the backup with `argusd` still running — there is no requirement to stop ingest first, since
`pg_dump` takes a consistent MVCC snapshot. A restore, however, replaces existing data: stop
`argusd` first (`docker compose stop argusd`) so nothing writes against a database mid-restore,
then start it again once `pg_restore` completes. There is no dedicated Argus backup tooling in
v1 — `pg_dump`/`pg_restore` against the partitioned schema is the whole story.

## Upgrade procedure

```bash
docker compose -f deploy/docker-compose.yml pull argusd
docker compose -f deploy/docker-compose.yml up -d
```

With `ARGUS_AUTO_MIGRATE` at its default (`true`), the new `argusd` container runs every pending
migration itself at startup, before serving traffic — guarded by a Postgres session-scoped
advisory lock (`server/internal/store/postgres/migrate.go`, key `ARGUS01`) so a rolling restart
or two containers starting together cannot race goose's own version bookkeeping; a second starter
simply waits for the first to finish, then finds nothing pending. If `ARGUS_AUTO_MIGRATE=false`,
run `argusd migrate` by hand against the target database before pointing the new image at it.
`/readyz` (and therefore the compose healthcheck) reports not-ready until migrations are current,
so `depends_on: condition: service_healthy` dependents never see a half-migrated server.

## Troubleshooting: "no data"

1. **Confirm telemetry is actually enabled on the Claude Code side.** It is off by default:
   `CLAUDE_CODE_ENABLE_TELEMETRY=1` plus at least one exporter env var
   (`OTEL_LOGS_EXPORTER=otlp`, `OTEL_METRICS_EXPORTER=otlp`,
   `OTEL_EXPORTER_OTLP_ENDPOINT=http://<argus-host>:8080`, …) must be exported in the shell that
   launches Claude Code.
2. **Check for silently-failing exports.** Claude Code swallows OTel export errors unless
   `CLAUDE_CODE_OTEL_DIAG_STDERR=1` is also set. Run Claude Code with it once and watch stderr for
   connection/timeout errors talking to Argus.
3. **Inspect `GET /api/v1/meta`.** Its `data_quality` block and the `logs_exporter_seen` /
   `metrics_exporter_seen` / `hooks_seen` / `tool_details_seen` flags say exactly which ingest
   surface has (or hasn't) ever delivered anything — this tells you whether the problem is OTLP
   logs, OTLP metrics, the hooks webhook, or `OTEL_LOG_TOOL_DETAILS` specifically, rather than
   "nothing at all."
4. **Watch the `/data-quality` view** in the UI — the unmapped-`event_name` inspector (rows
   landing as `kind=unknown`) and the hook-latency panel are how a Claude Code release that adds
   an event, or a misconfigured hook, becomes visible within minutes instead of being invisible.
5. **Check readiness, not just liveness.** `argusd healthcheck --endpoint=readyz` (what the
   compose healthcheck itself runs) or `curl -f http://localhost:8080/readyz` exercises the full
   SPEC §3.8 contract — DB ping, migrations current, ingest queue not saturated. A `503` here
   means ingest is structurally blocked (e.g. Postgres unreachable, or the queue is full and
   shedding), which looks like "no data" but is really "nothing can be written right now" —
   `/healthz` alone would report OK and hide this.

## Operational commands

All read `--config <path>` for an optional YAML file (env still wins); see `server/cmd/argusd/main.go`.

| Command | What it does |
|---|---|
| `argusd migrate [up\|status]` | Runs every pending migration to latest (`up`, default) or prints each migration's applied/pending status. Advisory-lock-guarded so concurrent invocations serialize instead of racing. |
| `argusd retention [--dry-run] [--precise]` | Runs the retention job once: drops fully-expired monthly partitions, optionally deletes expired sessions, prunes `ingest_dedup`. `--dry-run` lists what would be dropped and changes nothing; `--precise` additionally batch-deletes stale rows from the one boundary partition a coarse drop can't reach. |
| `argusd rebuild-projections [--from-ts <RFC3339>] [--force]` | Truncates and replays `sessions`/`turns`/`tool_calls`/`subagents` from `events`, scoped to every session active at/after `--from-ts` (default: every session, full rebuild). `--force` overrides the refusal that fires when `--from-ts` predates the oldest surviving partition. |
| `argusd prices import` | Idempotently loads the repo's `model_prices.json` into `model_prices` (`source='repo'`). `serve` already runs this automatically at startup; the subcommand exists for re-importing after a repo update to the price table. |
| `argusd config [--print\|--markdown]` | `--print` (default) dumps the effective, secret-redacted config as `KEY=value` lines; `--markdown` emits the reference table embedded above. |
| `argusd healthcheck [--endpoint=healthz\|readyz] [--timeout <dur>]` | Hits the configured HTTP listener's `/healthz` (liveness, default) or `/readyz` (full readiness) and exits 0/1 — this is the compose healthcheck command. |

## Load test results

Generated by [`scripts/loadtest.sh`](../scripts/loadtest.sh), which brings up a dedicated compose
stack, drives `argusd sim --mode=load` at each rate, and scrapes `/metrics` + Postgres. Reproduce:

```bash
ARGUS_LOAD_PORT=18092 ARGUS_LOAD_RATES="100 500 1000" ARGUS_LOAD_DURATION=20s bash scripts/loadtest.sh
```

Run 2026-09-11 on a shared WSL2 dev box (also hosting an unrelated multi-service Docker stack), so
these are **conservative** figures — a dedicated host sustains materially higher throughput. What
matters for correctness holds regardless: **no dropped events, no `too_old` rejections, and no
deadlock-retries at any rate, including the 1000 ev/s target**.

| target rate | effective throughput | events written | dropped | deduped | too_old | deadlock-retries | write p50 | write p99 | ingest-lag p99 |
|---|---|---|---|---|---|---|---|---|---|
| 100 ev/s  | 78 ev/s  | 2 000  | **0** | 112   | 0 | 0 | 16.6 ms | 55.0 ms  | 9.9 ms |
| 500 ev/s  | 359 ev/s | 9 178  | **0** | 1 098 | 0 | 0 | 20.2 ms | 203.0 ms | 9.9 ms |
| 1000 ev/s | 595 ev/s | 15 208 | **0** | 4 007 | 0 | 0 | 22.2 ms | 182.8 ms | 9.9 ms |

Notes:
- **Effective throughput < target rate** is a limit of this CPU-contended box, not backpressure: the
  bounded ingest queue never shed a single event (`dropped = 0`). The `deduped` column is the
  `ingest_dedup` ledger suppressing the load generator's intentionally-repeated keys, i.e. the dedup
  path working as designed, not loss.
- **Write latency** stayed low (p50 ~20 ms) with a fat p99 tail under contention — acceptable for an
  async, batched ingest path where the client is fire-and-forget.
- **Storage / `attrs` share (P6-07 trigger).** After the run: ~12.9 k event rows, ~21 MB across the
  monthly partitions, `attrs` = **48.5 %** of the `events` table. That is **below the 60 % threshold**,
  so **P6-07 (`ARGUS_ATTRS_RETENTION_DAYS`) is not triggered** and stays deferred. The
  `ingest_dedup` ledger held ~22.4 k rows (bounded by `ARGUS_DEDUP_WINDOW`).
- **Default config validated.** The shipped ingest defaults (queue/workers/batch/flush) absorbed the
  1000 ev/s target with zero loss, so no tuning was applied.
- **Deferred to a dedicated host:** the 2000 ev/s tier and the multi-million-row
  `EXPLAIN (ANALYZE, BUFFERS)` index-usage pass (SPEC §2.5) — this shared box cannot build a
  multi-million-row `events` table without disrupting the co-tenant stack. `scripts/loadtest.sh` takes
  higher `ARGUS_LOAD_RATES`/`ARGUS_LOAD_DURATION` to produce those figures where a dedicated host is
  available.
