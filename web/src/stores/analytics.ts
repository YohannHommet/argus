import { computed, reactive, ref } from 'vue'
import { defineStore } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import type { LocationQuery, LocationQueryRaw } from 'vue-router'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { computeDelta, seriesDelta, sparklineValues } from '@/lib/analyticsDelta'

export type Summary = components['schemas']['Summary']
export type Series = components['schemas']['Series']
export type Breakdown = components['schemas']['Breakdown']
export type DecisionMatrix = components['schemas']['DecisionMatrix']
export type TimeseriesMetric = components['schemas']['TimeseriesMetric']
export type BreakdownDimension = 'model' | 'project' | 'tool' | 'decision_source' | 'query_source' | 'error_type'
export type BreakdownMetric = 'cost' | 'calls' | 'tokens'

/** SPEC §4.3: default analytics window when nothing is in the URL yet. */
export const ANALYTICS_PRESETS = ['24h', '7d', '30d', 'custom'] as const
export type AnalyticsPreset = (typeof ANALYTICS_PRESETS)[number]
export const DEFAULT_PRESET: AnalyticsPreset = '24h'

const PRESET_FROM: Record<Exclude<AnalyticsPreset, 'custom'>, string> = {
  '24h': '-24h',
  '7d': '-7d',
  '30d': '-30d',
}

/** Same presets, as milliseconds — used only to size the *preceding* window for KPI deltas (below). */
const PRESET_DURATION_MS: Record<Exclude<AnalyticsPreset, 'custom'>, number> = {
  '24h': 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
  '30d': 30 * 24 * 60 * 60 * 1000,
}

/**
 * `TimeseriesMetric`s the KPI strip can plot a sparkline for (round-5 UI
 * pass). `cost`/`tokens` are deliberately excluded here — the strip reuses
 * `costSeries`/`tokenSeries`, already fetched for the two chart panels
 * (the "sum(series)+other" total is invariant under `group_by`, see
 * `lib/analyticsDelta.ts`, so no separate current-window fetch is needed
 * for those two). `sessions`/`turns`/`api_requests`/`api_errors`/
 * `tool_calls`/`tool_rejects` have no other consumer, so they get their
 * own dedicated (always `group_by=none`) current+previous fetch pair.
 * `loc` is excluded too: the API only reports it as one combined
 * added+removed number (SPEC), which would misrepresent either the "LOC
 * added" or "LOC removed" tile's own trend — those two tiles get a delta
 * (from `previousSummary`, which *does* split added/removed) but no
 * sparkline. `active_seconds`/`reject_rate` have no `TimeseriesMetric` at
 * all (they're summary-only derived figures) — same treatment.
 */
export const KPI_SPARKLINE_METRICS = ['sessions', 'turns', 'api_requests', 'api_errors', 'tool_calls', 'tool_rejects'] as const
export type KpiSparklineMetric = (typeof KPI_SPARKLINE_METRICS)[number]

/** `group_by` switch on the cost timeseries chart. */
export const GROUP_BY_OPTIONS = ['model', 'project', 'vendor', 'none'] as const
export type GroupBy = (typeof GROUP_BY_OPTIONS)[number]
export const DEFAULT_GROUP_BY: GroupBy = 'model'

export interface AnalyticsFilters {
  project: string[]
  model: string[]
  vendor: string[]
}

export function emptyAnalyticsFilters(): AnalyticsFilters {
  return { project: [], model: [], vendor: [] }
}

/**
 * Only `llm.request` events carry a model, so only these timeseries metrics are
 * model-attributable (SPEC §4.3 "Model-filtered requests"). `sessions`, `turns`,
 * `tool_calls`, `tool_rejects`, `loc` are not in this list on purpose.
 */
export const ATTRIBUTABLE_TIMESERIES_METRICS = ['cost', 'tokens', 'api_requests', 'api_errors'] as const

/**
 * Breakdown dimensions with no model column to filter on (SPEC §4.3): a
 * `?model=` request against any of these is refused rather than silently
 * dropping the filter and returning fleet-wide totals that look filtered.
 */
const NON_MODEL_BREAKDOWN_DIMENSIONS = ['tool', 'decision_source', 'error_type', 'query_source'] as const

export interface AttributabilityCheck {
  endpoint: 'summary' | 'timeseries' | 'breakdown' | 'decisions'
  hasModelFilter: boolean
  metric?: string
  dimension?: string
}

/**
 * SPEC §4.3's "Model-filtered requests" rule, as one predicate every fetch in
 * this store consults before issuing a `?model=` request — rather than a
 * hand-rolled `if` at each of the store's eight call sites, which is exactly
 * how a future ninth call site would forget the rule.
 *
 * Refusal table (`hasModelFilter: false` is always attributable — none of
 * this applies without a model filter active):
 *
 *  1. `timeseries` + `metric` not in {cost, tokens, api_requests, api_errors}
 *     — e.g. `metric=sessions` — 400 `urn:argus:error:not-attributable`.
 *  2. `breakdown` + `dimension=tool` — tool_calls rows have no model column.
 *  3. `breakdown` + `dimension=decision_source` — same: no model column.
 *  4. `breakdown` + `dimension=error_type` — same: no model column.
 *  5. `breakdown` + `dimension=query_source` — same: no model column (and
 *     this dimension is session-lifetime scoped besides, see AnalyticsView's
 *     doc comment — this store never actually requests it).
 *  6. `breakdown` + `metric=calls` on *any* dimension — `rollup_hourly` books
 *     tool calls in the `model=''` group by construction, so a model-filtered
 *     call count could only ever silently read zero.
 *
 * `summary` and `decisions` are always attributable at the endpoint level:
 * `summary` degrades per-counter via its own `not_attributable[]` (server-
 * driven, see {@link useAnalyticsStore}'s `isNotAttributable`) rather than
 * refusing the whole request, and `getAnalyticsDecisions` takes no `model`
 * query parameter at all (SPEC §4.3) — there is nothing for a model filter
 * to even attach to.
 */
export function isRequestAttributable(check: AttributabilityCheck): boolean {
  if (!check.hasModelFilter) return true

  switch (check.endpoint) {
    case 'summary':
    case 'decisions':
      return true
    case 'timeseries':
      return (ATTRIBUTABLE_TIMESERIES_METRICS as readonly string[]).includes(check.metric ?? '')
    case 'breakdown':
      if (check.dimension && (NON_MODEL_BREAKDOWN_DIMENSIONS as readonly string[]).includes(check.dimension)) {
        return false
      }
      return check.metric !== 'calls'
  }
}

function queryToArray(value: LocationQuery[string]): string[] {
  if (value === undefined || value === null) return []
  const raw = Array.isArray(value) ? value : [value]
  return raw.filter((v): v is string => typeof v === 'string')
}

export interface AnalyticsUrlState {
  preset: AnalyticsPreset
  customFrom: string | null
  customTo: string | null
  filters: AnalyticsFilters
  groupBy: GroupBy
}

/**
 * Pure — parses a route query into window/filter/group_by state. Mirrors
 * `stores/sessions.ts`'s `parseFiltersFromQuery` convention: an unrecognised
 * `window`/`group_by` value degrades to the default rather than throwing, so
 * a hand-edited or stale URL never produces a broken page.
 */
export function parseAnalyticsQuery(query: LocationQuery): AnalyticsUrlState {
  const presetRaw = typeof query.window === 'string' ? query.window : DEFAULT_PRESET
  const preset = (ANALYTICS_PRESETS as readonly string[]).includes(presetRaw) ? (presetRaw as AnalyticsPreset) : DEFAULT_PRESET

  const groupByRaw = typeof query.group_by === 'string' ? query.group_by : DEFAULT_GROUP_BY
  const groupBy = (GROUP_BY_OPTIONS as readonly string[]).includes(groupByRaw) ? (groupByRaw as GroupBy) : DEFAULT_GROUP_BY

  return {
    preset,
    customFrom: preset === 'custom' && typeof query.from === 'string' ? query.from : null,
    customTo: preset === 'custom' && typeof query.to === 'string' ? query.to : null,
    filters: {
      project: queryToArray(query.project),
      model: queryToArray(query.model),
      vendor: queryToArray(query.vendor),
    },
    groupBy,
  }
}

/** Pure — inverse of {@link parseAnalyticsQuery}. Defaults and empty arrays are omitted. */
export function analyticsQuery(state: AnalyticsUrlState): LocationQueryRaw {
  const query: LocationQueryRaw = {}
  if (state.preset !== DEFAULT_PRESET) query.window = state.preset
  if (state.preset === 'custom') {
    if (state.customFrom) query.from = state.customFrom
    if (state.customTo) query.to = state.customTo
  }
  if (state.filters.project.length) query.project = state.filters.project
  if (state.filters.model.length) query.model = state.filters.model
  if (state.filters.vendor.length) query.vendor = state.filters.vendor
  if (state.groupBy !== DEFAULT_GROUP_BY) query.group_by = state.groupBy
  return query
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException ? err.name === 'AbortError' : (err as Error)?.name === 'AbortError'
}

/**
 * One independent fetchable slice of the dashboard (a KPI strip, a chart, a
 * breakdown, the decision matrix). Each of the store's eight resources gets
 * its own instance so a failure/abort/skip in one can never leak into
 * another — the AC's "an error in one of four requests renders that panel's
 * error state while the others render" reduces to each panel reading only
 * its own resource.
 *
 * `run` owns abort-the-previous-in-flight-call-on-a-new-one + a monotonic
 * run id so a superseded call's late settlement can never overwrite a newer
 * one's state (same shape as `sessions.ts`'s hand-rolled fetch, generalised
 * so it isn't repeated eight times over).
 *
 * `skip` is the not-attributable path: no network call at all, `data`
 * cleared, `notAttributable` set so the view can render an honest "not
 * available under this filter" panel instead of an empty chart or a fake
 * error.
 */
export interface ResourceState<T> {
  data: T | null
  loading: boolean
  error: ApiError | Error | null
  notAttributable: boolean
}

/**
 * `state` is a `reactive()` object (not a bag of individual `ref()`s) so a
 * consumer can write `analytics.costSeries.data` directly — in a template
 * or a `computed` — and get `T | null` back, not a `Ref<T | null>` that
 * still needs an explicit `.value` two property-accesses deep. Pinia only
 * auto-unwraps refs that are themselves top-level properties of a store's
 * setup() return; a ref nested inside a plain object two levels down (e.g.
 * `store.costSeries.data` where `costSeries` is a plain object holding a
 * `data: Ref<...>`) is not — `reactive()` sidesteps that entirely by never
 * introducing an inner `Ref` in the first place.
 */
function createResource<T>(): ResourceState<T> & {
  run: (task: (signal: AbortSignal) => Promise<T>) => Promise<void>
  skip: () => void
  abort: () => void
} {
  // Cast rather than `reactive<ResourceState<T>>(...)`: `T` is a plain OpenAPI response shape (no
  // refs ever nest inside it), but TS's generic `UnwrapNestedRefs<T>` can't know that structurally,
  // so it refuses to unify `UnwrapRef<T> | null` back with the declared `T | null`.
  const state = reactive({ data: null, loading: false, error: null, notAttributable: false }) as ResourceState<T>

  let controller: AbortController | null = null
  let runId = 0

  function abort(): void {
    controller?.abort()
  }

  function skip(): void {
    abort()
    runId += 1
    state.data = null
    state.error = null
    state.notAttributable = true
    state.loading = false
  }

  async function run(task: (signal: AbortSignal) => Promise<T>): Promise<void> {
    abort()
    controller = new AbortController()
    const { signal } = controller
    const id = ++runId

    state.notAttributable = false
    state.loading = true
    state.error = null

    try {
      const result = await task(signal)
      if (id !== runId) return
      state.data = result
      state.loading = false
    } catch (err) {
      if (isAbortError(err) || signal.aborted) return
      if (id !== runId) return
      state.data = null
      state.error = err instanceof Error ? err : new Error(String(err))
      state.loading = false
    }
  }

  return Object.assign(state, { run, skip, abort })
}

export type AnalyticsResource<T> = ReturnType<typeof createResource<T>>

/**
 * SPEC §4.3's fleet dashboard: a window (preset or custom range) + project/
 * model/vendor filters drive eight independent, coalesced, per-resource
 * fetches (summary, cost timeseries, token timeseries, model breakdown,
 * project breakdown, tool breakdown, error breakdown, decisions), all
 * cancelled and reissued together on a window/filter change, and only the
 * cost timeseries reissued on a `group_by` change.
 *
 * `dimension=query_source` deliberately has no resource here: SPEC §4.3
 * scopes it to `sessions.cost_by_query_source` (whole-session-lifetime,
 * live-verified ≈$39.78 vs. the same window's ≈$24.31 summary cost) rather
 * than `rollup_hourly` like every other breakdown — presenting it next to a
 * windowed KPI strip would misrepresent it as window-filtered. It belongs on
 * the session detail view, not here.
 */
export const useAnalyticsStore = defineStore('analytics', () => {
  const router = useRouter()
  const route = useRoute()

  const initial = parseAnalyticsQuery(route.query)
  const preset = ref<AnalyticsPreset>(initial.preset)
  const customFrom = ref<string | null>(initial.customFrom)
  const customTo = ref<string | null>(initial.customTo)
  const filters = ref<AnalyticsFilters>(initial.filters)
  const groupBy = ref<GroupBy>(initial.groupBy)

  /** True once every resource has settled at least once — this store's own "initial fetch round done". */
  const initialized = ref(false)

  const hasModelFilter = computed(() => filters.value.model.length > 0)

  const windowParams = computed<{ from?: string; to?: string }>(() => {
    if (preset.value === 'custom') {
      return { from: customFrom.value ?? undefined, to: customTo.value ?? undefined }
    }
    return { from: PRESET_FROM[preset.value], to: undefined }
  })

  const commonQuery = computed(() => ({
    from: windowParams.value.from,
    to: windowParams.value.to,
    project: filters.value.project,
    model: filters.value.model,
    vendor: filters.value.vendor,
  }))

  /**
   * The window immediately preceding the current one, same length, for
   * every KPI tile's period-over-period delta (round-5 UI pass). `null`
   * when it can't be computed at all (a custom range missing either
   * bound, or an inverted/zero-length one) — every delta then reads
   * `null` too (see `lib/analyticsDelta.ts`'s `computeDelta`), rather than
   * a fabricated comparison.
   *
   * Resolved to absolute ISO instants rather than `PRESET_FROM`'s relative
   * shorthand (`-24h`) because the current window's own `-24h` is resolved
   * server-side against the server's own "now" — this store has no way to
   * ask for "the 24h immediately before that" without first anchoring to
   * a concrete instant itself. `Date.now()` here and the server's "now"
   * for the current window can drift by network latency (milliseconds,
   * not enough to matter for a period-over-period comparison).
   */
  const previousWindowParams = computed<{ from: string; to: string } | null>(() => {
    if (preset.value === 'custom') {
      if (!customFrom.value || !customTo.value) return null
      const fromMs = Date.parse(customFrom.value)
      const toMs = Date.parse(customTo.value)
      if (Number.isNaN(fromMs) || Number.isNaN(toMs)) return null
      const duration = toMs - fromMs
      if (duration <= 0) return null
      return { from: new Date(fromMs - duration).toISOString(), to: customFrom.value }
    }
    const duration = PRESET_DURATION_MS[preset.value]
    const currentFromMs = Date.now() - duration
    return { from: new Date(currentFromMs - duration).toISOString(), to: new Date(currentFromMs).toISOString() }
  })

  const previousQuery = computed(() => {
    const w = previousWindowParams.value
    if (!w) return null
    return { from: w.from, to: w.to, project: filters.value.project, model: filters.value.model, vendor: filters.value.vendor }
  })

  const summary = createResource<Summary>()
  /** The preceding window's summary — read only for KPI deltas the timeseries endpoint can't back (LOC added/removed, active time, reject rate; see `KPI_SPARKLINE_METRICS`'s doc comment). */
  const previousSummary = createResource<Summary>()
  const costSeries = createResource<Series>()
  const tokenSeries = createResource<Series>()
  /** Preceding-window counterparts to `costSeries`/`tokenSeries`, always `group_by=none` — only their bucket totals are read (delta math), never rendered as their own chart. */
  const costPreviousSeries = createResource<Series>()
  const tokenPreviousSeries = createResource<Series>()
  const modelBreakdown = createResource<Breakdown>()
  const projectBreakdown = createResource<Breakdown>()
  const toolBreakdown = createResource<Breakdown>()
  const errorBreakdown = createResource<Breakdown>()
  const decisions = createResource<DecisionMatrix>()

  /** One current+previous `Series` pair per {@link KPI_SPARKLINE_METRICS} entry — the KPI strip's sparkline/delta source for metrics no other panel already fetches. */
  const kpiSeries: Record<KpiSparklineMetric, { current: AnalyticsResource<Series>; previous: AnalyticsResource<Series> }> = {
    sessions: { current: createResource<Series>(), previous: createResource<Series>() },
    turns: { current: createResource<Series>(), previous: createResource<Series>() },
    api_requests: { current: createResource<Series>(), previous: createResource<Series>() },
    api_errors: { current: createResource<Series>(), previous: createResource<Series>() },
    tool_calls: { current: createResource<Series>(), previous: createResource<Series>() },
    tool_rejects: { current: createResource<Series>(), previous: createResource<Series>() },
  }

  function fetchSummary(): Promise<void> {
    const client = useApiClient()
    return summary.run((signal) => unwrap(client.GET('/api/v1/analytics/summary', { params: { query: commonQuery.value }, signal })))
  }

  function fetchPreviousSummary(): Promise<void> {
    const q = previousQuery.value
    if (!q) {
      previousSummary.skip()
      return Promise.resolve()
    }
    const client = useApiClient()
    return previousSummary.run((signal) => unwrap(client.GET('/api/v1/analytics/summary', { params: { query: q }, signal })))
  }

  function fetchTimeseries(
    resource: AnalyticsResource<Series>,
    metric: TimeseriesMetric,
    gb: GroupBy,
    query: { from?: string; to?: string; project: string[]; model: string[]; vendor: string[] } = commonQuery.value,
  ): Promise<void> {
    if (!isRequestAttributable({ endpoint: 'timeseries', metric, hasModelFilter: hasModelFilter.value })) {
      resource.skip()
      return Promise.resolve()
    }
    const client = useApiClient()
    return resource.run((signal) =>
      unwrap(
        client.GET('/api/v1/analytics/timeseries', {
          params: { query: { ...query, metric, group_by: gb } },
          signal,
        }),
      ),
    )
  }

  /**
   * Fetches both halves of one {@link KPI_SPARKLINE_METRICS} entry: the
   * current window (the sparkline) and the immediately preceding one of
   * equal length (the delta's baseline). Both always `group_by=none` —
   * these resources back a single-number delta and a single trend line,
   * never a per-dimension breakdown. The preceding-window half is skipped
   * outright when {@link previousQuery} can't be computed (custom range
   * missing a bound) — its `data` then stays `null`, which
   * `lib/analyticsDelta.ts`'s `seriesDelta` already reads as "no delta".
   */
  function fetchKpiSeries(metric: KpiSparklineMetric): Promise<void[]> {
    const entry = kpiSeries[metric]
    const previousQ = previousQuery.value
    let previousFetch: Promise<void>
    if (previousQ) {
      previousFetch = fetchTimeseries(entry.previous, metric, 'none', previousQ)
    } else {
      entry.previous.skip()
      previousFetch = Promise.resolve()
    }
    return Promise.all([fetchTimeseries(entry.current, metric, 'none'), previousFetch])
  }

  function fetchBreakdown(resource: AnalyticsResource<Breakdown>, dimension: BreakdownDimension, metric: BreakdownMetric): Promise<void> {
    if (!isRequestAttributable({ endpoint: 'breakdown', dimension, metric, hasModelFilter: hasModelFilter.value })) {
      resource.skip()
      return Promise.resolve()
    }
    const client = useApiClient()
    return resource.run((signal) =>
      unwrap(
        client.GET('/api/v1/analytics/breakdown', {
          params: { query: { ...commonQuery.value, dimension, metric } },
          signal,
        }),
      ),
    )
  }

  function fetchDecisions(): Promise<void> {
    // getAnalyticsDecisions takes no `model`/`vendor` query param (SPEC §4.3) — only from/to/project.
    const client = useApiClient()
    return decisions.run((signal) =>
      unwrap(
        client.GET('/api/v1/analytics/decisions', {
          params: { query: { from: commonQuery.value.from, to: commonQuery.value.to, project: commonQuery.value.project } },
          signal,
        }),
      ),
    )
  }

  const retryCostSeries = () => fetchTimeseries(costSeries, 'cost', groupBy.value)
  const retryTokenSeries = () => fetchTimeseries(tokenSeries, 'tokens', 'none')

  /** Preceding-window `cost`/`tokens` totals for the Cost/Tokens KPI tiles' deltas — always `group_by=none`, independent of the chart's own `groupBy`. */
  function fetchPreviousCostAndTokenSeries(): Promise<void[]> {
    const previousQ = previousQuery.value
    if (!previousQ) {
      costPreviousSeries.skip()
      tokenPreviousSeries.skip()
      return Promise.resolve([])
    }
    return Promise.all([
      fetchTimeseries(costPreviousSeries, 'cost', 'none', previousQ),
      fetchTimeseries(tokenPreviousSeries, 'tokens', 'none', previousQ),
    ])
  }

  const retryModelBreakdown = () => fetchBreakdown(modelBreakdown, 'model', 'cost')
  const retryProjectBreakdown = () => fetchBreakdown(projectBreakdown, 'project', 'cost')
  const retryToolBreakdown = () => fetchBreakdown(toolBreakdown, 'tool', 'calls')
  const retryErrorBreakdown = () => fetchBreakdown(errorBreakdown, 'error_type', 'calls')
  const retryDecisions = () => fetchDecisions()
  const retrySummary = () => fetchSummary()

  /**
   * The one entry point for a window/filter change: aborts every resource's
   * previous in-flight call (each resource's own `run` does this the instant
   * it's invoked) and issues exactly one new request per resource — no
   * `Promise.all` that would let one rejection cancel the others' state
   * updates, since each resource already isolates its own error via `run`.
   */
  async function fetchAll(): Promise<void> {
    await Promise.allSettled([
      fetchSummary(),
      fetchPreviousSummary(),
      retryCostSeries(),
      retryTokenSeries(),
      fetchPreviousCostAndTokenSeries(),
      ...KPI_SPARKLINE_METRICS.map((metric) => fetchKpiSeries(metric)),
      retryModelBreakdown(),
      retryProjectBreakdown(),
      retryToolBreakdown(),
      retryErrorBreakdown(),
      fetchDecisions(),
    ])
    initialized.value = true
  }

  function syncRoute(): void {
    void router.replace({
      query: analyticsQuery({ preset: preset.value, customFrom: customFrom.value, customTo: customTo.value, filters: filters.value, groupBy: groupBy.value }),
    })
  }

  /** Switches to a relative preset (`24h`/`7d`/`30d`) — the common case. */
  function setPreset(next: Exclude<AnalyticsPreset, 'custom'>): void {
    preset.value = next
    customFrom.value = null
    customTo.value = null
    syncRoute()
    void fetchAll()
  }

  /** Switches to an explicit custom range (RFC 3339 or relative shorthand, per SPEC §4.1). */
  function setCustomRange(from: string | null, to: string | null): void {
    preset.value = 'custom'
    customFrom.value = from
    customTo.value = to
    syncRoute()
    void fetchAll()
  }

  function setFilters(patch: Partial<AnalyticsFilters>): void {
    filters.value = { ...filters.value, ...patch }
    syncRoute()
    void fetchAll()
  }

  function clearFilters(): void {
    setFilters(emptyAnalyticsFilters())
  }

  /**
   * `group_by` only ever changes what the cost timeseries chart shows — it
   * has no bearing on the KPI tiles, the other timeseries, either breakdown,
   * or the decision matrix, so only `costSeries` is reissued. This is the
   * AC's "`group_by` change refetches only the series".
   */
  function setGroupBy(next: GroupBy): void {
    if (groupBy.value === next) return
    groupBy.value = next
    syncRoute()
    void retryCostSeries()
  }

  /**
   * SPEC §4.1/§4.3's "null vs. zero", driven off the server's own
   * `Summary.not_attributable[]` rather than a hardcoded client-side list of
   * which counters a model filter blanks out — the server is the only
   * authority on which counters it could not attribute for a given request,
   * and duplicating that list here would only drift from it.
   */
  function isNotAttributable(field: string): boolean {
    return summary.data?.not_attributable.includes(field) ?? false
  }

  /**
   * Round-5 UI pass: every KPI tile's period-over-period delta + inline
   * sparkline, computed from real per-bucket data (never fabricated) —
   * `lib/analyticsDelta.ts`'s pure math applied to whichever pair of
   * `Series` resources backs a given metric. `cost`/`tokens` read off the
   * chart panels' own resources (see `KPI_SPARKLINE_METRICS`'s doc
   * comment); the rest read their dedicated {@link kpiSeries} entry.
   */
  const costDelta = computed(() => seriesDelta(costSeries.data, costPreviousSeries.data))
  const costSparkline = computed(() => sparklineValues(costSeries.data))
  const tokenDelta = computed(() => seriesDelta(tokenSeries.data, tokenPreviousSeries.data))
  const tokenSparkline = computed(() => sparklineValues(tokenSeries.data))

  function kpiDelta(metric: KpiSparklineMetric): number | null {
    const entry = kpiSeries[metric]
    return seriesDelta(entry.current.data, entry.previous.data)
  }

  function kpiSparkline(metric: KpiSparklineMetric): number[] | null {
    return sparklineValues(kpiSeries[metric].current.data)
  }

  /**
   * Delta for the four KPIs the timeseries endpoint can't back at all —
   * LOC added/removed (only reported combined per-bucket, SPEC) and
   * active time / reject rate (summary-only derived figures, no
   * `TimeseriesMetric` exists for either) — computed from
   * {@link previousSummary} instead. Still real preceding-window data,
   * just via the summary endpoint rather than timeseries buckets.
   */
  function summaryFieldDelta(field: 'locAdded' | 'locRemoved' | 'activeSeconds' | 'rejectRate'): number | null {
    const current = summary.data
    const previous = previousSummary.data
    if (!current || !previous) return null
    switch (field) {
      case 'locAdded':
        return computeDelta(current.loc?.added ?? null, previous.loc?.added ?? null)
      case 'locRemoved':
        return computeDelta(current.loc?.removed ?? null, previous.loc?.removed ?? null)
      case 'activeSeconds':
        return computeDelta(current.active_seconds, previous.active_seconds)
      case 'rejectRate':
        return computeDelta(current.reject_rate, previous.reject_rate)
    }
  }

  // Initial load: the very first render already has window/filter state
  // (parsed from route.query above), so it fetches immediately.
  void fetchAll()

  return {
    // window/filter state
    preset,
    customFrom,
    customTo,
    filters,
    groupBy,
    hasModelFilter,
    initialized,
    setPreset,
    setCustomRange,
    setFilters,
    clearFilters,
    setGroupBy,
    // resources
    summary,
    previousSummary,
    costSeries,
    tokenSeries,
    modelBreakdown,
    projectBreakdown,
    toolBreakdown,
    errorBreakdown,
    decisions,
    // per-panel retry
    retrySummary,
    retryCostSeries,
    retryTokenSeries,
    retryModelBreakdown,
    retryProjectBreakdown,
    retryToolBreakdown,
    retryErrorBreakdown,
    retryDecisions,
    // derived
    isNotAttributable,
    fetchAll,
    // KPI strip deltas + sparklines (round-5 UI pass)
    costDelta,
    costSparkline,
    tokenDelta,
    tokenSparkline,
    kpiDelta,
    kpiSparkline,
    summaryFieldDelta,
  }
})
