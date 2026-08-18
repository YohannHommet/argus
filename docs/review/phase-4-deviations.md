# Phase 4 accepted deviations and findings

Stage 1 of Phase 4 (everything functional; the visual gauntlet iterates on design afterwards).
Entries marked **RULING NEEDED** were not ratified when this file was written. The rest are
interpretation calls recorded for completeness.

Every number quoted below was measured against a live `docker compose` stack seeded with
`argusd sim --mode=demo --seed=42`, not inferred from a fixture.

---

## Deviations needing an owner ruling

| ID | Deviation | Evidence | Recommendation |
|---|---|---|---|
| D-26 | **The synthetic root subagent node returns a `status` outside its own declared enum.** `openapi.yaml` types `SubagentStatus` as `enum: [running, complete, failed, unknown]` and describes it as "Argus-computed … closed", but the live API returns `"ended"` for the root node — and would equally return `active` or `abandoned`. | `server/internal/store/postgres/subagent_tree.go:124` casts the *session* status (`active\|ended\|abandoned\|unknown`, a different vocabulary) straight into `model.SubagentStatus`. Reproduced live: `GET /api/v1/sessions/{id}/subagents` → root node `"status": "ended"`. P3-09's conformance harness runs over the fake store, which only ever yields in-enum values, so it never saw a real root node. | Map session status → subagent status at the seam (`active→running`, `ended→complete`, `abandoned→failed`, `unknown→unknown`), **or** widen the enum and drop the "closed" claim. Do not leave the spec asserting a closed set the server violates. The UI is already safe either way: P4-05 routes node `status` through `RawValue`, tested against `"ended"` and against an invented value. |
| D-27 | **PLAN P4-06's sort acceptance criterion is unsatisfiable — the tool-calls endpoints accept no `sort` parameter.** The AC reads "sorting by `wait_ms` desc issues the right query params". | `server/api/openapi.yaml:756-767` (`listToolCalls`) and `:473-483` (`listSessionToolCalls`) both declare only `Project`/`Tool`/`DecisionSource`/`From`/`To`/`Limit`/`Cursor` — no `sort`, and also no `correlation` or `session` filter. Confirmed against the generated `web/src/api/schema.d.ts`. Contrast `GET /api/v1/sessions`, which does have `sort`. | Either add `sort` to both endpoints (server work, outside Phase 4) or reword the AC. As shipped: no fabricated `sort` param is ever sent (asserted), and sorting is a client-side reorder of the **loaded page only** — materially weaker than sorting the result set, and documented as such in `toolsStore` and `ToolCallTable`. The ticket's scope line "sortable, keyset paginated" is therefore only partly delivered. |
| D-28 | **Five of PLAN P4-09's six named data-quality tiles have no backing API field.** The ticket names partial sessions, unknown-kind events 24 h, clock-skewed 24 h, dropped total, heuristic tool-call share, and oldest raw event. Only unknown-kind events is obtainable. | `components['schemas']['DataQuality']` carries exactly four booleans (`logs_exporter_seen`, `metrics_exporter_seen`, `hooks_seen`, `tool_details_seen`) and no counts. `SessionSummary.partial` is per-session; `TimelineEvent.clock_skewed` is per-event; `ToolCall.correlation` is a per-row enum — none is aggregated by any endpoint. `dropped_total` occurs at exactly one place in the whole schema: `StreamStatsFrame` (the SSE stream, Phase 5). "Oldest raw event" has no field anywhere. | Either extend `/api/v1/meta`'s `data_quality` block with these aggregates, or reduce the ticket to what the API exposes. As shipped, the five unbacked tiles render `—` with a new `NOT_EXPOSED_BY_API` reason rather than summing to a fabricated `0`; SPEC §4.1 forbids the silent zero, and a data-quality screen reporting "0 dropped events" when it cannot know would undermine the one view whose job is telling an operator whether to trust the numbers. Consequently P4-09's AC "a non-zero dropped count renders in the warn colour" is tested against the real backed analogue, with the dropped-total logic kept identical for when the field lands. |
| D-29 | **`argusd sim --mode=demo` does not spread sessions over `--backfill`; only their events are spread.** All 20 session rows start within a ~3.4 s span while their events span 14 days. | Measured on a live stack: `min(started_at)=15:13:45.94`, `max(started_at)=15:13:49.30`, `max(last_event_at)=15:18:20.94`, while `analytics/summary` over `-1h`/`-24h`/`-7d`/`-30d` returns `$1.23`/`$4.24`/`$19.73`/`$39.78` — i.e. the event timestamps really do cover the full backfill. | Decide whether this is intended. The consequence for Phase 4: `/analytics` defaults to a 24 h window (SPEC §4.1) and therefore honestly shows ~11 % of demo cost with thin charts, while `/sessions` (unbounded by default) shows everything as seconds old. Neither view is wrong; the demo data is simply shaped so the two disagree at first glance. The capture harness now takes both a default-window and a `?window=30d` analytics screenshot so a design review sees both states. |

---

## Corrections to my own earlier analysis (recorded because a wrong diagnosis reads as authoritative)

Phase 3's ledger makes the point that a wrong diagnosis is worse than none. Two of mine:

1. **I briefly suspected the analytics rollup of under-reporting cost**, having seen a
   session-cost sum of `$39.7778` against an `analytics/summary` of `$24.31`. That was wrong.
   Measured apples-to-apples on one stack, `from=-30d` returns **exactly** `$39.7778` and `1249`
   tool calls, matching the sessions projection to the cent. The earlier gap was a window
   artefact (D-29), not a rollup defect. The rollup and the projection agree.

2. **My first fix for the harness's empty-analytics problem was itself wrong.** I used a
   "stop when N consecutive reads are identical" convergence check; it accepted `$24.31/955`
   as final when the truth was `$39.78/1234`, because the rollup plateaus ~35 s between fill
   steps and the stability window landed inside a plateau. Replaced with equality against the
   synchronously-written sessions projection. Recorded because the first version *looked* like
   a careful fix and would have shipped partial numbers that appear real.

---

## Interpretation calls (no ruling expected)

| Area | Call made | Why |
|---|---|---|
| `sessionDetailStore` LRU eviction | True least-recently-**used**, not first-inserted | PLAN P4-03's worked example ("load A,B,C; back to A; load D; A refetches") describes FIFO-by-insertion and contradicts the ticket's own prose, which says "evict least-recently-*used*". The prose won: revisiting A makes it most-recent, so D evicts B. Both halves are asserted — a cache hit must not refetch, and the evicted entry must. This deviation is against the lead's prompt wording, which was wrong, not against the ticket. |
| `collapseEvents` Δts anchor | Compared against the group's **first** member | SPEC §1.5.3(b) gives `\|Δts\| ≤ 2000 ms` without naming the reference. Anchoring to the previous member lets a chain of events 1.5 s apart merge transitively into a group spanning far more than the window, defeating its purpose: the window bounds how stale a merged row's data can be. |
| `collapseEvents` and `clock_skewed` | A skewed event never merges on the basis of `ts` | A skewed clock makes `\|Δts\|` meaningless. SPEC §1.5.3(b) states collapsing is heuristic and that users debugging their own pipeline need the raw stream, so refusing to merge is strictly safer than guessing permissively or restrictively. |
| Reject rate over zero tool calls | `—`, not `0%` | 0/0 is undefined, and SPEC §6.1's null-vs-zero rule extends to a derived ratio. Note `SessionSummary.tool_call_count` is a non-nullable `number`, so the null-count branch is unreachable through the typed schema and is defensive only; the reachable honesty case is the zero-denominator one. The *subagent node* `tool_call_count` **is** nullable (SPEC §1.9) — opposite rule, different field. |
| `wait_ms` null reason | `NOT_MEASURED`, not `NO_HOOK_COVERAGE` | A live seed-42 row has non-null `decided_at` and `correlation: otel_only` yet still null `wait_ms`, so blaming missing hooks would be a guess presented as an explanation. |
| `/analytics` query-source breakdown | Deliberately not fetched | `dimension=query_source` is sourced from session-lifetime `sessions.cost_by_query_source`, not `rollup_hourly` (SPEC §4.3), so its total legitimately disagrees with a windowed summary. Rendering it on a windowed dashboard would ship a quietly wrong number. The split is shown where it belongs: the session's `CostAttributionCard`, labelled "whole-session-lifetime figures (not windowed)". |
| `not_attributable` tiles | Driven by the server's `not_attributable[]` array | Duplicating the field list client-side guarantees drift, and the server is the authority on what it could not attribute. |
| `DecisionMatrix` axis labels | Raw values reach the DOM via an HTML legend, not the canvas axis | ECharts axis labels are canvas text and cannot host a Vue component, so the literal "every source goes through `RawValue`" could not be met on the axis. The verbatim string still reaches the DOM. |
| Coverage measurement scope | `src/components/ui/**` and test scaffolding excluded | shadcn-vue registry output is copied in verbatim; we test our own components that use reka-ui primitives, not reka-ui. Measuring it put ~20 files at 0 % into the global number purely as a function of how many primitives the phase installed. **Thresholds were not changed** — final measured coverage clears the original gate on every axis. |
| `RawValue.vue` reports 0 % coverage | Left measured, drag accepted | `@vitest/coverage-v8`'s `remapCoverage()` sporadically loses `startOffset` for `.vue` files — acknowledged in the installed provider's own source (`node_modules/@vitest/coverage-v8/dist/provider.js:35`, "mergeProcessCovs sometimes loses startOffset, e.g. in vue"), with the workaround at :37-39 defaulting it to 0. A probe (`throw` in the script block, all 5 tests red) proved the module really executes. Most `.vue` files measure correctly, so a blanket `.vue` exclusion would discard real measurement to work around one file, and an exclusion outlives the bug that motivated it. |
| `useCaptureReady` | New composable, not in SPEC §6.3's component list | The screenshot harness needs one stable selector across all views and must never photograph a skeleton. Each view declares when its first fetch has settled (data, empty, **or** error — all three are legitimate to photograph). |
| Virtualized session rows above 200 | `role="table"` div grid, not `<table>` | `useVirtualList`'s absolute positioning cannot live inside a `<tbody>`. The ≤200-row path stays a semantic `<table>`, which is what a11y and the harness see. |
| `Timeline` sort/refetch on `agentId` | Added a watcher in `Timeline.vue` | See the integration defect below. |

---

## Integration defect found only by wiring the tabs together

**An agent filter applied from the Subagents tab left the Timeline unfiltered.**

`SubagentTree`'s node click routes to `?tab=timeline&agent_id=…`; `SessionDetailView`'s query
watcher calls `store.setTimelineFilters({ agentId })`, which is a pure state setter; the store's
`loadTimeline` does send `agent_id`. Every layer was individually correct and individually
tested — P4-05 asserts the store's `agentId` lands, P4-04 asserts the kind chips refetch — and
nothing asserted that an externally-set agent filter changes the rendered events. `Timeline.vue`
watched its own kind chips and paired each with a `loadTimeline`, but had no watcher on
`store.agentId`, so the filter landed and the events silently stayed unfiltered.

This is the same shape as Phase 3's two shipped-but-for-a-live-run defects: a gap between
components that were each correct on their own. Fixed by a watcher in `Timeline.vue`, with two
tests **confirmed to fail against a stubbed-out watcher body before being kept** — a regression
test that has never been seen red is only asserting its author's belief.

---

## Process notes

- Ten tickets ran as Sonnet subagents. Two returned a deviation the lead had asked them to
  justify rather than silently following the prompt: the LRU semantics (the prompt's worked
  example was wrong) and the missing `sort` param (the AC was unsatisfiable). Both were right to
  push back, and both are recorded above.
- One self-inflicted process error: the lead edited `eslint.config.js` while a subagent was
  running, and that agent correctly reverted it as foreign damage. Every subsequent prompt
  carried an explicit no-revert/no-stash rule plus "a failure in a file you do not own is not
  yours to fix". No further collisions occurred across the remaining nine tickets.
- The shadcn-vue CLI re-added a `@import url('https://fonts.googleapis.com/…')` to `theme.css`
  on its second invocation (adding `sonner`), having had it removed on the first. Caught only by
  reviewing the diff before committing. The embedded SPA must not fetch a stylesheet from the
  network — it breaks air-gapped deployments and makes screenshot capture non-deterministic.
  Worth a lint or CI check if the CLI is invoked again.
- `golangci-lint` is not installed on this workstation, so `make ci`'s `lint` target cannot run
  locally. Phase 4 changed **zero** Go files (`git diff main...HEAD -- server/` is empty), so the
  Go lint status is unchanged from `main`; CI is unaffected. Every other `make ci` step was run
  individually and passed.
