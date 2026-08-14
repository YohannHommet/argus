# Phase 3 accepted deviations

All entries below deviate from SPEC.md/PLAN.md as originally written. Entries marked **RULING
NEEDED** have not been ratified by the owner; the rest are mechanical consequences of already-accepted
decisions and are recorded for completeness. Unlike Phase 1 and 2, SPEC.md and PLAN.md have **not**
yet been amended to match — that amendment is the owner's call on the RULING NEEDED rows.

## Deviations needing an owner ruling

| ID | Deviation | Evidence | Recommendation | Status |
|---|---|---|---|---|
| D-18 | **`too_old` (SPEC §1.7 rule 3) is now structurally unreachable through normal ingest.** P3-12's backward partition creation covers `[now − ARGUS_RETENTION_RAW_DAYS, now + 2mo]`, and §1.2's clamp rewrites any `ts` outside `[now − retention, now + 1h]` to `now`. The two windows together leave no timestamp that is both in-window and unpartitioned, so rule 3 fires only when a partition is *genuinely* absent: partition manager behind, or an operator dropped one. | The Phase-2 e2e chaos assertion went red the moment P3-12 landed (`internal/app/e2e_ingest_test.go` / `runChaosClockSkew`); `internal/sim/chaos.go`'s `buildChaosTooOldEvent` had documented "Argus never creates a partition behind the current month" as the mechanism it relied on. | Ratify `too_old` as a defence-in-depth classification rather than a routinely-reachable state, and amend SPEC §1.7 rule 3 to say so. Both affected tests now inject the missing partition explicitly (e2e drops the month; `retention_test.go` uses a partition `ApplyRetention` itself dropped) rather than asserting a condition the system no longer produces. | **Ratified, amended in SPEC.md §1.7 rule 3 and §2.4's backward-creation note (2026-08-14, W14).** |
| D-19 | **PLAN P3-10's `too_old` acceptance criterion is unreachable as literally worded** ("an event with `ts` older than the retention cutoff is rejected at write with `argus_ingest_too_old_total` incremented"). Such an event is clamped to `now` before the write path sees it and is flagged `clock_skewed`, not counted `too_old`. | `server/internal/model/clamp.go`; `TestApplyRetention_WriteBatchNeverClamps` documents the disjointness, `TestApplyRetention_DroppedPartitionRejectsInWindowEventAsTooOld` asserts the reachable form. | Reword the AC to the reachable form. This is the third consecutive revision in which this AC has been restated (review M6 → PLAN revision 3 → here); the underlying cause is D-18, so fixing D-18's wording fixes this one too. | **Ratified, PLAN.md P3-10's AC reworded to the reachable form (2026-08-14, W14).** |
| D-20 | **`internal/pricing` is a new package not in SPEC §3.1's layout.** depguard forbids `internal/store` from importing `internal/query`, where PLAN P3-04 placed the price lookup — so the rollup job (in `store`) had independently re-implemented the exact/longest-prefix/effective-date resolution. Two implementations of cost arithmetic. | The duplicate helpers (`estimateCost`/`resolvePriceRow`/`bestCandidatePriceModel`) existed in `rollups.go` before review; P3-04's 11 pricing tests now live at `internal/pricing` unchanged as the algorithm's specification. | Ratify and add `pricing/` to SPEC §3.1's layout listing. The alternative — duplicated money math that will drift — is strictly worse. | **Ratified, `pricing/` added to SPEC.md §3.1's layout listing (2026-08-14, W14).** |
| D-21 | **`openapi.yaml`'s `/meta` example advertises `feature_flags: {estimated_cost: true}`, but SPEC §3.7's config table — declared "complete and normative" — defines no such key.** `feature_flags` therefore ships as an empty map. | `server/internal/httpapi/meta.go`'s `metaResponse.FeatureFlags` doc comment. | Either define the flag in §3.7 or drop it from the example. Shipping a fabricated capability to match an example is not an option. | **Ratified (empty map), documented in SPEC.md §4.2 (2026-08-14, W14). The stale `openapi.yaml` example is a separate, not-yet-landed ticket.** |
| D-22 | **The not-found sentinels moved from `internal/store/postgres` onto the `internal/store` seam.** As first written, `internal/query` imported the concrete postgres package to recognise a missing session — inverting SPEC §3.1's `httpapi → query → store` direction and, critically, making `storetest.Fake` unable to signal 404 at all. | `server/internal/store/errors.go` (`ErrSessionNotFound`, `ErrEventNotFound`); postgres keeps aliases so `errors.Is` holds under either name. | Ratify. Without it P3-09's conformance suite would have validated the OpenAPI contract against a 500 where production returns 404 — a harness certifying behaviour the server does not have. | Out of W14 scope (no SPEC text found contradicting this; not amended here). |
| D-23 | **`rollup_hourly.tool_calls`/`tool_rejects` aggregate the `tool_calls` projection, not `events`.** SPEC §2.4 describes the pass as aggregating `events`, but only the `PreToolUse` hook emits `tool.pre` (§1.5.1/§1.5.2), so counting that kind reported **0 tool calls on every OTLP-only deployment** — a mode `/api/v1/meta` advertises via `hooks_seen: false`. | `TestRunRollups_ToolCallsFromOTelOnlyDeployment_CountsRealCalls` fails with `tool_calls=0` against the original implementation. SPEC §2.5 forbids *request-time* aggregation over `events`, not a rollup job reading a projection, so the change is within §2.5. | Ratify and amend SPEC §2.4's step 4 to name the `tool_calls` source for these two counters. This was a silent zero on the product's headline metric that no acceptance criterion covered. | **Ratified, amended in SPEC.md §2.4 step 4 (2026-08-14, W14).** |
| D-24 | **The timeline's `collapse` parameter is documented as reserved and ignored.** SPEC §4.3 lists it, but defines no server-side semantics anywhere; collapsing is client-side (PLAN P4-04's `collapseEvents`). | `server/api/openapi.yaml`, `getSessionTimeline`'s `collapse` description. | Either specify server-side collapsing or remove the parameter from SPEC §4.3. Silently accepting a no-op filter invites a client to believe it filters. | **Ratified (reserved/ignored), amended in SPEC.md §4.3 (2026-08-14, W14).** |

## Mechanical consequences and interpretation calls (recorded, no ruling expected)

| Area | Call made | Why |
|---|---|---|
| `hook_latency.by_hook_event` | Per-hook-event **p50 latency**, not execution counts | The block is `hook_latency`, its siblings are `p50_ms`/`p95_ms`, and SPEC §4.3's example pairs `p50_ms: 9` with `by_hook_event: {PostToolUse: 9}`. Counts are what `/quality/hook-latency` reports (`executions`); counting here would leave the p50 breakdown available nowhere. Corrected in review. **SPEC.md §4.3 amended to state this explicitly (2026-08-14, W14) — SPEC was silent, not contradicting, but the ambiguity is exactly what caused P3-02's bug.** |
| Analytics breakdown under `?model=` | `dimension=tool\|decision_source\|error_type\|query_source`, and `metric=calls` on any dimension, return `ErrNotAttributable` | Those sources have no model column. As first written the filter was silently dropped, so a per-model question returned fleet-wide totals that looked filtered. `rollup_hourly` books tool calls in the `model=''` group by construction, so a model-filtered call count could only ever be zero. Corrected in review. **SPEC.md §4.3 amended to extend the existing model-attributability rule to `/breakdown`'s dimension and `metric=calls` (2026-08-14, W14).** |
| `breakdown?dimension=query_source` | Sourced from `sessions.cost_by_query_source`, always reporting cost | SPEC §2.4 deliberately keeps `query_source` out of the rollup key; §4.3 puts the split on the session alone. **SPEC.md §4.3 amended to state the source and the always-cost behaviour next to the endpoint's dimension enum (2026-08-14, W14).** |
| `SessionFilter.From`/`To` | Bound `sessions.last_event_at` | SPEC §4.1 states the session list's default window is unbounded but never names the column an explicit window applies to. `last_event_at` matches the endpoint's default sort key. **SPEC.md §4.1 amended to name the column (2026-08-14, W14).** |
| Event ordering vs keyset | Keyset predicate is the full 3-key `(ts, vendor_seq NULLS LAST, seq)` continuation, not the `(ts, seq)` the ticket's scope line named | Those are different orders; a `(ts, seq)` predicate silently skips or repeats rows when two events share a `ts` and `vendor_seq` ranks them opposite to `seq`. Still rides `events (session_id, ts, seq)` because `ts` leads. **SPEC.md §1.2 amended — it previously stated the keyset cursor was `(ts, seq)`, contradicting the implementation (2026-08-14, W14).** |
| `ListTurns` pagination | In-memory over the store's full per-session result | SPEC §3.3 fixes the signature with no `Page` parameter while `openapi.yaml` declares `limit`/`cursor`. Acceptable at v1 turn counts; revisit if they grow. |
| `model.Event`/`model.ToolCall` wire shapes | Small wire adapters in `httpapi` rather than marshalling the model types | Those two types carry no JSON tags and have no `event_ref` field; marshalling them directly would ship wrong field names. |
| `ApplyRetentionPrecise` | A `Store`-only method, not on the `Maintenance` interface | SPEC §3.3's interface is frozen and does not list it; same treatment as the existing `MigrateStatus`/`ImportPrices`. |
| Nullable schema spelling | `type: [X, "null"]`, not `nullable: true` | PLAN P3-01's AC uses OpenAPI 3.0 phrasing; the spec is 3.1, where `nullable` does not exist. |
| `estimated_cost_present` | Computed via `AnalyticsSummary` over all recorded history | No dedicated store method exists, and SPEC §2.5 allow-lists only the two quality queries onto `events`. Rollup-only, and retention never prunes rollups. |
| `PartitionJob` construction | Takes `retentionRawDays int` | `internal/store` must not import `internal/config`; `internal/app` owns the config dependency. |

## Integration defects found only by running the product (both would have shipped)

Both were found during the CI-exact / exit-criteria pass against a live
`docker compose` stack — **not** by the test suite, which was green at 715 tests
throughout. Both sat in the gap between components that were each individually
correct and individually well tested.

| Defect | Symptom | Why no test saw it | Fix |
|---|---|---|---|
| **The entire read API was unmounted on the real server.** `Serve` never set `Deps.Reader`/`Deps.Analytics`, and `router.go` mounts each group only when those are non-nil (a nil-safe default inherited from P1-05). | `docker compose up` + `curl /api/v1/sessions` → **404**, likewise every session, event, tool-call, analytics, facets and quality route. Only `/meta`, `/healthz`, `/readyz`, `/metrics` and the ingest paths answered. | P3-07's and P3-08's handler suites *and* P3-09's conformance harness (100% of operationIds) all construct `httpapi.New` directly with a fake reader. None goes through `Serve`, so none exercises the real route table. | `951e4ed` — two lines in `serve.go` plus `read_api_e2e_test.go`, which starts the real App via `New` + `Serve` and asserts all 17 read routes answer non-404. Verified to fail without the wiring: 16 of 17 red. |
| **`model_prices` was empty on every deployment**, so cost estimation could never resolve a price. | After `--cost-mode=omit`, 31 uncosted `llm.request` events across 3 models were stored while the API reported `cost.estimated_usd = 0` and `estimated_share = 0` — the silent zero SPEC §4.1 forbids, on the numbers that tell an operator a cost is estimated rather than measured. | The table ships embedded and `argusd prices import` loads it, but nothing ran that command — not compose, not the quickstart, not startup. Every pricing and rollup test seeds prices itself. | `df3a7ab` — `New` imports the embedded table after migrations and fails startup if it cannot. Verified: 3 rows at boot, then `estimated_usd = 9.33385798`, `estimated_share = 1`. |

The second is also a **SPEC gap worth ratifying**: §3.8 lists `prices` as an
operator subcommand and §2.4 defines `cost_estimated_usd` in terms of
`model_prices`, but nothing says who populates that table on a fresh install.
Unconditional idempotent startup import (repo-sourced rows only) is the reading
taken here; the alternative — documenting a manual step — leaves estimated cost
silently zero for any operator who skips it.

**Status: ratified, specified in SPEC.md §3.8 (startup behaviour) with a cross-reference from §4.1
(2026-08-14, W14).**

## Ratified in the pre-Phase-4 audit (2026-08-14, W14)

| ID | Deviation | Evidence | Ruling |
|---|---|---|---|
| D-25 | **The `store/postgres → ingest/normalize` import edge is ratified rather than removed.** `upsert_toolcall.go` and `upsert_subagent.go` reuse `ingest/normalize`'s pure correlation helpers instead of duplicating them in `store`. `.golangci.yml`'s `store` depguard rule denies only `httpapi`/`query`, so this edge passes silently; SPEC §3.1 claimed the inward direction was "enforced by a depguard rule ... not by convention," which overstated the actual coverage. | `.golangci.yml`'s `store` rule (`internal/store/**`, denies only `httpapi` and `query`); pre-Phase-4 audit finding m25/M25-class. A concurrent ticket is adding the missing `httpapi` and `query` depguard rules; this edge is intentionally left un-denied rather than closed. | Ratified. The helpers are pure functions with no side effects; duplicating them in `store` would be strictly worse than the layering exception. SPEC.md §3.1 amended to (a) record the exception with its rationale and (b) stop claiming depguard enforces coverage it does not (model/store/ingest/sim only). |

## CI and test-harness hardening (not SPEC/PLAN deviations)

| Fix | What broke | Fix |
|---|---|---|
| `pnpm type-check` checked **zero files** | `web/tsconfig.json` is solution-style (`"files": []`, only `"references"`), and `vue-tsc --noEmit` against such a config type-checks nothing and exits 0 — proven with a `const x: number = "string"` probe. The `web` CI job's type-check step has asserted nothing since P1-03, which is why P3-11's AC ("type-check catches a deliberately wrong field access") was unfalsifiable. | `vue-tsc --build --force`, which immediately surfaced two real pre-existing errors it had been hiding (`main.ts`'s CSS side-effect import needing `vite/client` types, and P3-11's fetch mocks not satisfying `typeof fetch`). `--force` so a stale `tsbuildinfo` cannot re-hide one. |
| EXPLAIN allow-list test asserted nothing | `TestExplainGuard_DataQualityAllowList` called `t.Logf` and nothing else, so a populated allow-list was indistinguishable from an empty one — on the very gate P3-10 had built to be non-vacuous. | Asserts both halves of SPEC §2.5's *conditional* exemption: the plan really does touch `events`, **and** it applies a `ts` bound. Verified to fail against a deliberately unbounded query. |
| EXPLAIN plans measured an empty plan | In a fresh test schema no partitions cover the window, so the allow-listed queries plan as `Result / One-Time Filter: false` — scanning nothing. The plan *still* contained the string `events` (a `Group Key` references `events.event_name`), so even a mention check passed while proving nothing. | The test creates the partitions the window needs before running `EXPLAIN`. |
| P3-12's own test would have passed on `main` | It called `EnsurePartitions` directly with a backward range, which the function already honoured — the ticket changed the *caller*. | Added `internal/app/jobs_test.go`, asserting the job's range reaches the retention floor; verified to fail against the previous `from = now` caller. |

## Defect found in Phase-2 code, not fixed here (Phase 4 will hit it)

**`argusd sim --sessions=N` produces fewer usable sessions than N.** Verified deterministically with
`argusd sim --out=/tmp/simout --seed=7 --sessions=5`: the emitted per-session directories hold

| directory | log events | hook events | metric files |
|---|---|---|---|
| session-0000 | 150 | 131 | 11 |
| session-0001 | 203 | 184 | 17 |
| session-0002 | 121 | 102 | 9 |
| session-0003 | **8** | **8** | 2 |
| session-0004 | **0** | **0** | 1 |

Session content collapses as the ordinal rises. `backfillOffset(ordinal, sessions, backfill)` places
ordinal `N-1` at the very end of the backfill window, so its events fall at or past "now" and are
not emitted. The last session therefore contributes only a metric sample, which correctly produces
**no `sessions` row** (the sessions projection is event-driven; a metrics-only project is exactly
what SPEC §4.3's `metrics_only_projects` describes). This is why a `--sessions=5` demo run yields 4
session rows.

Server-side behaviour is correct — `count(*) FROM sessions` equals `count(DISTINCT session_id) FROM
events` and no stub rows remain — so nothing in Phase 3 is wrong. But **Phase 4's exit criterion 1
requires `/sessions` to list ≥ 20 sessions from a demo run**, and with this defect the operator must
pass a larger `--sessions` than they expect, with the tail of the range producing thin or empty
sessions. Worth fixing in `internal/sim` (P2-12's `backfillOffset`/emission cutoff) before Phase 4
depends on demo data volume.

## Process observations

Phase 2 recorded that three implementer agents pre-emptively loosened passing assertions. Phase 3's
recurring failure mode was adjacent but distinct: **tests and gates that pass while proving nothing.**
Five instances, none of which a green suite would have revealed —

1. P3-12's test exercised a function the ticket did not change (would have passed on `main`).
2. `pnpm type-check` type-checked zero files, project-wide, since Phase 1.
3. The EXPLAIN allow-list test logged its plans instead of asserting on them.
4. Those same plans scanned nothing, so even the added mention check was vacuous.
5. P3-02's `by_hook_event` asserted the opposite of the field's meaning (counts, not latency), so the
   test agreed with the bug.

Two further defects were silent *wrong answers* rather than vacuous tests, and are the ones that
would have shipped: analytics `tool_calls` reading 0 on any hooks-less deployment (D-23), and the
breakdown endpoint dropping the `model` filter instead of refusing it. Neither was covered by any
acceptance criterion.

One report was also inaccurate: P3-08 stated the allow-list test "now actually asserts" when it did
not. Reports are leads, not evidence — every AC in this phase was re-verified against actual command
output before its commit.

Commit-boundary note: P3-08's `pool.go` stub removals and its first allow-list entries were swept
into `0d36bcb` (P3-10) by the lead staging shared files while both tickets ran in one working tree.
Content is intact and both commit messages describe it; recorded rather than rewritten.
