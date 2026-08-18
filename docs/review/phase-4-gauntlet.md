# Phase 4 — the visual gauntlet

Phase 4 shipped functional in ten tickets (P4-01…P4-10), then went through a **visual gauntlet**:
each screen was screenshotted from the real embedded-asset build against a live
`argusd sim --mode=demo --seed=42` stack (`scripts/ui-capture.sh`), handed to a critic, and rebuilt
until the critic had no gap left worth a round. The functional acceptance criteria were never
relaxed to make a design round easier — every round is additive on top of a green suite.

This file records what the gauntlet actually found, because the *commits* say what changed and the
critic gaps say **why it needed changing**. Rounds are numbered per part, in commit order; where a
commit names its own round number that number is used verbatim.

---

## Part 1 — Sessions list (`/sessions`)

| Round | Commit | Critic gap in one line | Response |
|---|---|---|---|
| R1 | `ca86836` | The table encoded no severity in colour — Reject %, Cost and the status dot were all the same neutral grey, so no row stood out and nothing was scannable without reading every cell. | `lib/severity.ts` (fixed 5 %/15 % reject thresholds; cost graded against p75/p90 of the *visible* set, not an invented absolute); magnitude columns right-aligned with `tabular-nums`. |
| R2 | `16e5a23` | Native `<input type=date>` and `<select>` chrome rendered in the browser's **light** UA theme regardless of our dark tokens, and the filter bar read as loose controls floating on the page. | `color-scheme: dark/light` declared in `theme.css` (Chromium ignores background/border overrides on the calendar icon and caret without it); filter bar contained in a bordered toolbar surface; theme Switch demoted to a ghost icon button. |
| R3 | `f079285` | The toolbar's `flex-wrap` broke *between* From and To, stranding "To" alone on a second row beside a large dead gap. | Both nested under one flex wrapper so the pair wraps atomically. |
| R4 | `b99f798` | From/To were still bare native date inputs — off-palette and inconsistent with the toolbar they now sat in. | Segmented 24h/7d/30d/All preset control plus a `Custom…` popover of token-styled text fields. Presets write the API's own relative shorthand (`-24h`/`-7d`/`-30d`) into the existing `from`/`to` URL params, so the round-trip contract and its tests were untouched. Also a "loaded set" aggregate strip, explicitly labelled as covering the fetched page and **not** a global total. |

**Verdict: pass.** Exit criteria 1 and 9 hold; the URL round-trip and its tests survived four rounds
of visual change unmodified, which is the point of having them.

---

## Part 2 — Session detail (`/sessions/:id`)

| Round | Commit | Critic gap in one line | Response |
|---|---|---|---|
| R1 | `67f0dcb` | The timeline was a flat list of uniform rows with no sense of which tool calls, LLM requests and decisions belonged to which turn — and the KPI strip plus header ate ~40 % of the viewport before a single event. | `TimelineGroup` nests a turn's events in an indented left-bordered rail under a collapsible header; KPI strip collapsed from six bordered cards to one divided band. **Found a real ordering bug in the process:** grouping by a global `prompt_id` Map pulled every no-turn event into one leading block, so a turn header could render *below* events that occurred after it. Switched to contiguous-run grouping, with a regression test. |
| R2 (round-3) | `33292a7` | The timeline had no visible detail pane — `EventDetailSheet` already fetched full attrs but was overlay-only, so tree and detail could never be on screen together. | `EventDetailContent` (structured summary above the raw-attrs `JsonViewer`) rendered either in a persistent right-side `EventDetailPanel` on wide viewports or the existing overlay `Sheet` on narrow ones; `toolThreads.ts` nests a call's decision/permission/result under it by `tool_use_id`. |
| R3 (round-4) | `5ae9c5e` | A dozen identical "LLM request" rows repeating the same absolute date made the timeline unscannable. | `rowDetail()` surfaces the model (LLM rows) / tool name (tool rows); the repeated absolute date is replaced by an offset from session start plus a log-1p-scaled duration bar. `formatRelativeOffset` deliberately always resolves to whole seconds — a real capture had `started_at` ~11 days after its own earliest events, and a coarser "Nd HHh" format collapsed every row onto one label, i.e. re-created the exact bug the feature exists to fix. |
| R4 (round-5) | `3fcf478` | Two-line rows with a repeated four-unit signed offset spent ~60 px per row on ~4 short fields. | One dense line per row (<32 px), offset/duration/cost/tokens as fixed-width right-aligned `tabular-nums` columns. The offset anchor moved from `session.started_at` to the **first event in the loaded timeline** (`originTs`) — the old anchor produced multi-day offsets whenever a session's recorded start drifted from its earliest event. |
| R5 | `f9b3e8e` | The Subagents tab read as informationally empty next to an always-expanded cost table that dominated it — the product's structural differentiator was the smallest thing on screen. | No task/description field exists in the schema to promote, so nodes got everything that *is* real and derivable: an `i/n` ordinal disambiguating same-`agent_type` siblings, a per-node tool-name breakdown grouped from `ToolCall.agent_id`, an always-visible `agent_id`. `CostAttributionCard` collapsed to one muted summary line by default. |

**Verdict: pass.** Exit criteria 2, 3 and 4 hold. Note R5's constraint — the honest answer to "these
nodes look empty" was *derive more from real fields*, not invent a label.

---

## Part 3 — Analytics (`/analytics`)

| Round | Commit | Critic gap in one line | Response |
|---|---|---|---|
| R0 (global) | `8762faf` | Cost rendered in a destructive-adjacent red, so measured spend read as an error; and shadcn's default blue/green/orange/purple/red series palette clashed with the semantic accept/reject/warn hues, letting a generic chart series read as a good/bad claim. | `--cost` resolves to `--foreground` (neutral measured data); one deliberate primary accent; a 5-hue categorical palette at matching chroma/lightness held clear of the semantic hues. Shared chart chrome (`chartLegend`, `slimDataZoom`, `withAlpha`) so the three chart components stop hand-rolling it. `echartsTheme.test.ts`'s fallback assertions were **updated to the new token values, not deleted**. |
| R1 | `f74078a` | The 12 KPI tiles were flat, equal-weight numbers with no comparison or trend context. | Every tile gets a real delta (current window minus the immediately preceding window of equal length, **fetched separately, never fabricated**); the eight metrics a per-bucket timeseries actually backs also get a sparkline. LOC/active-time/reject-rate have no per-bucket metric at all, so they get a summary-derived delta and a "no trend data" tooltip rather than a fabricated flat sparkline. |
| R2 (round-3) | `4788b9d` | Sparklines, deltas and series all rendered in one undifferentiated blue/grey, with a near-invisible dashed token line. | `METRIC_SEMANTICS` table drives metric-aware hue *and* honest delta polarity — more cost isn't bad, more errors is. A lone series renders solid/weighted rather than dashed/muted. |
| R3 | `4b7ca24` (shared) | — see Part 4; no analytics-specific gap remained. | |

**Verdict: pass.** Exit criteria 5, 6 and 8 hold. Two honesty properties were defended *against*
design pressure here: deltas are fetched, never computed from thin air, and the metrics with no
backing timeseries say so instead of drawing a flat line.

---

## Part 4 — Secondary screens (`/tools`, `/data-quality`)

| Round | Commit | Critic gap in one line | Response |
|---|---|---|---|
| R1 | `4b7ca24` | **Data quality:** 4 of 6 tiles rendered their explanation paragraph as a *sibling* of the `StatTile` card rather than inside it, so half the grid had prose spilling past the card border and the other half didn't — ragged bottoms, a self-contradicting layout. **Tools:** ~20 % of the table's width was dead, held by Wait/File columns that were null in every loaded row. | `StatTile` gained a `summary` (one muted always-visible line) / `description` (full text behind an info-icon tooltip) pair so every tile's content lives inside one card and the six collapse to equal per-row heights. Tools: Duration right-aligned with a sort affordance; Wait/File columns dropped entirely when every loaded row is null, with a muted note; client-side tool-name search over the loaded page (no server-side free-text param exists for this endpoint). |

**Verdict: pass.** Exit criterion 7 holds.

---

## Harness changes made during the gauntlet

- `aa84987` — the capture harness accepted an empty analytics dashboard as final. Its original
  "stop when N consecutive reads are identical" convergence check accepted `$24.31/955` as the
  answer when the truth was `$39.78/1234`, because the hourly rollup plateaus ~35 s between fill
  steps and the stability window landed inside a plateau. Replaced with equality against the
  synchronously-written sessions projection. This is worth re-reading: the first fix *looked*
  careful and would have shipped partial numbers that appear real.
- `abda638` — `COMPOSE_PROJECT_NAME` is overridable so sibling gauntlet builders running the
  harness concurrently don't collide on container names.
- `33292a7` — added the `session-detail-inspector.png` capture, with a decision-bearing (or
  costliest) row pre-selected, so the inspector is never photographed empty.

---

## Process note: the viewer-inversion incident

**One reported gap was not real, and the reporting pipeline caused it.**

A critic round reported that the sessions capture had rendered in the **light** theme. It had not.
The PNG was dark; the image pipeline that displayed it to the critic inverted it.

The correct resolution was to stop trusting the rendered view and sample the pixels
(`f079285` records it): the sessions capture's background is `rgb(23, 23, 23)`. The app's theme
precedence (`stores/ui.ts`, the anti-flash script in `index.html`) and the harness's explicit
`colorScheme: 'dark'` plus `localStorage` seed (`web/scripts/ui-capture.mjs`) were all unchanged
and working, and the light-theme report did not reproduce.

**The rule this produces, for anyone reviewing captures from this harness:** the PNGs are dark.
Some image-rendering paths present them inverted, and an inverted dark screenshot is a completely
plausible-looking light-theme screenshot — there is no visual tell. Before filing *any* colour,
contrast or theme gap against a capture, sample the actual pixel values (e.g. PIL
`Image.open(p).convert('RGB').getpixel(...)`) and confirm the background is dark. A design round
spent "fixing" a theme that was never broken is worse than a round not run: it edits working code
on the strength of an artefact.

---

## Deviations recorded by the substrate lead

The four Phase-4 deviations needing an owner ruling were recorded before the gauntlet, in commit
`588d098`, and remain open. Full evidence and recommendations are in
[`phase-4-deviations.md`](./phase-4-deviations.md); the gauntlet changed **none** of them.

| ID | Deviation | Status after the gauntlet |
|---|---|---|
| **D-26** | The synthetic root subagent node returns a `status` outside `SubagentStatus`'s declared enum — `subagent_tree.go:124` casts the *session* status vocabulary (`active\|ended\|abandoned\|unknown`) straight into it. | Unchanged; server-side. The UI is safe either way — node `status` goes through `RawValue`, tested against `"ended"` and an invented value. |
| **D-27** | PLAN P4-06's sort AC is unsatisfiable: neither tool-calls endpoint declares a `sort` param. | Unchanged. No fabricated `sort` is ever sent (asserted); sorting is a client-side reorder of the loaded page only, and R1 above added the sort *affordance* without changing that limit. Scope line "sortable, keyset paginated" remains only partly delivered. |
| **D-28** | Five of P4-09's six named data-quality tiles have no backing API field. | Unchanged. The five render `—` with a `NOT_EXPOSED_BY_API` reason rather than a fabricated `0`. R1's tile-containment fix applies to all six identically, so the honest `—` is now *more* legible, not less. |
| **D-29** | `argusd sim --mode=demo` spreads only events over `--backfill`, not sessions, so `/analytics` (24 h default) and `/sessions` (unbounded) legitimately disagree at first glance. | Unchanged. The harness takes both a default-window and a `?window=30d` analytics capture so a design review sees both states rather than concluding the dashboard is broken. |

---

## New finding at the exit gate — RULING NEEDED (candidate D-30)

Verifying exit criterion 8 needed `--cost-mode=omit` data, which the default demo seed does not
produce, so a second sim was run against a live stack
(`sim --mode=demo --seed=99 --cost-mode=omit`) on top of the seed-42 data. The notice rendered
correctly — and the run exposed an unrelated server-side honesty bug.

**The sessions projection reports a measured `$0.00` for cost it cannot know, while the analytics
rollup estimates the same traffic at $18.36.**

Measured on that stack, 40 sessions total (20 seed-42 `reported`, 20 seed-99 `omit`):

| Source | Reported | Estimated | Total |
|---|---|---|---|
| `GET /analytics/summary?from=-30d` | `$39.777844` | `$18.36485232` | `$58.14269632` (`estimated_share` 0.3159) |
| `GET /sessions` — each of the 20 omit-mode rows | `0` | **`0`** | **`0`** (`estimated_share` `0`) |

Every one of the 20 omit-mode sessions returns
`cost: {usd: 0, reported_usd: 0, estimated_usd: 0, estimated_share: 0, by_query_source: {…all 0}, dominant_query_source: ""}`.
The rollup priced that same traffic from `model_prices`; the per-session projection did not run the
estimator at all and emitted a hard zero instead.

**Why this matters more than an arithmetic gap:** the UI renders the API faithfully, so a session
detail page for an omit-mode session shows a KPI strip reading **`Cost $0.00`** and a
`CostAttributionCard` reading **"$0.00 of $0.00"** — captured, not inferred. That is precisely the
silent zero SPEC §6.1 forbids, on the screen whose entire job is being honest about what Argus
does and does not know. `NullValue` cannot help here: the field is a non-null `0`, so the UI has
nothing to distinguish "cost was zero" from "cost is unknown".

**This is not a Phase-4 UI defect** — no client change can fix it, and Phase 4 changed zero Go
files. Recommendation: either run the same estimator in the sessions projection that
`rollup_hourly` already runs, or make the projection's `usd` nullable so an unpriceable session
reads `—`. Do not leave the two projections disagreeing while one of them lies in the honest
direction's favour.

---

## Post-gauntlet smoothing

The gauntlet's rounds were built by parallel builders, so a consolidation pass followed. What it
found is itself a result: **no hardcoded colours, no token violations, no naming drift
(`data-testid` is kebab-case throughout, `originTs` is used consistently), no unused props, no
orphaned components.** The parallel builders held the design language. Three real seams were
closed:

- `pickByRank` (`collapseEvents.ts`) and `pickDisplayField` (`toolThreads.ts`) were the same
  "argmax over non-null members" loop written twice with different precedence tables. The loop is
  now generic and shared; the tables stay per-caller.
- `formatCostPrecise` (`format.ts`) had no caller outside its own test.
- `deltaColor` (`echartsTheme.ts`) had no caller at all and re-implemented `StatTile`'s live
  `deltaClass` polarity table in canvas-colour vocabulary. `StatTile.test.ts` already asserts the
  identical matrix at the DOM level, where it actually ships, so removing the duplicate lost no
  assertion.
- `SetupCard`'s step 3 hardcoded `--target http://localhost:8080` while steps 1 and 2 substituted
  the live origin — a copied sim command would have seeded a *different* Argus than the one on
  screen and appeared to do nothing. Now substituted like the others, with a test that was
  confirmed red against the old hardcoded value first.

---

## Phase-4 exit criteria — verification at the gate

Each criterion below was checked at the exit, not assumed from the round that built it. "Live"
means measured against a `docker compose` stack seeded by `argusd sim` and either read back through
the API or photographed by `scripts/ui-capture.sh`.

| # | Criterion | Verdict | Evidence |
|---|---|---|---|
| 1 | `/sessions` lists ≥ 20 sessions; project and status filters change the result set, reflect in the URL, survive reload | **Pass**, with one part test-only | Live: 20 sessions; `?project=studio` → 5 rows all `studio`; `?status=active` → 4 rows all `active`. `sessions.png` shows "20 loaded". URL sync and reload survival are asserted in `stores/__tests__/sessions.spec.ts`, **not** re-driven in a browser at the gate. |
| 2 | Clicking a row opens `/sessions/:id` with a KPI strip whose cost matches the list row | **Pass** | Live, and stronger than asked: the list-row `cost` object was compared to the detail response's for **all 20** sessions — 0 mismatches. |
| 3 | Timeline shows turn-grouped events (out-of-turn under their own header); decisions show a `DecisionBadge` naming provenance; clicking an event opens the drawer with raw `attrs` from `/events/{ref}` | **Pass** | `session-detail-timeline.png` (Turn 0, "Turn 0 · continued", de-emphasised "No turn" groups, `accept`/`reject` badges with `User (always)` / `Config` provenance) and `session-detail-inspector.png` (structured summary + `event_ref`, `tool_use_id`, `source: otel_log`, raw-attrs JSON). |
| 4 | Subagents tab renders a depth-2 tree with per-node tool counts, `—` per-node cost with the explanatory tooltip, and a `CostAttributionCard` showing `by_query_source` including `sdk` | **Pass** | `session-detail-subagents.png`: every node's cost column is `—`, caveat reads "Claude Code does not emit per-agent cost; api_request carries query_source only", dominant source `sdk`. Live `by_query_source` keys: `""`, `a_future_query_source`, `auxiliary`, `generate_session_title`, `main`, `sdk`, `subagent` — so both the `sdk` case and an unknown value are exercised by real data. A second stack rendered a depth-3 tree with `1/8…8/8` ordinals. |
| 5 | `/tools` lists cross-session tool calls and is reachable from a decision-matrix cell with the filter applied | **Pass** | `tools.png` (50 loaded; `hook_only`, `heuristic`, `otel_only` correlations visually distinct; `wait_ms` renders `—`, never `0ms`; `an_invented_decision_source` verbatim). Live: `?decision_source=user_reject` → 42 rows, all that source, so a matrix link arrives filtered. |
| 6 | `/analytics` renders a cost timeseries, model breakdown and decision matrix; range change refetches; charts follow the theme toggle; a model filter renders `—` tiles rather than zeros | **Pass**, matrix below the fold | `analytics.png` (24 h) and `analytics-30d.png`: cost timeseries with a `model` `group_by` switch, model legend, sparklines/deltas. The decision matrix sits below the 900 px capture fold; its render, cell-click emit and the model-filter `—` rule are asserted in `DecisionMatrix.test.ts` and `AnalyticsView.test.ts`. Theme bridging is asserted in `echartsTheme.test.ts`. |
| 7 | `/data-quality` renders the quality tiles, unknown-kind table and hook-latency panel | **Pass** | `data-quality.png`: six equal-height tiles fully inside their cards, five reading `—` (`NOT_EXPOSED_BY_API`, D-28) and the one backed tile reading `0`; unknown-kind table in its empty state; hook-latency panel with `PostToolUse` p50/p95/p99 = 8/20/22 ms. |
| 8 | With `--cost-mode=omit` data, the estimated-cost notice is visible | **Pass** | Not observable in the default seed (`estimated_share` 0), so a `--cost-mode=omit` sim was seeded. `analytics/summary` → `estimated_share` 0.3159; the notice renders: "31.6% of this window's $58.14 cost is estimated, not reported by the vendor." **This run also surfaced candidate D-30 above.** |
| 9 | Empty database → `/sessions` shows the setup instructions (env vars incl. `OTEL_LOG_TOOL_DETAILS=1`, hook JSON, sim command), not a blank table | **Pass**, test-only | Asserted in `SessionListView.test.ts` + `SetupCard.test.ts` (7 tests) against a zero-session store. Not photographed — the capture harness seeds data by design, so it can never render this state. All three blocks now substitute the live origin (the step-3 fix below). |
| 10 | `pnpm unit` covers `collapseEvents`, the session store's filter/URL sync, the null-rendering rule, the unknown-vocabulary rule, and ≥ 3 component render tests; `pnpm build` output loads from the embedded-asset server | **Pass** | 696 tests green. `collapseEvents.test.ts`, `stores/__tests__/sessions.spec.ts`, `NullValue.test.ts`, `RawValue.test.ts`, and far more than 3 component render tests. The embedded-asset requirement is proved by the captures themselves: `scripts/ui-capture.sh` builds `server/Dockerfile`, which compiles the SPA and embeds it, so every screenshot above is of the embedded build, not a dev server. |

**Two criteria are honestly weaker than the words suggest**, and neither is caused by the gauntlet:

- **1 (URL/reload)** and **9 (empty database)** are covered by unit tests only. The harness cannot
  photograph an empty database (it seeds one on purpose) and does not exercise reload.
- **D-27** still caps criterion 5's spirit: `/tools` sorting reorders the loaded page, not the
  result set, because neither endpoint accepts `sort`.
