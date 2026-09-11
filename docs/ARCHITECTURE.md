# Argus — Architecture

Audience: a reviewer judging the design, not an operator. This document is a map with WHY
attached to each load-bearing decision; it links to `SPEC.md` for the normative detail rather
than restating it. Ground truth is the code — every claim below cites a file.

## System diagram

Copied from [SPEC §0](SPEC.md#0-system-summary):

```
Claude Code
  ├─ OTLP/HTTP  (OTEL_EXPORTER_OTLP_ENDPOINT → argusd)  → POST /v1/logs, /v1/metrics
  └─ hooks      ("type": "http")                        → POST /ingest/hook
                                    │
                          ┌─────────▼─────────┐
                          │ internal/ingest   │  decode → normalize → bounded queue
                          └─────────┬─────────┘
                                    │ batches
                    ┌───────────────┼───────────────┐
                    ▼                               ▼
           internal/store (pgx)             internal/stream (SSE hub, in-process)
             Postgres 18                             │
             events (partitioned)          GET /api/v1/stream (firehose)
             + projections + rollups       GET /api/v1/sessions/{id}/stream
                    │
                    ▼
           internal/query → internal/httpapi (chi) → /api/v1/... → Vue SPA
```

One Go binary (`argusd`) embeds the Vue SPA, Postgres is the only stateful dependency, and the
whole system is a straight line from an agent's telemetry to a row in Postgres to a browser. The
two rules this diagram exists to serve — append-only event log with rebuildable projections, and
no vendor vocabulary is ever constrained — are the subject of invariants 1 and 5 below; everything
else in this document is downstream of them.

## Internal package map and request/event flow

| Package | Role |
|---|---|
| `internal/model` | Leaf package (stdlib-only, depguard-enforced): the `Event` record, the four closed taxonomies (`Kind`/`Source`/`Correlation`/`Status`), dedup-key and event-ref codecs. Everything else depends on it; it depends on nothing internal. |
| `internal/ingest` (+ `hooks`, `normalize`, `otlp` subpackages) | Decode (`otlp`: `POST /v1/logs`, `/v1/metrics`, `/v1/traces`; `hooks`: `POST /ingest/hook`) → normalize (pure, in the request goroutine) → `Pipeline`: bounded queue, worker batching, retry-classified writes, and the publish hand-off. |
| `internal/store` (+ `postgres`) | The `Store` interface (`Writer`/`Reader`/`Maintenance`) and its Postgres implementation — the only package that speaks SQL. |
| `internal/query` | Read-side business logic sitting between `store.Reader` and `httpapi` (facets, data-quality, analytics shaping) — not itself SQL. |
| `internal/stream` | The in-process SSE `Hub`: fan-out, per-subscriber buffering, replay window, stats broadcast. |
| `internal/httpapi` | chi router: mounts the OTLP/hook ingest routes, `/api/v1/*` REST, `/healthz`/`/readyz`/`/metrics`, and the embedded SPA. |
| `internal/app` | Wires config → store → hub → pipeline → httpapi and owns the process lifecycle (migrations, background jobs, graceful shutdown). Doc comment: "the only package allowed to know about every other layer at once; httpapi, store, and config each stay ignorant of it" (`server/internal/app/app.go:1-6`). |

**Write path.** `httpapi` hands a decoded OTLP/hook payload to `ingest`, which normalizes it
(`ingest/normalize`, `ingest/otlp`, `ingest/hooks`), enqueues it (non-blocking, load-shed on a
full queue), and a `Pipeline` worker batches and flushes it through `store.Writer.WriteBatch` —
one Postgres transaction that inserts into `events` and updates every projection plus
`rollup_dirty` (`server/internal/store/postgres/write.go`). Only after that transaction commits
does the pipeline hand the persisted subset to `ingest.HubPublisher`, which fans it out through
`internal/stream.Hub` to SSE subscribers (invariant 3, below).

**Read path.** `httpapi` handlers call `internal/query` functions (`Facets`, `DataQuality`,
`AnalyticsSummary`, …), which call `store.Reader` for the SQL and shape the result into the
`/api/v1/*` response envelopes the Vue SPA consumes.

**Startup.** `internal/app.New` builds this graph in a fixed order — the stream hub must exist
before the ingest pipeline, because `HubPublisher` needs it at construction time
(`server/internal/app/app.go:171-190`) — then `Serve` runs the HTTP server and background jobs
(partitions, rollups, sweep, retention) until SIGINT/SIGTERM drives the graceful-shutdown sequence
([SPEC §3.8](SPEC.md#38-subcommands-and-lifecycle)).

## The five invariants

### 1. Append-only event log + rebuildable projections

**Why:** `sessions`/`turns`/`tool_calls`/`subagents` are derived state, not sources of truth. A
schema or mapping bug in a projection is recoverable by recomputation from `events` instead of
requiring a backfill migration or living with permanently wrong aggregates — and it is what makes
merging two independent sources (OTel vs. hooks) tractable at all, since a correlation-heuristic
fix can simply be replayed rather than reconciled by hand.

**Where:** [SPEC §0 rule 1](SPEC.md#0-system-summary), [SPEC §1.6](SPEC.md#16-projections).
`argusd rebuild-projections` (`server/cmd/argusd/main.go:301-364`) drives
`store.Maintenance.RebuildProjections` (`server/internal/store/postgres/rebuild.go`), which
truncates the four projection tables (scoped by session when `--from-ts` is given) and replays
`events` in `(ts, seq)` order through the **same** unexported fold/upsert functions `WriteBatch`
itself calls — per that file's package doc, this is what makes "rebuild produces identical rows"
true by construction rather than by convention. `events.id` is `uuidv7()` and is never rebuilt;
deterministic `tool_calls.id` (UUIDv5 over `session_id|tool_use_id`) is what makes that replay
reproducible.

### 2. The fixed lock order

**Why:** two concurrent `WriteBatch` transactions touching an overlapping set of sessions must
acquire every row lock in the same order, so they can only ever queue behind each other — never
deadlock. This is an invariant, not a tuning choice: reordering the statements "for efficiency"
reintroduces the FK share-lock/exclusive-lock interleaving deadlock this order exists to prevent,
which the SPEC calls out as likely at 4 workers × 500-event batches.

**Order:** `ingest_dedup` (by `dedup_key`) → `sessions` (by `id`) → `turns` (by `session_id,
prompt_id`) → `events` (by `ts, dedup_key`) → `tool_calls` (by `id`) → `subagents` (by
`session_id, agent_id`) → `rollup_dirty` (by `bucket, source`).

**Where:** [SPEC §1.6](SPEC.md#16-projections). `server/internal/store/postgres/write.go:1-20`
states the invariant verbatim in the package doc and names its regression test
(`TestWriteBatch_ConcurrentOverlappingSessions`). One documented, narrow deviation: `too_old`
classification (partition coverage) is decided *before* the dedup gate, because it only reads
partition metadata and takes no row lock — see `partitions.go`'s `partitionCoverage` doc.

### 3. Publish-after-commit

**Why:** the SSE stream must never show a viewer an event that a concurrent read (session detail,
timeline) cannot also see. An event that appears live and then vanishes on refresh — because it
was never actually durable, or the transaction that would have persisted it failed — is worse than
the extra latency of waiting for the commit.

**Where:** `server/internal/ingest/pipeline.go`'s `flushEvents` calls `store.Writer.WriteBatch`
first; only once that succeeds does it call `p.handoffPublish(persisted)`, using
`res.EventRefs` — the subset Postgres actually committed, not the whole input batch (dedup and
too-old rows are excluded). `handoffPublish` is the only caller of
`ingest.HubPublisher.Publish` (`server/internal/ingest/publish.go`), which turns committed events
into `stream.Envelope`s and fans them out via `internal/stream.Hub`. Wiring:
`server/internal/app/app.go:171-190`.

### 4. Never-block-publish

**Why:** a slow or stuck SSE hub must never become ingest backpressure. A tool that drops a few
live-view frames under load is acceptable; a tool that 503s OTLP/hook ingest because *its own
dashboard* is slow is not — ingest correctness must not depend on the read side's health.

**Where:** `server/internal/ingest/pipeline.go:72-77` states the contract on the `Publisher`
interface itself: "never block, tolerate Publish after Close returns." The mechanism is a bounded
hand-off channel (`publishCh`) fed by a non-blocking `select`/`default` send
(`handoffPublish`, same file): on a full channel the batch is dropped from the stream (logged, not
counted against the "never made it to storage" drop metric, since the write already committed) —
the flushing worker never stalls. A dedicated `runPublishWorker` goroutine is the only consumer of
`publishCh`, isolating the hub from every ingest worker.

### 5. Never-constrain-a-vendor-vocabulary

**Why:** Claude Code (and any future OTel-emitting agent) ships attribute values the documentation
does not list — the SPEC's live capture found `query_source` values (`sdk`,
`generate_session_title`) and a `terminal.type` (`wsl-Ubuntu`) nobody had documented. A `CHECK`
constraint or a rejecting Go enum on any agent-supplied string would silently drop or 500 on real,
valid telemetry the moment a vendor changes something Argus does not control.

**Where:** [SPEC §0 rule 2](SPEC.md#0-system-summary). Only Argus's own taxonomy — `kind`,
`source`, `correlation`, `status` — is closed, and even that is not total:
`server/internal/model/kind.go:1-16` states it directly — "`Kind`... is a closed set — one of the
four vocabularies SPEC §0 permits to be closed — but closed does not mean total: any `event_name`
the normalizer does not recognize maps to `KindUnknown` rather than being dropped or rejected."
The `events` table ([SPEC §2.2](SPEC.md#22-events-partitioned)) carries `query_source`,
`terminal_type`, `decision_source`, `tool_source`, `start_type`, `permission_mode` as plain `text`
columns with zero `CHECK` constraints — Argus has no `CHECK` constraints anywhere.

## Deployment = topology

One binary, one embedded SPA, one database. `argusd serve` is the only production entrypoint: it
serves ingest, the REST/SSE API, and the built Vue app (`server/internal/httpapi/assets/dist`,
copied in at build time — see `server/Dockerfile`) on a single `ARGUS_HTTP_ADDR` listener, so
there is no reverse proxy and no CORS configuration in production. Postgres 18+ (`uuidv7()`
requires it) is the only stateful dependency. `deploy/docker-compose.yml` is the reference
topology: a health-gated `postgres` service and an `argusd` service whose own healthcheck is
`argusd healthcheck --endpoint=readyz` (SPEC §3.8's full readiness contract: DB ping, migrations
current, queue not saturated) — see `docs/OPERATIONS.md` for the operational detail. There is no
OTel Collector container and no ClickHouse in v1 (v2 backlog only, per `docs/DECISIONS.md`): the
binary itself is the OTLP/HTTP receiver, and Postgres is the only storage backend.
