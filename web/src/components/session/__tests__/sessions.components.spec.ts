import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SessionRow from '@/components/session/SessionRow.vue'
import SessionTable from '@/components/session/SessionTable.vue'
import StatusDot from '@/components/session/StatusDot.vue'
import SessionListView from '@/views/SessionListView.vue'
import { getFacets200Default, getMeta200Default, listSessions200Default } from '@/test/fixtures'
import {
  criticalRejectRateSessionSummary,
  partialSessionSummary,
  secondSessionSummary,
  zeroToolCallsSessionSummary,
} from '@/test/fixtures.extra'

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

  it('grades the reject-rate badge neutral/warn/critical by its actual rate (round-4 UI gap)', () => {
    const sessions = [listSessions200Default.data[0]!, secondSessionSummary, criticalRejectRateSessionSummary]
    const wrapper = mount(SessionTable, {
      props: { sessions, sort: 'last_event_at' },
      global: { stubs: { teleport: true } },
    })

    const badges = wrapper.findAll('[data-testid="reject-rate-badge"] [data-severity]')
    // Session 0: 3/96 = 3.125% — below the 5% warn threshold.
    expect(badges[0]!.attributes('data-severity')).toBe('neutral')
    // secondSessionSummary: 1/20 = 5.0% — at the warn threshold.
    expect(badges[1]!.attributes('data-severity')).toBe('warn')
    // criticalRejectRateSessionSummary: 4/20 = 20% — past the 15% critical threshold.
    expect(badges[2]!.attributes('data-severity')).toBe('critical')
    expect(badges[2]!.classes()).toContain('text-destructive')
  })

  it('grades Cost against the visible set\'s own distribution, not a fixed dollar figure', () => {
    const makeSession = (id: string, usd: number) => ({ ...listSessions200Default.data[0]!, id, cost: { ...listSessions200Default.data[0]!.cost, usd } })
    // 10 rows, evenly spread $0.10..$1.00: p75=$0.775, p90=$0.91 — only the top one or two rows
    // should grade as outliers, not the whole page.
    const sessions = Array.from({ length: 10 }, (_, i) => makeSession(`cost-${i}`, (i + 1) / 10))

    const wrapper = mount(SessionTable, {
      props: { sessions, sort: 'last_event_at' },
      global: { stubs: { teleport: true } },
    })

    const costCells = wrapper.findAll('[data-severity]').filter((c) => c.text().startsWith('$'))
    expect(costCells.map((c) => c.attributes('data-severity'))).toEqual([
      'neutral', 'neutral', 'neutral', 'neutral', 'neutral', 'neutral', 'neutral', 'warn', 'warn', 'critical',
    ])
  })

  it('does not flag any cost as an outlier when every visible session costs the same', () => {
    const makeSession = (id: string) => ({ ...listSessions200Default.data[0]!, id, cost: { ...listSessions200Default.data[0]!.cost, usd: 1.5 } })
    const sessions = Array.from({ length: 5 }, (_, i) => makeSession(`flat-${i}`))

    const wrapper = mount(SessionTable, {
      props: { sessions, sort: 'last_event_at' },
      global: { stubs: { teleport: true } },
    })

    const costCells = wrapper.findAll('[data-severity]').filter((c) => c.text().startsWith('$'))
    expect(costCells.every((c) => c.attributes('data-severity') === 'neutral')).toBe(true)
  })

  it('right-aligns the magnitude column headers and cells (Turns, Events, Tools, Tokens, Cost, Duration)', () => {
    const wrapper = mount(SessionTable, {
      props: { sessions: [listSessions200Default.data[0]!], sort: 'last_event_at' },
    })
    const headers = wrapper.findAll('th')
    for (const label of ['Duration', 'Turns', 'Events', 'Tools', 'Tokens', 'Cost']) {
      const header = headers.find((h) => h.text().includes(label))!
      expect(header.classes()).toContain('text-right')
    }
    // Reject % is a badge/chip, not a bare number — deliberately not right-aligned.
    expect(headers.find((h) => h.text().includes('Reject'))!.classes()).not.toContain('text-right')
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
    ['active', 'bg-primary'],
    ['ended', 'bg-accept/60'],
    ['abandoned', 'bg-warn'],
    ['unknown', 'bg-unknown'],
  ])('maps status %s to %s', (status, expectedClass) => {
    const wrapper = mount(StatusDot, { props: { status } })
    expect(wrapper.find('[data-testid="status-dot"] > span').classes()).toContain(expectedClass)
  })

  it('pulses the active dot — the only status that is still changing right now', () => {
    const wrapper = mount(StatusDot, { props: { status: 'active' } })
    expect(wrapper.find('.animate-pulse').exists()).toBe(true)
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

  it('renders a "loaded set" summary strip with cost/tokens/session totals over the fetched page, not a global total', async () => {
    sessionListGetSessions = vi.fn(() => okResponse({ data: listSessions200Default.data, page: { next_cursor: null, has_more: false } }))
    const { wrapper } = await mountAt('/sessions')

    const summary = wrapper.find('[data-testid="session-list-summary"]')
    expect(summary.exists()).toBe(true)
    expect(summary.text()).toContain('Loaded set')
    // The single fixture session: cost.usd 4.2711 -> "$4.27"; tokens 41233+18944+1204331+88210 =
    // 1,352,718 -> formatTokens's SI-suffixed "1.4M"; one session in the loaded page.
    expect(wrapper.get('[data-testid="summary-cost"]').text()).toBe('$4.27')
    expect(wrapper.get('[data-testid="summary-tokens"]').text()).toBe('1.4M')
    expect(wrapper.get('[data-testid="summary-sessions"]').text()).toBe('1')
  })
})

describe('SessionFilterBar — time range control (round-4 UI gap)', () => {
  function okResponse<T>(data: T) {
    return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
  }

  let mountedWrapper: Awaited<ReturnType<typeof mount>> | null = null

  beforeEach(() => {
    setActivePinia(createPinia())
    sessionListGetSessions = vi.fn(() => okResponse({ data: listSessions200Default.data, page: { next_cursor: null, has_more: false } }))
  })

  afterEach(() => {
    vi.restoreAllMocks()
    mountedWrapper?.unmount()
    mountedWrapper = null
  })

  async function mountAt(path: string) {
    const { flushPromises } = await import('@vue/test-utils')
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/sessions', name: 'sessions', component: SessionListView }],
    })
    await router.push(path)
    await router.isReady()
    // No teleport stub here (unlike the rendering describe block above): that stub renders an
    // empty placeholder for `<Teleport>`'s default slot rather than its real content, which would
    // hide the exact popover markup these tests need to inspect. Real `<Teleport>` moves the
    // popover into `document.body` (attached here so it actually mounts), so assertions on its
    // content query the document directly.
    const wrapper = mount(SessionListView, { attachTo: document.body, global: { plugins: [router] } })
    mountedWrapper = wrapper
    await flushPromises()
    return { router, wrapper }
  }

  it('has no bare native date inputs at rest — no [type=date] control on the page', async () => {
    const { wrapper } = await mountAt('/sessions')
    expect(wrapper.find('input[type="date"]').exists()).toBe(false)
  })

  it('defaults to the "All" preset selected when no from/to filter is active', async () => {
    const { wrapper } = await mountAt('/sessions')
    const all = wrapper.get('[data-testid="filter-range-all"]')
    expect(all.attributes('data-state')).toBe('active')
  })

  it('clicking the "7d" preset writes the API\'s own relative shorthand into the from param and refetches', async () => {
    const { wrapper, router } = await mountAt('/sessions')
    const { flushPromises } = await import('@vue/test-utils')

    // reka-ui's TabsTrigger selects on `mousedown` (immediate tab-switch feedback), not `click`.
    await wrapper.get('[data-testid="filter-range-7d"]').trigger('mousedown', { button: 0 })
    await flushPromises()

    expect(router.currentRoute.value.query.from).toBe('-7d')
    expect(router.currentRoute.value.query.to).toBeUndefined()
    expect(wrapper.get('[data-testid="filter-range-7d"]').attributes('data-state')).toBe('active')
  })

  it('the custom popover applies a typed range as styled text fields, with no native picker chrome visible at rest', async () => {
    const { wrapper, router } = await mountAt('/sessions')
    const { DOMWrapper, flushPromises } = await import('@vue/test-utils')

    await wrapper.get('[data-testid="filter-range-custom"]').trigger('click')
    await flushPromises()

    // PopoverContent teleports into `document.body` (a sibling of the mounted tree, not a
    // descendant) — `wrapper.get` can't see it, so this queries the real DOM directly.
    const body = new DOMWrapper(document.body)
    const fromInput = body.get('[data-testid="filter-from"]')
    const toInput = body.get('[data-testid="filter-to"]')
    // Styled text inputs, not native date pickers — no [type=date] UA chrome/placeholder.
    expect(fromInput.attributes('type')).toBe('text')
    expect(fromInput.attributes('placeholder')).toBe('2026-08-01')
    expect(toInput.attributes('placeholder')).toBe('2026-08-17')

    await fromInput.setValue('2026-08-01')
    await toInput.setValue('2026-08-10')
    await body.get('[data-testid="filter-range-apply"]').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query.from).toBe('2026-08-01')
    expect(router.currentRoute.value.query.to).toBe('2026-08-10')
    // No preset shorthand matches an explicit absolute range -> none of the segmented presets
    // claim to be active; the custom trigger reflects the applied range instead.
    expect(wrapper.get('[data-testid="filter-range-all"]').attributes('data-state')).not.toBe('active')
    expect(wrapper.get('[data-testid="filter-range-custom"]').text()).toContain('2026-08-01')
  })
})
