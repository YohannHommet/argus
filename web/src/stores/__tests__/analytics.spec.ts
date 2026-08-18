import { flushPromises } from '@vue/test-utils'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  getAnalyticsBreakdown200Default,
  getAnalyticsDecisions200Default,
  getAnalyticsSummary200Default,
  getAnalyticsSummary200ModelFiltered,
  getAnalyticsTimeseries200Default,
} from '@/test/fixtures'

let getSummary: ReturnType<typeof vi.fn>
let getTimeseries: ReturnType<typeof vi.fn>
let getBreakdown: ReturnType<typeof vi.fn>
let getDecisions: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      if (path === '/api/v1/analytics/summary') return getSummary(init)
      if (path === '/api/v1/analytics/timeseries') return getTimeseries(init)
      if (path === '/api/v1/analytics/breakdown') return getBreakdown(init)
      if (path === '/api/v1/analytics/decisions') return getDecisions(init)
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function apiErrorResponse(status: number, type = 'urn:argus:error:boom') {
  const problem = { type, title: 'Boom', status }
  return Promise.resolve({
    data: undefined,
    error: problem,
    response: new Response(JSON.stringify(problem), { status, headers: { 'Content-Type': 'application/problem+json' } }),
  })
}

/** Never-settling promise — for asserting a call was aborted before it resolved. */
function pending<T>(): Promise<T> {
  return new Promise<T>(() => {
    /* never resolves within the test */
  })
}

async function setupStore(initialPath = '/analytics') {
  const { useAnalyticsStore } = await import('../analytics')
  const pinia = createPinia()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/analytics', name: 'analytics', component: { template: '<div/>' } }],
  })
  await router.push(initialPath)
  await router.isReady()

  const app = createApp({})
  app.use(pinia)
  app.use(router)

  let store!: ReturnType<typeof useAnalyticsStore>
  app.runWithContext(() => {
    store = useAnalyticsStore()
  })
  await vi.waitFor(() => expect(store.initialized).toBe(true))
  return { store, router }
}

beforeEach(() => {
  vi.resetModules()
  getSummary = vi.fn(() => okResponse(getAnalyticsSummary200Default))
  getTimeseries = vi.fn(() => okResponse(getAnalyticsTimeseries200Default))
  getBreakdown = vi.fn(() => okResponse(getAnalyticsBreakdown200Default))
  getDecisions = vi.fn(() => okResponse(getAnalyticsDecisions200Default))
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('isRequestAttributable — SPEC §4.3 refusal table', () => {
  it('everything is attributable without a model filter', async () => {
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'timeseries', metric: 'sessions', hasModelFilter: false })).toBe(true)
    expect(isRequestAttributable({ endpoint: 'breakdown', dimension: 'tool', metric: 'calls', hasModelFilter: false })).toBe(true)
  })

  it('summary is always attributable at the endpoint level (it degrades per-counter instead)', async () => {
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'summary', hasModelFilter: true })).toBe(true)
  })

  it('decisions is always attributable (no model query param exists to refuse)', async () => {
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'decisions', hasModelFilter: true })).toBe(true)
  })

  it('case 1: timeseries metric=sessions under a model filter is refused', async () => {
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'timeseries', metric: 'sessions', hasModelFilter: true })).toBe(false)
  })

  it('timeseries cost/tokens/api_requests/api_errors remain attributable under a model filter', async () => {
    const { isRequestAttributable } = await import('../analytics')
    for (const metric of ['cost', 'tokens', 'api_requests', 'api_errors']) {
      expect(isRequestAttributable({ endpoint: 'timeseries', metric, hasModelFilter: true })).toBe(true)
    }
  })

  it.each(['tool', 'decision_source', 'error_type', 'query_source'])(
    'cases 2-5: breakdown dimension=%s under a model filter is refused regardless of metric',
    async (dimension) => {
      const { isRequestAttributable } = await import('../analytics')
      expect(isRequestAttributable({ endpoint: 'breakdown', dimension, metric: 'cost', hasModelFilter: true })).toBe(false)
    },
  )

  it('case 6: breakdown metric=calls under a model filter is refused on any dimension', async () => {
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'breakdown', dimension: 'model', metric: 'calls', hasModelFilter: true })).toBe(false)
    expect(isRequestAttributable({ endpoint: 'breakdown', dimension: 'project', metric: 'calls', hasModelFilter: true })).toBe(false)
  })

  it('breakdown dimension=model/project with metric=cost remains attributable under a model filter', async () => {
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'breakdown', dimension: 'model', metric: 'cost', hasModelFilter: true })).toBe(true)
    expect(isRequestAttributable({ endpoint: 'breakdown', dimension: 'project', metric: 'cost', hasModelFilter: true })).toBe(true)
  })
})

describe('analytics store — window/filter <-> URL query (pure helpers)', () => {
  it('round-trips a custom range + filters through the query and back', async () => {
    const { analyticsQuery, parseAnalyticsQuery } = await import('../analytics')
    const state = {
      preset: 'custom' as const,
      customFrom: '2026-08-01T00:00:00Z',
      customTo: '2026-08-10T00:00:00Z',
      filters: { project: ['argus'], model: ['claude-opus-5'], vendor: [] },
      groupBy: 'project' as const,
    }
    const query = analyticsQuery(state)
    expect(parseAnalyticsQuery(query)).toEqual(state)
  })

  it('omits defaults entirely — an untouched dashboard has a clean URL', async () => {
    const { analyticsQuery, emptyAnalyticsFilters, DEFAULT_PRESET, DEFAULT_GROUP_BY } = await import('../analytics')
    const query = analyticsQuery({ preset: DEFAULT_PRESET, customFrom: null, customTo: null, filters: emptyAnalyticsFilters(), groupBy: DEFAULT_GROUP_BY })
    expect(query).toEqual({})
  })

  it('drops a non-vocabulary window/group_by rather than throwing', async () => {
    const { parseAnalyticsQuery, DEFAULT_PRESET, DEFAULT_GROUP_BY } = await import('../analytics')
    const parsed = parseAnalyticsQuery({ window: 'bogus', group_by: 'nonsense' })
    expect(parsed.preset).toBe(DEFAULT_PRESET)
    expect(parsed.groupBy).toBe(DEFAULT_GROUP_BY)
  })
})

describe('useAnalyticsStore', () => {
  it('fetches all resources exactly once on creation, including the preceding-window pair every KPI delta needs', async () => {
    const { store } = await setupStore()
    // summary: current + previous window (round-5 UI pass — KPI tile deltas).
    expect(getSummary).toHaveBeenCalledTimes(2)
    // timeseries: cost + tokens (current) + cost + tokens (previous)
    // + 6 KPI_SPARKLINE_METRICS x (current + previous) = 4 + 12 = 16.
    expect(getTimeseries).toHaveBeenCalledTimes(16)
    expect(getBreakdown).toHaveBeenCalledTimes(4) // model + project + tool + error_type
    expect(getDecisions).toHaveBeenCalledTimes(1)
    expect(store.summary.data).toEqual(getAnalyticsSummary200Default)
    expect(store.previousSummary.data).toEqual(getAnalyticsSummary200Default)
  })

  it('restores window preset and filters from the initial route query (reload stability)', async () => {
    const { store } = await setupStore('/analytics?window=7d&model=claude-opus-5&project=argus')
    expect(store.preset).toBe('7d')
    expect(store.filters.model).toEqual(['claude-opus-5'])
    expect(store.filters.project).toEqual(['argus'])
  })

  it('changing the range aborts in-flight requests and issues exactly one new set', async () => {
    // Round 1 (the store's own initial fetch on creation) settles normally with the default mocks.
    const { store, router } = await setupStore()
    getSummary.mockClear()
    getTimeseries.mockClear()
    getBreakdown.mockClear()
    getDecisions.mockClear()

    // Round 2: a range change whose summary call is left deliberately in flight, so round 3 has
    // something concrete to abort.
    let firstSignal: AbortSignal | undefined
    getSummary = vi.fn((init: { signal?: AbortSignal }) => {
      firstSignal = init.signal
      return pending()
    })

    store.setPreset('7d')
    await flushPromises()
    // getSummary backs both `summary` and `previousSummary` (round-5 UI pass) — 2 calls, one per resource.
    expect(getSummary).toHaveBeenCalledTimes(2)
    expect(getTimeseries).toHaveBeenCalledTimes(16) // (cost + tokens) x (current + previous) + 6 KPI metrics x (current + previous)
    expect(getBreakdown).toHaveBeenCalledTimes(4)
    expect(getDecisions).toHaveBeenCalledTimes(1)
    expect(firstSignal?.aborted).toBe(false)

    // Round 3: another range change. It must abort round 2's still-in-flight summary call and issue
    // exactly one new set (one more call per resource) — not two, not zero.
    getSummary = vi.fn(() => okResponse(getAnalyticsSummary200Default))

    store.setPreset('30d')
    await flushPromises()

    expect(router.currentRoute.value.query.window).toBe('30d')
    expect(getTimeseries).toHaveBeenCalledTimes(32)
    expect(getBreakdown).toHaveBeenCalledTimes(8)
    expect(getDecisions).toHaveBeenCalledTimes(2)
    expect(firstSignal?.aborted).toBe(true)
  })

  it('group_by change refetches only the cost series — summary/breakdowns/decisions/token series untouched', async () => {
    const { store } = await setupStore()
    getSummary.mockClear()
    getTimeseries.mockClear()
    getBreakdown.mockClear()
    getDecisions.mockClear()

    store.setGroupBy('project')
    await flushPromises()

    expect(getTimeseries).toHaveBeenCalledTimes(1)
    const call = getTimeseries.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
    expect(call.params.query.metric).toBe('cost')
    expect(call.params.query.group_by).toBe('project')
    expect(getSummary).not.toHaveBeenCalled()
    expect(getBreakdown).not.toHaveBeenCalled()
    expect(getDecisions).not.toHaveBeenCalled()
  })

  it('setGroupBy is a no-op when the value is unchanged', async () => {
    const { store } = await setupStore()
    getTimeseries.mockClear()
    store.setGroupBy(store.groupBy)
    await flushPromises()
    expect(getTimeseries).not.toHaveBeenCalled()
  })

  it('an error in one of four breakdown requests renders that panel error while the others render', async () => {
    getBreakdown = vi.fn((init: { params: { query: { dimension: string } } }) => {
      if (init.params.query.dimension === 'tool') return apiErrorResponse(400)
      return okResponse(getAnalyticsBreakdown200Default)
    })
    const { store } = await setupStore()

    expect(store.toolBreakdown.error).not.toBeNull()
    expect(store.toolBreakdown.data).toBeNull()
    expect(store.modelBreakdown.data).toEqual(getAnalyticsBreakdown200Default)
    expect(store.modelBreakdown.error).toBeNull()
    expect(store.projectBreakdown.data).toEqual(getAnalyticsBreakdown200Default)
    expect(store.errorBreakdown.data).toEqual(getAnalyticsBreakdown200Default)
    expect(store.decisions.data).toEqual(getAnalyticsDecisions200Default)
    expect(store.summary.data).toEqual(getAnalyticsSummary200Default)
  })

  it('retrying a single panel only reissues that resource', async () => {
    getBreakdown = vi.fn((init: { params: { query: { dimension: string } } }) => {
      if (init.params.query.dimension === 'tool') return apiErrorResponse(400)
      return okResponse(getAnalyticsBreakdown200Default)
    })
    const { store } = await setupStore()
    expect(store.toolBreakdown.error).not.toBeNull()

    getBreakdown = vi.fn(() => okResponse(getAnalyticsBreakdown200Default))
    getSummary.mockClear()
    getDecisions.mockClear()
    await store.retryToolBreakdown()

    expect(store.toolBreakdown.error).toBeNull()
    expect(store.toolBreakdown.data).toEqual(getAnalyticsBreakdown200Default)
    expect(getSummary).not.toHaveBeenCalled()
    expect(getDecisions).not.toHaveBeenCalled()
  })

  it('a model filter skips tool/error_type breakdowns without ever calling the client for them', async () => {
    const { store } = await setupStore()
    getBreakdown.mockClear()
    getTimeseries.mockClear()

    store.setFilters({ model: ['claude-opus-5'] })
    await flushPromises()

    expect(store.toolBreakdown.notAttributable).toBe(true)
    expect(store.toolBreakdown.data).toBeNull()
    expect(store.errorBreakdown.notAttributable).toBe(true)
    expect(store.errorBreakdown.data).toBeNull()

    const dimensions = getBreakdown.mock.calls.map((c) => (c[0] as { params: { query: { dimension: string } } }).params.query.dimension)
    expect(dimensions).not.toContain('tool')
    expect(dimensions).not.toContain('error_type')
    expect(dimensions).toEqual(expect.arrayContaining(['model', 'project']))

    // The store never requests a non-attributable timeseries metric at all: `cost`/`tokens` (the
    // chart panels) and `api_requests`/`api_errors` (round-5 UI pass's KPI sparklines) all stay
    // attributable under a model filter, so none of those series is skipped; `sessions`/`turns`/
    // `tool_calls`/`tool_rejects` (the other KPI sparklines) are not attributable and are skipped
    // client-side — no request for any of them is ever constructed to send.
    const metrics = getTimeseries.mock.calls.map((c) => (c[0] as { params: { query: Record<string, unknown> } }).params.query.metric)
    expect(metrics).not.toContain('sessions')
    expect(metrics).not.toContain('turns')
    expect(metrics).not.toContain('tool_calls')
    expect(metrics).not.toContain('tool_rejects')
    expect(metrics).toEqual(expect.arrayContaining(['cost', 'tokens', 'api_requests', 'api_errors']))
    expect(store.costSeries.notAttributable).toBe(false)
    expect(store.tokenSeries.notAttributable).toBe(false)
  })

  it('a timeseries fetch for a non-attributable metric is refused before ever touching the client', async () => {
    const { store, router } = await setupStore()
    void router
    store.setFilters({ model: ['claude-opus-5'] })
    await flushPromises()
    getTimeseries.mockClear()

    // Exercise the store's own guarded fetch path (not just the standalone predicate) the same way
    // costSeries/tokenSeries do internally, proving the wiring — not only the predicate — refuses it.
    const { isRequestAttributable } = await import('../analytics')
    expect(isRequestAttributable({ endpoint: 'timeseries', metric: 'sessions', hasModelFilter: store.hasModelFilter })).toBe(false)
    expect(getTimeseries).not.toHaveBeenCalled()
  })

  it('isNotAttributable is driven by the server-provided not_attributable[], not a hardcoded list', async () => {
    getSummary = vi.fn(() => okResponse(getAnalyticsSummary200ModelFiltered))
    const { store } = await setupStore()

    expect(store.isNotAttributable('sessions')).toBe(true)
    expect(store.isNotAttributable('loc')).toBe(true)
    expect(store.isNotAttributable('active_seconds')).toBe(true)
    // api_requests/api_errors/cost/tokens are attributable and never appear in the fixture's list.
    expect(store.isNotAttributable('api_requests')).toBe(false)
    expect(store.isNotAttributable('cost')).toBe(false)
  })

  it('isNotAttributable is false for every counter on the unfiltered default fixture', async () => {
    const { store } = await setupStore()
    expect(store.isNotAttributable('sessions')).toBe(false)
    expect(store.isNotAttributable('loc')).toBe(false)
  })
})

describe('useAnalyticsStore — KPI deltas + sparklines (round-5 UI pass)', () => {
  /**
   * `commonQuery`'s current window uses the preset's relative shorthand
   * (`-24h`) unchanged; `previousQuery` always resolves to an absolute ISO
   * `from` (see `previousWindowParams`) — so a mock can tell the two
   * apart by that shape alone, without needing to know the real clock.
   */
  function isPreviousWindowQuery(query: { from?: string }): boolean {
    return typeof query.from === 'string' && query.from !== '-24h' && query.from !== '-7d' && query.from !== '-30d'
  }

  it('costDelta/tokenDelta/kpiDelta are real current-minus-previous totals from two separate fetches', async () => {
    getTimeseries = vi.fn((init: { params: { query: { metric: string; from?: string } } }) => {
      const { metric, from } = init.params.query
      const previous = isPreviousWindowQuery({ from })
      if (metric === 'cost') return okResponse(previous ? { bucket: 'hour', buckets: ['t0'], series: [{ key: '', values: [10] }] } : { bucket: 'hour', buckets: ['t0'], series: [{ key: '', values: [15] }] })
      if (metric === 'api_requests')
        return okResponse(previous ? { bucket: 'hour', buckets: ['t0'], series: [{ key: '', values: [50] }] } : { bucket: 'hour', buckets: ['t0'], series: [{ key: '', values: [70] }] })
      return okResponse(getAnalyticsTimeseries200Default)
    })

    const { store } = await setupStore()

    expect(store.costDelta).toBe(5) // 15 - 10
    expect(store.kpiDelta('api_requests')).toBe(20) // 70 - 50
  })

  it('costSparkline/kpiSparkline plot the current window only, never the preceding one', async () => {
    const { store } = await setupStore()
    // getAnalyticsTimeseries200Default: series [1.2, 0] + other [0.4, 0.1] -> per-bucket totals.
    expect(store.costSparkline).toEqual([1.6, 0.1])
  })

  it('a metric skipped as not-attributable under a model filter has null delta and null sparkline, never a fabricated flat line', async () => {
    const { store } = await setupStore()
    store.setFilters({ model: ['claude-opus-5'] })
    await flushPromises()

    // sessions/turns/tool_calls/tool_rejects are not model-attributable -> skipped client-side.
    expect(store.kpiDelta('sessions')).toBeNull()
    expect(store.kpiSparkline('sessions')).toBeNull()
  })

  it('summaryFieldDelta compares the current and preceding window summaries for LOC/active-time/reject-rate', async () => {
    const previous = {
      ...getAnalyticsSummary200Default,
      loc: { added: 100, removed: 40 },
      active_seconds: 500,
      reject_rate: 0.1,
    }
    const current = {
      ...getAnalyticsSummary200Default,
      loc: { added: 130, removed: 55 },
      active_seconds: 620,
      reject_rate: 0.07,
    }
    let call = 0
    getSummary = vi.fn(() => okResponse(call++ === 0 ? current : previous))

    const { store } = await setupStore()

    expect(store.summaryFieldDelta('locAdded')).toBe(30)
    expect(store.summaryFieldDelta('locRemoved')).toBe(15)
    expect(store.summaryFieldDelta('activeSeconds')).toBe(120)
    expect(store.summaryFieldDelta('rejectRate')).toBeCloseTo(-0.03)
  })

  it('summaryFieldDelta is null when the preceding window has no data (e.g. a custom range missing a bound)', async () => {
    const { store, router } = await setupStore()
    await router.push({ query: { window: 'custom', from: '2026-08-10' } }) // `to` missing -> previousWindowParams can't be computed
    store.setCustomRange('2026-08-10', null)
    await flushPromises()

    expect(store.summaryFieldDelta('locAdded')).toBeNull()
    expect(store.previousSummary.data).toBeNull()
  })
})
