import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getSession200Default, getSessionTimeline200Default, listSessionTurns200Default } from '@/test/fixtures'
import { makeTimelineEvent } from '@/test/fixtures.extra'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import Timeline from './Timeline.vue'

// Same pattern as stores/__tests__/sessionDetail.spec.ts: mock the API
// client, exercise the real store, so Timeline is tested against actual
// store state transitions rather than a hand-poked shadowRef.
let getSession: ReturnType<typeof vi.fn>
let getTimeline: ReturnType<typeof vi.fn>
let getTurns: ReturnType<typeof vi.fn>
let getEvent: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string) => {
      if (path === '/api/v1/sessions/{id}') return getSession()
      if (path === '/api/v1/sessions/{id}/timeline') return getTimeline()
      if (path === '/api/v1/sessions/{id}/turns') return getTurns()
      if (path === '/api/v1/events/{ref}') return getEvent()
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function errorResponse(status: number) {
  const problem = { type: 'urn:argus:error:boom', title: 'Boom', status }
  return Promise.resolve({
    data: undefined,
    error: problem,
    response: new Response(JSON.stringify(problem), { status, headers: { 'Content-Type': 'application/problem+json' } }),
  })
}

function timelinePage(events: ReturnType<typeof makeTimelineEvent>[], hasMore = false) {
  return okResponse({ data: events, page: { next_cursor: hasMore ? 'cursor-2' : null, has_more: hasMore } })
}

/**
 * Mirrors `SessionDetailView.vue`'s real sequencing: `loadSession(id)` first
 * (which is what actually creates the store's per-session entry), *then*
 * mount the tab. Setting `store.currentId` directly and skipping
 * `loadSession` would let Timeline's first render observe "no entry yet"
 * and permanently cache that shape in its `timelineItems` computed — a
 * store-level gotcha (the computed's only tracked dependency becomes
 * `currentId` when the entry doesn't exist yet), not a Timeline bug, but
 * one this test setup must avoid to test Timeline honestly.
 */
async function mountTimeline() {
  const store = useSessionDetailStore()
  await store.loadSession('session-1')
  const wrapper = mount(Timeline, { attachTo: document.body })
  await flushPromises()
  return { wrapper, store }
}

describe('Timeline', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSession = vi.fn(() => okResponse({ ...getSession200Default, id: 'session-1' }))
    getTimeline = vi.fn(() => timelinePage([]))
    getTurns = vi.fn(() => okResponse({ data: [], page: { next_cursor: null, has_more: false } }))
    getEvent = vi.fn(() => okResponse(getSessionTimeline200Default.data[0]))
  })

  afterEach(() => {
    document.body.replaceChildren()
    vi.restoreAllMocks()
  })

  it('loads the timeline and turns on mount', async () => {
    await mountTimeline()
    expect(getTimeline).toHaveBeenCalled()
    expect(getTurns).toHaveBeenCalled()
  })

  it('shows an ErrorState (never a blank timeline) on a load failure, and retry reloads', async () => {
    getTimeline = vi.fn(() => errorResponse(500))
    const { wrapper } = await mountTimeline()
    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)

    getTimeline = vi.fn(() => timelinePage([makeTimelineEvent({})]))
    const retryButton = wrapper.findAll('button').find((b) => b.text().includes('Retry'))
    await retryButton?.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="event-row"]').exists()).toBe(true)
  })

  it('shows an EmptyState when the timeline loaded with zero events', async () => {
    const { wrapper } = await mountTimeline()
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('groups events by prompt_id into turns, and null prompt_id under an explicit "no turn" header', async () => {
    getTimeline = vi.fn(() =>
      timelinePage([
        makeTimelineEvent({ prompt_id: null, tool_use_id: null, kind: 'hook.registered' }),
        makeTimelineEvent({ prompt_id: 'p_88f1', kind: 'tool.decision' }),
      ]),
    )
    const { wrapper } = await mountTimeline()

    const headers = wrapper.findAll('[data-testid="timeline-group-header"]')
    expect(headers).toHaveLength(2)
    expect(headers[0]!.text()).toContain('No turn')
    expect(headers[1]!.text()).toContain('p_88f1')
  })

  /**
   * Regression for the "turn headers appear after events they own" bug: the
   * old `groups` computed keyed by a `Map<prompt_id, Group>` bucketed *every*
   * item sharing a `prompt_id` together, wherever in the sequence it
   * occurred — so a no-turn event that happened chronologically *after* a
   * turn's own events still rendered inside the single leading "no turn"
   * block, ahead of the turn header it should follow. Grouping must instead
   * split on contiguous runs of the same `prompt_id`, so groups render in
   * the exact order the (already chronological) event sequence provides.
   */
  it('groups by contiguous runs, not a global bucket, so an interleaved no-turn event after a turn does not get pulled ahead of that turn (ordering bug)', async () => {
    getTimeline = vi.fn(() =>
      timelinePage([
        makeTimelineEvent({ prompt_id: null, tool_use_id: null, kind: 'hook.registered', ts: '2026-08-14T01:00:00.000Z' }),
        makeTimelineEvent({ prompt_id: 'p_1', tool_use_id: null, kind: 'turn.start', ts: '2026-08-14T01:00:05.000Z' }),
        makeTimelineEvent({ prompt_id: null, tool_use_id: null, kind: 'llm.request', ts: '2026-08-14T01:00:10.000Z' }),
      ]),
    )
    const { wrapper } = await mountTimeline()

    const headers = wrapper.findAll('[data-testid="timeline-group-header"]')
    expect(headers).toHaveLength(3)
    expect(headers[0]!.text()).toContain('No turn')
    expect(headers[1]!.text()).toContain('p_1')
    expect(headers[2]!.text()).toContain('No turn')
  })

  it("shows the turn's cost/tokens from the turns store in its sticky header", async () => {
    const turn = listSessionTurns200Default.data[0]!
    getTurns = vi.fn(() => okResponse({ data: [turn], page: { next_cursor: null, has_more: false } }))
    getTimeline = vi.fn(() => timelinePage([makeTimelineEvent({ prompt_id: turn.prompt_id })]))

    const { wrapper } = await mountTimeline()
    expect(wrapper.get('[data-testid="timeline-group-header"]').text()).toContain('$0.42')
  })

  it('collapse toggle switches between collapsed and 1:1 raw rendering', async () => {
    getTimeline = vi.fn(() =>
      timelinePage([
        makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', tool_use_id: 'toolu_x', ts: '2026-08-14T01:00:00.000Z' }),
        makeTimelineEvent({ kind: 'tool.result', source: 'hook', tool_use_id: 'toolu_x', ts: '2026-08-14T01:00:00.300Z' }),
      ]),
    )
    const { wrapper } = await mountTimeline()
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(1)

    await wrapper.get('[data-testid="timeline-collapse-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(2)
  })

  it('a kind filter chip narrows the store filter and reloads', async () => {
    getTimeline = vi.fn(() => timelinePage([makeTimelineEvent({ kind: 'tool.decision' }), makeTimelineEvent({ kind: 'llm.request', tool_use_id: null })]))
    const { wrapper, store } = await mountTimeline()
    getTimeline.mockClear()

    await wrapper.get('[data-testid="timeline-kind-chip-llm.request"]').trigger('click')
    await flushPromises()

    expect(store.kinds).toEqual(['llm.request'])
    expect(getTimeline).toHaveBeenCalled()
  })

  it('clicking "Load more" calls loadMoreTimeline when more pages remain', async () => {
    getTimeline = vi.fn(() => timelinePage([makeTimelineEvent({ tool_use_id: 'toolu_page1' })], true))
    const { wrapper } = await mountTimeline()
    getTimeline.mockClear()
    getTimeline.mockImplementation(() => timelinePage([makeTimelineEvent({ tool_use_id: 'toolu_page2' })], false))

    await wrapper.get('[data-testid="timeline-load-more"]').trigger('click')
    await flushPromises()

    expect(getTimeline).toHaveBeenCalled()
    expect(wrapper.findAll('[data-testid="event-row"]').length).toBeGreaterThanOrEqual(2)
  })

  it('clicking an event row opens the detail sheet, which fetches by event_ref', async () => {
    const event = makeTimelineEvent({})
    getTimeline = vi.fn(() => timelinePage([event]))
    getEvent = vi.fn(() => okResponse({ ...event, attrs: { foo: 'bar' } }))

    const { wrapper } = await mountTimeline()
    await wrapper.get('[data-testid="event-row"]').trigger('click')
    await flushPromises()

    expect(getEvent).toHaveBeenCalled()
    expect(document.body.querySelector('[data-testid="event-detail-sheet"]')).toBeTruthy()
    expect(document.body.querySelector('[data-testid="json-viewer"]')?.textContent).toContain('foo')
  })

  /**
   * The P4-04/P4-05/P4-03 seam. A subagent node click routes to
   * `?tab=timeline&agent_id=…`; SessionDetailView's query watcher then calls
   * `setTimelineFilters`, which is a pure state setter. Every layer was
   * individually correct and individually tested — P4-05 asserts the store's
   * `agentId` lands, P4-04 asserts the kind chips refetch, the store does
   * send `agent_id` — but nothing asserted that an externally-set agent
   * filter actually changes the rendered events. Without Timeline's
   * `agentId` watcher this fails with the call count unchanged, which is
   * precisely the silent "filter applied, nothing filtered" bug it prevents.
   */
  it('refetches the timeline when an external agent_id filter is applied', async () => {
    const { store } = await mountTimeline()
    const callsBeforeFilter = getTimeline.mock.calls.length

    store.setTimelineFilters({ agentId: 'agent-107d2cba' })
    await flushPromises()

    expect(getTimeline.mock.calls.length).toBeGreaterThan(callsBeforeFilter)
    expect(store.agentId).toBe('agent-107d2cba')
  })

  it('refetches again when the agent filter is cleared back to null', async () => {
    const { store } = await mountTimeline()
    store.setTimelineFilters({ agentId: 'agent-107d2cba' })
    await flushPromises()
    const callsWhileFiltered = getTimeline.mock.calls.length

    store.setTimelineFilters({ agentId: null })
    await flushPromises()

    expect(getTimeline.mock.calls.length).toBeGreaterThan(callsWhileFiltered)
    expect(store.agentId).toBeNull()
  })
})
