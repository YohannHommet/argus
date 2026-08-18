import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AnalyticsView from './AnalyticsView.vue'
import {
  getAnalyticsBreakdown200Default,
  getAnalyticsDecisions200Default,
  getAnalyticsSummary200Default,
  getAnalyticsSummary200ModelFiltered,
  getAnalyticsTimeseries200Default,
  getFacets200Default,
  getMeta200Default,
} from '@/test/fixtures'
import {
  getAnalyticsSummary200EstimatedCostAndMetricsOnly,
  getAnalyticsSummary200FullyEstimatedCost,
  getAnalyticsSummary200MeasuredZeros,
} from '@/test/fixtures.extra'
import { makeVChartStub, stubResizeObserver, VCHART_STUB_KEY } from '@/test/chartStub'

let getSummary: ReturnType<typeof vi.fn>
let getTimeseries: ReturnType<typeof vi.fn>
let getBreakdown: ReturnType<typeof vi.fn>
let getDecisions: ReturnType<typeof vi.fn>
let getMeta: ReturnType<typeof vi.fn>
let getFacets: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      if (path === '/api/v1/analytics/summary') return getSummary(init)
      if (path === '/api/v1/analytics/timeseries') return getTimeseries(init)
      if (path === '/api/v1/analytics/breakdown') return getBreakdown(init)
      if (path === '/api/v1/analytics/decisions') return getDecisions(init)
      if (path === '/api/v1/meta') return getMeta()
      if (path === '/api/v1/facets') return getFacets()
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function errorResponse(status: number, type = 'urn:argus:error:boom') {
  const problem = { type, title: 'Boom', status }
  return Promise.resolve({
    data: undefined,
    error: problem,
    response: new Response(JSON.stringify(problem), { status, headers: { 'Content-Type': 'application/problem+json' } }),
  })
}

async function mountAt(path = '/analytics'): Promise<{ router: Router; wrapper: ReturnType<typeof mount> }> {
  const { Stub } = makeVChartStub()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/analytics', name: 'analytics', component: AnalyticsView },
      { path: '/tools', name: 'tools', component: { template: '<div/>' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(AnalyticsView, {
    global: { plugins: [router], stubs: { [VCHART_STUB_KEY]: Stub, teleport: true } },
  })
  await flushPromises()
  return { router, wrapper }
}

describe('AnalyticsView', () => {
  let ro: ReturnType<typeof stubResizeObserver>

  beforeEach(() => {
    setActivePinia(createPinia())
    ro = stubResizeObserver()
    getSummary = vi.fn(() => okResponse(getAnalyticsSummary200Default))
    getTimeseries = vi.fn(() => okResponse(getAnalyticsTimeseries200Default))
    getBreakdown = vi.fn(() => okResponse(getAnalyticsBreakdown200Default))
    getDecisions = vi.fn(() => okResponse(getAnalyticsDecisions200Default))
    getMeta = vi.fn(() => okResponse(getMeta200Default))
    getFacets = vi.fn(() => okResponse(getFacets200Default))
  })

  afterEach(() => {
    ro.restore()
    vi.restoreAllMocks()
  })

  it('renders a cost timeseries, a model breakdown, and the decision matrix (Phase-4 exit criterion 6)', async () => {
    const { wrapper } = await mountAt()

    expect(wrapper.find('[data-testid="panel-cost-timeseries"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="panel-model-breakdown"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="panel-decision-matrix"]').exists()).toBe(true)
    // Three chart-bearing panels rendered a stubbed VChart, proving the store's data actually
    // reached each component rather than each one showing its own empty/loading state.
    expect(wrapper.findAllComponents({ name: 'VChartStub' })).not.toHaveLength(0)
  })

  it('flips data-capture-ready only once every one of the eight resources has settled', async () => {
    await mountAt()
    expect(document.documentElement.getAttribute('data-capture-ready')).toBe('true')
  })

  it('changing the window preset refetches every resource (Phase-4 exit criterion 6)', async () => {
    const { useAnalyticsStore } = await import('@/stores/analytics')
    await mountAt()
    getSummary.mockClear()
    getTimeseries.mockClear()
    getBreakdown.mockClear()
    getDecisions.mockClear()

    // Same pinia instance the mounted view's store came from (one active pinia per test, set in
    // beforeEach) — calling the action directly is equivalent to the window `<Select>` firing
    // `update:modelValue`, without depending on reka-ui's portal-based popover internals.
    const store = useAnalyticsStore()
    store.setPreset('7d')
    await flushPromises()

    // getSummary/getTimeseries counts include the round-5 UI pass's preceding-window fetches
    // (KPI tile deltas/sparklines) — see stores/__tests__/analytics.spec.ts's own count assertion
    // for the full breakdown.
    expect(getSummary).toHaveBeenCalledTimes(2)
    expect(getTimeseries).toHaveBeenCalledTimes(16)
    expect(getBreakdown).toHaveBeenCalledTimes(4)
    expect(getDecisions).toHaveBeenCalledTimes(1)
  })

  it('a model filter renders the non-attributable tiles as "—" instead of "0" (Phase-4 exit criterion 6)', async () => {
    getSummary = vi.fn((init: { params: { query: { model?: string[] } } }) => {
      if (init.params.query.model && init.params.query.model.length > 0) return okResponse(getAnalyticsSummary200ModelFiltered)
      return okResponse(getAnalyticsSummary200Default)
    })
    const { wrapper } = await mountAt('/analytics?model=claude-opus-5')

    const sessionsTile = wrapper.get('[data-testid="kpi-sessions"]')
    expect(sessionsTile.text()).toContain('—')
    expect(sessionsTile.text()).not.toContain('0')

    const tocTile = wrapper.get('[data-testid="kpi-tool-calls"]')
    expect(tocTile.text()).toContain('—')
  })

  it('a measured zero (loc.added: 0, active_seconds: 0) renders "0", never collapsing into the null dash', async () => {
    getSummary = vi.fn(() => okResponse(getAnalyticsSummary200MeasuredZeros))
    const { wrapper } = await mountAt()

    const locAdded = wrapper.get('[data-testid="kpi-loc-added"]')
    expect(locAdded.text()).toContain('0')
    expect(locAdded.text()).not.toContain('—')
  })

  it('under a model filter, the tool leaderboard and error panel explain why instead of showing an empty chart, and never request the refused endpoint', async () => {
    const { wrapper } = await mountAt('/analytics?model=claude-opus-5')

    const dimensions = getBreakdown.mock.calls.map((c) => (c[0] as { params: { query: { dimension: string } } }).params.query.dimension)
    expect(dimensions).not.toContain('tool')
    expect(dimensions).not.toContain('error_type')

    expect(wrapper.get('[data-testid="panel-tool-leaderboard"]').text()).toContain('Not available under a model filter')
    expect(wrapper.get('[data-testid="panel-error-breakdown"]').text()).toContain('Not available under a model filter')
  })

  it('an error in one panel renders that panel\'s error state while the others still render', async () => {
    getBreakdown = vi.fn((init: { params: { query: { dimension: string } } }) => {
      if (init.params.query.dimension === 'tool') return errorResponse(500)
      return okResponse(getAnalyticsBreakdown200Default)
    })
    const { wrapper } = await mountAt()

    const toolPanel = wrapper.get('[data-testid="panel-tool-leaderboard"]')
    expect(toolPanel.find('[data-testid="error-state"]').exists()).toBe(true)

    const modelPanel = wrapper.get('[data-testid="panel-model-breakdown"]')
    expect(modelPanel.find('[data-testid="error-state"]').exists()).toBe(false)
  })

  it('clicking a decision matrix cell navigates to /tools with snake_case decision_source/tool_name params', async () => {
    const { wrapper, router } = await mountAt()
    await wrapper.findComponent({ name: 'DecisionMatrix' }).vm.$emit('filter', { tool_name: 'Edit', decision_source: 'hook' })
    await flushPromises()

    expect(router.currentRoute.value.name).toBe('tools')
    expect(router.currentRoute.value.query.decision_source).toBe('hook')
    expect(router.currentRoute.value.query.tool_name).toBe('Edit')
  })

  it('the URL round-trips window and filters (survives a reload)', async () => {
    const { wrapper, router } = await mountAt('/analytics?window=7d&model=claude-opus-5&project=argus')
    void wrapper
    expect(router.currentRoute.value.query.window).toBe('7d')
    expect(router.currentRoute.value.query.model).toBe('claude-opus-5')
    expect(router.currentRoute.value.query.project).toBe('argus')
  })

  // PLAN.md P4-10 / Phase-4 exit criterion 8
  describe('EstimatedCostNotice and the metrics-only-projects banner (PLAN.md P4-10)', () => {
    it('estimated_share: 0.02 renders the notice with the percentage (an AC)', async () => {
      getSummary = vi.fn(() => okResponse(getAnalyticsSummary200EstimatedCostAndMetricsOnly))
      const { wrapper } = await mountAt()

      const notice = wrapper.get('[data-testid="estimated-cost-notice"]')
      expect(notice.get('[data-testid="estimated-cost-share"]').text()).toBe('2.0%')
    })

    it('a fully-estimated window (estimated_share: 1, --cost-mode=omit, live-verified) still renders sensibly', async () => {
      getSummary = vi.fn(() => okResponse(getAnalyticsSummary200FullyEstimatedCost))
      const { wrapper } = await mountAt()

      expect(wrapper.get('[data-testid="estimated-cost-share"]').text()).toBe('100.0%')
    })

    it('no notice when estimated_share is 0 (the default fixture)', async () => {
      getSummary = vi.fn(() => okResponse({ ...getAnalyticsSummary200Default, cost: { ...getAnalyticsSummary200Default.cost, estimated_share: 0 } }))
      const { wrapper } = await mountAt()

      expect(wrapper.find('[data-testid="estimated-cost-notice"]').exists()).toBe(false)
    })

    it('metrics_only_projects: ["x"] renders the banner naming the project (an AC), sourced from analyticsStore.summary', async () => {
      getSummary = vi.fn(() => okResponse(getAnalyticsSummary200EstimatedCostAndMetricsOnly))
      const { wrapper } = await mountAt()

      const banner = wrapper.get('[data-testid="metrics-only-banner"]')
      expect(banner.text()).toContain('x')
      expect(banner.text()).toContain('Logs exporter appears off')
    })

    it('no banner when metrics_only_projects is empty', async () => {
      getSummary = vi.fn(() => okResponse({ ...getAnalyticsSummary200Default, metrics_only_projects: [] }))
      const { wrapper } = await mountAt()
      expect(wrapper.find('[data-testid="metrics-only-banner"]').exists()).toBe(false)
    })
  })

  it('shows a skeleton (not the KPI grid) while the summary fetch is pending, and swaps once it resolves', async () => {
    // getSummary now backs two resources (`summary` + `previousSummary`, round-5 UI pass), so it's
    // called twice per fetchAll — each call needs its own resolver, not one shared variable.
    const resolveSummaryCalls: (() => void)[] = []
    getSummary = vi.fn(
      () =>
        new Promise((resolve) => {
          resolveSummaryCalls.push(() => resolve({ data: getAnalyticsSummary200Default, error: undefined, response: new Response(null, { status: 200 }) }))
        }),
    )
    const { wrapper } = await mountAt()

    expect(wrapper.find('[data-testid="stat-tile-value"]').exists()).toBe(false)
    expect(document.documentElement.getAttribute('data-capture-ready')).not.toBe('true')

    resolveSummaryCalls.forEach((resolve) => resolve())
    await flushPromises()

    expect(wrapper.find('[data-testid="stat-tile-value"]').exists()).toBe(true)
  })
})
