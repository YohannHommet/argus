// PLAN.md P6-04 AC: axe reports zero critical/serious violations on all six top-level views (the
// same six `router/index.ts` registers — see that file's own doc comment). Each view is mounted the
// same way its own dedicated `*View.test.ts` file already does (same fixtures, same API/EventSource
// mocking conventions), then scanned with `assertNoSeriousA11yViolations` (src/test/a11y.ts) — a
// deliberately narrower bar than `vitest-axe`'s default `toHaveNoViolations` (which fails on any
// impact level), matching the AC's own wording.
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AnalyticsView from '@/views/AnalyticsView.vue'
import DataQualityView from '@/views/DataQualityView.vue'
import LiveView from '@/views/LiveView.vue'
import SessionDetailView from '@/views/SessionDetailView.vue'
import SessionListView from '@/views/SessionListView.vue'
import ToolExplorerView from '@/views/ToolExplorerView.vue'
import { CAPTURE_READY_ATTR } from '@/composables/useCaptureReady'
import { resetEventSourceFactory, setEventSourceFactory } from '@/lib/sse'
import type { EventSourceLike } from '@/lib/sse'
import { assertNoSeriousA11yViolations } from '@/test/a11y'
import { makeVChartStub, stubResizeObserver, VCHART_STUB_KEY } from '@/test/chartStub'
import {
  getAnalyticsBreakdown200Default,
  getAnalyticsDecisions200Default,
  getAnalyticsSummary200Default,
  getAnalyticsTimeseries200Default,
  getFacets200Default,
  getMeta200Default,
  getQualityHookLatency200Default,
  getQualityUnknownKinds200Default,
  getSession200Default,
  getSessionSubagents200Default,
  getSessionTimeline200Default,
  listSessionToolCalls200Default,
  listSessionTurns200Default,
  listSessions200Default,
  listToolCalls200Default,
} from '@/test/fixtures'

const SESSION_ID = getSession200Default.id

/** Same structural fake `LiveView.test.ts`/`SessionDetailView.test.ts` already use — a firehose/session subscription would otherwise construct a real `EventSource`, unavailable in jsdom. */
class FakeEventSource implements EventSourceLike {
  static readonly CONNECTING = 0
  static readonly OPEN = 1

  readyState = FakeEventSource.CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  private readonly listeners = new Map<string, ((ev: MessageEvent) => void)[]>()

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? []
    list.push(listener)
    this.listeners.set(type, list)
  }

  close(): void {}

  open(): void {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.(new Event('open'))
  }

  emit(type: string, data: unknown): void {
    const ev = new MessageEvent(type, { data: JSON.stringify(data) })
    for (const listener of this.listeners.get(type) ?? []) listener(ev)
  }
}

let instances: FakeEventSource[] = []

let getSessions: ReturnType<typeof vi.fn>
let getMeta: ReturnType<typeof vi.fn>
let getFacets: ReturnType<typeof vi.fn>
let getSessionDetail: ReturnType<typeof vi.fn>
let getTurns: ReturnType<typeof vi.fn>
let getTimeline: ReturnType<typeof vi.fn>
let getSubagents: ReturnType<typeof vi.fn>
let getSessionToolCalls: ReturnType<typeof vi.fn>
let getToolCalls: ReturnType<typeof vi.fn>
let getSummary: ReturnType<typeof vi.fn>
let getTimeseries: ReturnType<typeof vi.fn>
let getBreakdown: ReturnType<typeof vi.fn>
let getDecisions: ReturnType<typeof vi.fn>
let getUnknownKinds: ReturnType<typeof vi.fn>
let getHookLatency: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      switch (path) {
        case '/api/v1/sessions':
          return getSessions(init)
        case '/api/v1/meta':
          return getMeta()
        case '/api/v1/facets':
          return getFacets()
        case '/api/v1/sessions/{id}':
          return getSessionDetail()
        case '/api/v1/sessions/{id}/turns':
          return getTurns()
        case '/api/v1/sessions/{id}/timeline':
          return getTimeline()
        case '/api/v1/sessions/{id}/subagents':
          return getSubagents()
        case '/api/v1/sessions/{id}/tool-calls':
          return getSessionToolCalls()
        case '/api/v1/tool-calls':
          return getToolCalls(init)
        case '/api/v1/analytics/summary':
          return getSummary(init)
        case '/api/v1/analytics/timeseries':
          return getTimeseries(init)
        case '/api/v1/analytics/breakdown':
          return getBreakdown(init)
        case '/api/v1/analytics/decisions':
          return getDecisions(init)
        case '/api/v1/quality/unknown-kinds':
          return getUnknownKinds()
        case '/api/v1/quality/hook-latency':
          return getHookLatency()
        default:
          throw new Error(`unexpected path ${path}`)
      }
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function page<T>(data: T[]) {
  return { data, page: { next_cursor: null, has_more: false } }
}

async function mountViewAt(component: object, path: string, props: Record<string, unknown> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/sessions', name: 'sessions', component: SessionListView },
      { path: '/sessions/:id', name: 'session-detail', component: SessionDetailView, props: true },
      { path: '/tools', name: 'tools', component: ToolExplorerView },
      { path: '/analytics', name: 'analytics', component: AnalyticsView },
      { path: '/live', name: 'live', component: LiveView },
      { path: '/data-quality', name: 'data-quality', component: DataQualityView },
    ],
  })
  await router.push(path)
  await router.isReady()
  const { Stub } = makeVChartStub()
  const wrapper = mount(component, {
    props,
    global: { plugins: [router], stubs: { [VCHART_STUB_KEY]: Stub, teleport: true } },
  })
  await flushPromises()
  return wrapper
}

describe('six-view accessibility (PLAN.md P6-04 AC)', () => {
  // A full-view axe scan is legitimately slow (~2-4s each); vitest's 5s default
  // times out one view intermittently when the whole suite's parallel forks
  // share the CPU (worse under --coverage instrumentation). Give axe real
  // headroom so this spec is deterministic, not flaky — the assertion is
  // unchanged, only the clock it runs against.
  vi.setConfig({ testTimeout: 30_000 })

  let ro: ReturnType<typeof stubResizeObserver>

  beforeEach(() => {
    setActivePinia(createPinia())
    ro = stubResizeObserver()
    instances = []
    setEventSourceFactory((): EventSourceLike => {
      const instance = new FakeEventSource()
      instances.push(instance)
      return instance
    })

    getSessions = vi.fn(() => okResponse(listSessions200Default))
    getMeta = vi.fn(() => okResponse(getMeta200Default))
    getFacets = vi.fn(() => okResponse(getFacets200Default))
    getSessionDetail = vi.fn(() => okResponse(getSession200Default))
    getTurns = vi.fn(() => okResponse(listSessionTurns200Default))
    getTimeline = vi.fn(() => okResponse(getSessionTimeline200Default))
    getSubagents = vi.fn(() => okResponse(getSessionSubagents200Default))
    getSessionToolCalls = vi.fn(() => okResponse(page(listSessionToolCalls200Default.data)))
    getToolCalls = vi.fn(() => okResponse(page(listToolCalls200Default.data)))
    getSummary = vi.fn(() => okResponse(getAnalyticsSummary200Default))
    getTimeseries = vi.fn(() => okResponse(getAnalyticsTimeseries200Default))
    getBreakdown = vi.fn(() => okResponse(getAnalyticsBreakdown200Default))
    getDecisions = vi.fn(() => okResponse(getAnalyticsDecisions200Default))
    getUnknownKinds = vi.fn(() => okResponse(getQualityUnknownKinds200Default))
    getHookLatency = vi.fn(() => okResponse(getQualityHookLatency200Default))
  })

  afterEach(() => {
    ro.restore()
    resetEventSourceFactory()
    document.documentElement.removeAttribute(CAPTURE_READY_ATTR)
    vi.restoreAllMocks()
  })

  it('SessionListView (/sessions) has zero critical/serious axe violations', async () => {
    const wrapper = await mountViewAt(SessionListView, '/sessions')
    expect(wrapper.find('[data-testid="session-table"]').exists()).toBe(true)
    await assertNoSeriousA11yViolations(wrapper.element)
  })

  it('SessionDetailView (/sessions/:id) has zero critical/serious axe violations', async () => {
    const wrapper = await mountViewAt(SessionDetailView, `/sessions/${SESSION_ID}`, { id: SESSION_ID })
    expect(wrapper.find('[data-testid="timeline"]').exists()).toBe(true)
    await assertNoSeriousA11yViolations(wrapper.element)
  })

  it('ToolExplorerView (/tools) has zero critical/serious axe violations', async () => {
    const wrapper = await mountViewAt(ToolExplorerView, '/tools')
    expect(wrapper.find('[data-testid="tool-call-table"]').exists()).toBe(true)
    await assertNoSeriousA11yViolations(wrapper.element)
  })

  it('AnalyticsView (/analytics) has zero critical/serious axe violations', async () => {
    const wrapper = await mountViewAt(AnalyticsView, '/analytics')
    expect(wrapper.find('[data-testid="panel-cost-timeseries"]').exists()).toBe(true)
    await assertNoSeriousA11yViolations(wrapper.element)
  })

  it('LiveView (/live) has zero critical/serious axe violations', async () => {
    const wrapper = await mountViewAt(LiveView, '/live')
    // A connected stream with at least one frame, so the feed/active-session-cards render their real
    // (non-empty) markup too, not just the empty-state branch.
    instances[0]!.open()
    instances[0]!.emit('session', listSessions200Default.data[0]!)
    instances[0]!.emit('stats', { events_per_sec: 1.2, active_sessions: 1, queue_depth: 0, ingest_lag_ms: 10, dropped_total: 0 })
    await flushPromises()

    expect(wrapper.find('[data-testid="live-feed"]').exists()).toBe(true)
    await assertNoSeriousA11yViolations(wrapper.element)
  })

  it('DataQualityView (/data-quality) has zero critical/serious axe violations', async () => {
    const wrapper = await mountViewAt(DataQualityView, '/data-quality')
    expect(wrapper.find('[data-testid="quality-tiles"]').exists()).toBe(true)
    await assertNoSeriousA11yViolations(wrapper.element)
  })
})
