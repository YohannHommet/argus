import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SessionDetailView from './SessionDetailView.vue'
import Timeline from '@/components/timeline/Timeline.vue'
import { getSession200Default } from '@/test/fixtures'
import { partialSessionDetail, rawEventsExpiredSessionDetail } from '@/test/fixtures.extra'
import { useSessionDetailStore } from '@/stores/sessionDetail'

let getSession: ReturnType<typeof vi.fn>
let getSubagents: ReturnType<typeof vi.fn>
let getToolCalls: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string) => {
      if (path === '/api/v1/sessions/{id}') return getSession()
      if (path === '/api/v1/sessions/{id}/subagents') return getSubagents()
      if (path === '/api/v1/sessions/{id}/tool-calls') return getToolCalls()
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

async function mountAt(path: string): Promise<{ router: Router; wrapper: ReturnType<typeof mount> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/sessions/:id', name: 'session-detail', component: SessionDetailView, props: true }],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(SessionDetailView, {
    props: { id: router.currentRoute.value.params.id as string },
    global: { plugins: [router] },
  })
  await flushPromises()
  return { router, wrapper }
}

describe('SessionDetailView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSession = vi.fn(() => okResponse(getSession200Default))
    getSubagents = vi.fn(() => okResponse({ data: [], cost_attribution: null }))
    getToolCalls = vi.fn(() => okResponse({ data: [], page: { next_cursor: null, has_more: false } }))
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('loads the session and renders the KPI strip with the matching cost (Phase-4 exit criterion 2)', async () => {
    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    expect(wrapper.find('[data-testid="session-kpi-strip"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="kpi-cost"]').text()).toBe('$4.27')
  })

  it('defaults to the timeline tab and does not eagerly fetch subagents/tool-calls', async () => {
    await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    expect(getSubagents).not.toHaveBeenCalled()
    expect(getToolCalls).not.toHaveBeenCalled()
  })

  it('a partial:true session renders the partial badge and no NaN/Invalid Date anywhere', async () => {
    getSession = vi.fn(() => okResponse(partialSessionDetail))
    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    expect(wrapper.find('[data-testid="partial-badge"]').exists()).toBe(true)
    const text = wrapper.text()
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('Invalid Date')
  })

  it('raw_events_expired renders the notice instead of the Timeline', async () => {
    getSession = vi.fn(() => okResponse(rawEventsExpiredSessionDetail))
    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    expect(wrapper.find('[data-testid="raw-events-expired-notice"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timeline"]').exists()).toBe(false)
  })

  it('a session with raw_events_expired: false renders the real Timeline, not the notice', async () => {
    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    expect(wrapper.find('[data-testid="timeline"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="raw-events-expired-notice"]').exists()).toBe(false)
  })

  it('tab state survives reload: mounting fresh at ?tab=subagents opens the subagents tab and lazily fetches it', async () => {
    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001?tab=subagents')

    expect(wrapper.find('[data-testid="subagent-tree"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="cost-attribution-card"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timeline"]').exists()).toBe(false)
    expect(getSubagents).toHaveBeenCalledTimes(1)
  })

  // D-30 (docs/review/phase-4-gauntlet.md): CostAttributionCard's estimated
  // marker is driven by props the *view* has to pass down from the session
  // projection — `by_query_source` is reported-cost-only (SPEC §2.1), so the
  // card cannot derive them from its own `data`. This asserts the wiring, not
  // the card: with the props unwired the card silently falls back to its
  // `estimatedUsd: 0` default and renders "$0.00 of $0.00" again, which is
  // exactly the defect D-30 named and exactly what a component-only test
  // cannot catch.
  it('an all-estimated session propagates cost.estimated_* into the cost attribution card', async () => {
    getSession = vi.fn(() =>
      okResponse({
        ...getSession200Default,
        cost: {
          usd: 1.5,
          reported_usd: 0,
          estimated_usd: 1.5,
          estimated_share: 1,
          by_query_source: {},
          dominant_query_source: '',
          other_query_source_usd: 0,
        },
      }),
    )
    getSubagents = vi.fn(() =>
      okResponse({
        data: [],
        cost_attribution: { by_query_source: {}, dominant_query_source: '', other_query_source_usd: 0 },
      }),
    )

    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001?tab=subagents')

    expect(wrapper.find('[data-testid="cost-attribution-estimated-notice"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="cost-attribution-estimated-text"]').text()).toContain('$1.50')
    // The "$0.00 of $0.00" reported-split summary must not also be rendered.
    expect(wrapper.find('[data-testid="cost-attribution-other-share"]').exists()).toBe(false)
  })

  it('switching to the tools tab updates the URL query and lazily fetches tool calls once', async () => {
    const { router, wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    const tabTrigger = (label: string) =>
      wrapper.findAll('[role="tab"]').find((el) => el.text() === label)!

    await tabTrigger('Tools').trigger('mousedown', { button: 0 })
    await flushPromises()

    expect(router.currentRoute.value.query.tab).toBe('tools')
    expect(wrapper.find('[data-testid="tool-call-table"]').exists()).toBe(true)
    expect(getToolCalls).toHaveBeenCalledTimes(1)

    // Switching away and back does not refetch (lazy-once, not lazy-every-time).
    await tabTrigger('Timeline').trigger('mousedown', { button: 0 })
    await tabTrigger('Tools').trigger('mousedown', { button: 0 })
    await flushPromises()
    expect(getToolCalls).toHaveBeenCalledTimes(1)
  })

  it('surfaces decision_summary.exact_share', async () => {
    const { wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001')

    expect(wrapper.get('[data-testid="decision-confidence"]').text()).toContain('100.0%')
  })

  it('renders ErrorState (not a crash) on a 404', async () => {
    getSession = vi.fn(() =>
      Promise.resolve({
        data: undefined,
        error: { type: 'urn:argus:error:not-found', title: 'Not Found', status: 404 },
        response: new Response(null, { status: 404 }),
      }),
    )
    const { wrapper } = await mountAt('/sessions/does-not-exist')

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
  })

  // Round-6 critic gap: clicking a Subagents node routes to
  // `?tab=timeline&agent_id=…` (SubagentTree.vue), but there was no way to
  // clear that filter short of hand-editing the URL. This view owns the
  // route, so clearing it is this view's job — Timeline.vue only emits.
  it('clears ?agent_id from the URL when Timeline emits clear-agent-filter, which the existing route watcher turns into a cleared store filter', async () => {
    const { router, wrapper } = await mountAt('/sessions/3f7a3b1e-0000-0000-0000-000000000001?tab=timeline&agent_id=agent-107d2cba')
    const store = useSessionDetailStore()

    expect(router.currentRoute.value.query.agent_id).toBe('agent-107d2cba')
    expect(store.agentId).toBe('agent-107d2cba')

    await wrapper.findComponent(Timeline).vm.$emit('clear-agent-filter')
    await flushPromises()

    expect(router.currentRoute.value.query.agent_id).toBeUndefined()
    expect(store.agentId).toBeNull()
  })
})
