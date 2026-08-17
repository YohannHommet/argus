import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SessionRow from '@/components/session/SessionRow.vue'
import SessionTable from '@/components/session/SessionTable.vue'
import StatusDot from '@/components/session/StatusDot.vue'
import SessionListView from '@/views/SessionListView.vue'
import { getFacets200Default, getMeta200Default, listSessions200Default } from '@/test/fixtures'
import { partialSessionSummary, secondSessionSummary, zeroToolCallsSessionSummary } from '@/test/fixtures.extra'

// Referenced (not called) by the mock factory below, then assigned per-test — vi.mock is hoisted
// above these imports at execution time, but the factory itself only runs lazily, on first import of
// '@/api/context', by which point `sessionListGetSessions` has already been assigned in beforeEach.
let sessionListGetSessions: ReturnType<typeof vi.fn>

function metaOkResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      if (path === '/api/v1/sessions') return sessionListGetSessions(init)
      // SessionListView (PLAN.md P4-10) needs meta/facets to tell a genuinely empty database
      // apart from these filters matching nothing — every fixture in this describe block has a
      // non-empty facets.projects, so it never falls into the SetupCard branch.
      if (path === '/api/v1/meta') return metaOkResponse(getMeta200Default)
      if (path === '/api/v1/facets') return metaOkResponse(getFacets200Default)
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

describe('SessionTable / SessionRow — rendering', () => {
  it('renders 3 distinct fixture sessions with correctly formatted cost and a reject-rate badge', () => {
    const sessions = [listSessions200Default.data[0]!, secondSessionSummary, zeroToolCallsSessionSummary]
    const wrapper = mount(SessionTable, {
      props: { sessions, sort: 'last_event_at' },
      global: { stubs: { teleport: true } },
    })

    const rows = wrapper.findAll('[data-testid="session-row"]')
    expect(rows).toHaveLength(3)

    // Session 0: cost.usd 4.2711 -> formatCost's 2-decimal branch.
    expect(rows[0]!.text()).toContain('$4.27')
    // Session 1 (secondSessionSummary): cost.usd 0.0031 -> below a cent, formatCost's 4-decimal branch.
    expect(rows[1]!.text()).toContain('$0.0031')

    const badges = wrapper.findAll('[data-testid="reject-rate-badge"]')
    expect(badges).toHaveLength(3)
    // Session 0: 3 / 96 tool calls -> a real percentage, not a dash.
    expect(badges[0]!.text()).toBe('3.1%')
    // Session 1: 1 / 20 -> a real percentage.
    expect(badges[1]!.text()).toBe('5.0%')
    // Session 2 (zeroToolCallsSessionSummary): 0 calls -> undefined rate, rendered as the dash, never "0%".
    expect(badges[2]!.text()).toBe('—')
    expect(badges[2]!.text()).not.toContain('0%')
  })

  it('a partial: true row renders without NaN or Invalid Date anywhere', () => {
    const wrapper = mount(SessionTable, {
      props: { sessions: [partialSessionSummary], sort: 'last_event_at' },
      global: { stubs: { teleport: true } },
    })

    expect(wrapper.text()).not.toContain('NaN')
    expect(wrapper.text()).not.toContain('Invalid Date')
    // started_at is null on a partial session (SPEC §1.7) — formatRelativeTime/formatDuration must
    // both fall back to the em dash rather than blow up on it.
    expect(wrapper.text()).toContain('—')
  })

  it('renders a real <table> at or below the virtualization threshold', () => {
    const wrapper = mount(SessionTable, {
      props: { sessions: [listSessions200Default.data[0]!], sort: 'last_event_at' },
    })
    expect(wrapper.find('table').exists()).toBe(true)
    expect(wrapper.find('[data-testid="session-virtual-table"]').exists()).toBe(false)
  })

  it('switches to the windowed div-grid layout above the virtualization threshold', () => {
    const many = Array.from({ length: 201 }, (_, i) => ({ ...listSessions200Default.data[0]!, id: `session-${i}` }))
    const wrapper = mount(SessionTable, {
      props: { sessions: many, sort: 'last_event_at' },
    })
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('[data-testid="session-virtual-table"]').exists()).toBe(true)
  })

  it('hides the "load more" button when has_more is false, shows it when true', async () => {
    const sessions = [listSessions200Default.data[0]!]
    const wrapperNoMore = mount(SessionTable, { props: { sessions, sort: 'last_event_at', hasMore: false } })
    expect(wrapperNoMore.find('[data-testid="load-more"]').exists()).toBe(false)

    const wrapperHasMore = mount(SessionTable, { props: { sessions, sort: 'last_event_at', hasMore: true } })
    expect(wrapperHasMore.find('[data-testid="load-more"]').exists()).toBe(true)

    await wrapperHasMore.get('[data-testid="load-more"]').trigger('click')
    expect(wrapperHasMore.emitted('loadMore')).toHaveLength(1)
  })

  it('renders ErrorState (not the table) when given an error, and emits retry', async () => {
    const wrapper = mount(SessionTable, {
      props: {
        sessions: [],
        sort: 'last_event_at',
        error: { name: 'ApiError', message: 'boom', type: 'urn:argus:error:boom', title: 'Boom', status: 500 } as never,
      },
    })
    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    expect(wrapper.find('table').exists()).toBe(false)

    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('clicking a header for a sortable column emits sort with that key; a non-sortable header emits nothing', async () => {
    const wrapper = mount(SessionTable, {
      props: { sessions: [listSessions200Default.data[0]!], sort: 'last_event_at' },
    })
    const headers = wrapper.findAll('th')
    const costHeader = headers.find((h) => h.text().includes('Cost'))!
    const projectHeader = headers.find((h) => h.text().includes('Project'))!

    await costHeader.trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['cost_usd']])

    await projectHeader.trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['cost_usd']])
  })

  it('a row click and an Enter keypress both emit selectSession with the session id', async () => {
    const session = listSessions200Default.data[0]!
    const wrapper = mount(SessionRow, { props: { session } })

    await wrapper.trigger('click')
    expect(wrapper.emitted('activate')).toHaveLength(1)

    await wrapper.trigger('keydown.enter')
    expect(wrapper.emitted('activate')).toHaveLength(2)
  })
})

describe('StatusDot', () => {
  it.each([
    ['active', 'bg-pending'],
    ['ended', 'bg-accept'],
    ['abandoned', 'bg-reject'],
    ['unknown', 'bg-unknown'],
  ])('maps status %s to %s', (status, expectedClass) => {
    const wrapper = mount(StatusDot, { props: { status } })
    expect(wrapper.find(`.${expectedClass}`).exists()).toBe(true)
  })

  it('falls back to the neutral/unknown token for a status outside the documented 4-value vocabulary', () => {
    const wrapper = mount(StatusDot, { props: { status: 'some_future_status' } })
    expect(wrapper.find('.bg-unknown').exists()).toBe(true)
  })
})

describe('SessionListView — error + retry integration (Phase-4 exit criterion 1)', () => {
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

  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountAt(path: string) {
    const { flushPromises } = await import('@vue/test-utils')
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/sessions', name: 'sessions', component: SessionListView }],
    })
    await router.push(path)
    await router.isReady()
    const wrapper = mount(SessionListView, { global: { plugins: [router] } })
    await flushPromises()
    return { router, wrapper }
  }

  it('a 500 renders ErrorState, and its retry button refetches and renders the data', async () => {
    sessionListGetSessions = vi.fn(() => errorResponse(500))
    const { wrapper } = await mountAt('/sessions')

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    expect(document.documentElement.getAttribute('data-capture-ready')).toBe('true')

    sessionListGetSessions.mockImplementation(() => okResponse({ data: listSessions200Default.data, page: { next_cursor: null, has_more: false } }))
    const { flushPromises } = await import('@vue/test-utils')
    await wrapper.get('[data-testid="error-state"] button').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="session-row"]')).toHaveLength(listSessions200Default.data.length)
  })

  it('is capture-ready once the initial fetch resolves with real data', async () => {
    sessionListGetSessions = vi.fn(() => okResponse({ data: listSessions200Default.data, page: { next_cursor: null, has_more: false } }))
    await mountAt('/sessions')
    expect(document.documentElement.getAttribute('data-capture-ready')).toBe('true')
  })
})
