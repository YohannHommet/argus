import { flushPromises } from '@vue/test-utils'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getFacets200Default, listSessions200Default } from '@/test/fixtures'

let getSessions: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      if (path === '/api/v1/sessions') return getSessions(init)
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

function page(data: typeof listSessions200Default.data, opts: { next_cursor?: string | null; has_more?: boolean } = {}) {
  return { data, page: { next_cursor: opts.next_cursor ?? null, has_more: opts.has_more ?? false } }
}

/**
 * `useSessionsStore` calls `useRouter()`/`useRoute()` from its own setup — unlike `useApiClient`
 * (which falls back to a module singleton outside an injection context), vue-router's composables
 * throw without one. `app.runWithContext` gives the store real router/pinia injection without
 * mounting any DOM, which is all these tests need.
 */
async function setupStore(initialPath = '/sessions') {
  const { useSessionsStore } = await import('../sessions')
  const pinia = createPinia()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/sessions', name: 'sessions', component: { template: '<div/>' } }],
  })
  await router.push(initialPath)
  await router.isReady()

  const app = createApp({})
  app.use(pinia)
  app.use(router)

  let store!: ReturnType<typeof useSessionsStore>
  app.runWithContext(() => {
    store = useSessionsStore()
  })
  await vi.waitFor(() => expect(store.initialized).toBe(true))
  return { store, router }
}

describe('sessions store — filters <-> URL query (pure helpers)', () => {
  it('round-trips: filters -> query -> filters reproduces the same state (reload stability)', async () => {
    const { filtersToQuery, parseFiltersFromQuery, DEFAULT_SORT } = await import('../sessions')
    const filters = {
      project: ['argus', 'platform'],
      vendor: ['claude_code'],
      model: [],
      status: ['ended' as const, 'abandoned' as const],
      tool: ['Edit'],
      decisionSource: ['hook'],
      from: '-7d',
      to: null,
      q: 'studio',
    }

    const query = filtersToQuery(filters, DEFAULT_SORT)
    const parsed = parseFiltersFromQuery(query)

    expect(parsed.filters).toEqual(filters)
    expect(parsed.sort).toBe(DEFAULT_SORT)
  })

  it('omits empty arrays and an empty q entirely rather than serialising `?project=`', async () => {
    const { filtersToQuery, emptySessionFilters, DEFAULT_SORT } = await import('../sessions')
    const query = filtersToQuery(emptySessionFilters(), DEFAULT_SORT)
    expect(query).toEqual({})
  })

  it('drops a non-vocabulary status and a garbled sort rather than throwing', async () => {
    const { parseFiltersFromQuery, DEFAULT_SORT } = await import('../sessions')
    const parsed = parseFiltersFromQuery({ status: ['active', 'bogus'], sort: 'not-a-real-sort' })
    expect(parsed.filters.status).toEqual(['active'])
    expect(parsed.sort).toBe(DEFAULT_SORT)
  })
})

describe('sessions store — reject rate honesty', () => {
  it('computes a real ratio when there is hook coverage and at least one call', async () => {
    const { computeRejectRate } = await import('../sessions')
    expect(computeRejectRate({ tool_call_count: 96, tool_reject_count: 3 })).toBeCloseTo(3 / 96)
  })

  it('is null (not 0) when tool_call_count is 0 — an undefined rate, not a measured zero', async () => {
    const { computeRejectRate } = await import('../sessions')
    expect(computeRejectRate({ tool_call_count: 0, tool_reject_count: 0 })).toBeNull()
  })

  it('is null when tool_call_count is null/undefined — no hook coverage', async () => {
    const { computeRejectRate } = await import('../sessions')
    expect(computeRejectRate({ tool_call_count: null, tool_reject_count: null })).toBeNull()
    expect(computeRejectRate({ tool_call_count: undefined, tool_reject_count: undefined })).toBeNull()
  })
})

describe('useSessionsStore', () => {
  beforeEach(() => {
    vi.resetModules()
    getSessions = vi.fn(() => okResponse(page(listSessions200Default.data)))
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('fetches once on creation and exposes the page', async () => {
    const { store } = await setupStore()
    expect(getSessions).toHaveBeenCalledTimes(1)
    expect(store.sessions).toEqual(listSessions200Default.data)
  })

  it('parses filters from the initial route query (reload stability)', async () => {
    const { store } = await setupStore('/sessions?project=argus&status=ended&status=abandoned&q=studio')
    expect(store.filters.project).toEqual(['argus'])
    expect(store.filters.status).toEqual(['ended', 'abandoned'])
    expect(store.filters.q).toBe('studio')
  })

  it('setting a filter updates the route query and triggers exactly one debounced refetch', async () => {
    const { store, router } = await setupStore()
    getSessions.mockClear()

    store.setFilters({ project: ['argus'] })
    // vue-router's own navigation resolves on a microtask even for `replace()` — awaiting one tick
    // (well before the debounce timer below fires) is enough to observe the URL update, and it still
    // lands ahead of the debounced fetch.
    await flushPromises()
    expect(router.currentRoute.value.query.project).toEqual(['argus'])
    expect(getSessions).not.toHaveBeenCalled()

    await vi.runAllTimersAsync()
    expect(getSessions).toHaveBeenCalledTimes(1)
  })

  it('a rapid burst of filter changes in the same tick still triggers exactly one refetch', async () => {
    const { store } = await setupStore()
    getSessions.mockClear()

    store.setFilters({ project: ['argus'] })
    store.setFilters({ vendor: ['claude_code'] })
    store.setFilters({ status: ['ended'] })

    await vi.runAllTimersAsync()
    expect(getSessions).toHaveBeenCalledTimes(1)
  })

  it('setSearch debounces ~300ms and collapses several keystrokes into one refetch', async () => {
    const { store } = await setupStore()
    getSessions.mockClear()

    store.setSearch('s')
    store.setSearch('st')
    store.setSearch('stu')
    expect(getSessions).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(299)
    expect(getSessions).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)
    expect(getSessions).toHaveBeenCalledTimes(1)
  })

  it('a filter change drops the cursor immediately — a stale cursor is never sent with new filters', async () => {
    getSessions = vi.fn(() => okResponse(page(listSessions200Default.data, { next_cursor: 'eyJrIjoi4oCm', has_more: true })))
    const { store } = await setupStore()

    expect(store.nextCursor).toBe('eyJrIjoi4oCm')
    expect(store.hasMore).toBe(true)

    getSessions.mockClear()
    getSessions.mockImplementation(() => okResponse(page(listSessions200Default.data)))

    store.setFilters({ project: ['argus'] })
    // Reset synchronously, not only once the debounced fetch eventually runs.
    expect(store.nextCursor).toBeNull()
    expect(store.hasMore).toBe(false)

    await vi.runAllTimersAsync()
    const callArgs = getSessions.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
    expect(callArgs.params.query.cursor).toBeUndefined()
  })

  it('"load more" appends via next_cursor and never duplicates an id already in the list', async () => {
    const firstId = listSessions200Default.data[0]!.id
    const secondSession = { ...listSessions200Default.data[0], id: 'second-id' }
    getSessions = vi
      .fn()
      .mockImplementationOnce(() => okResponse(page([listSessions200Default.data[0]!], { next_cursor: 'cursor-1', has_more: true })))
      .mockImplementationOnce(() =>
        // The server re-delivers `firstId` (a boundary re-delivery) alongside a genuinely new row.
        okResponse(page([listSessions200Default.data[0]!, secondSession], { next_cursor: null, has_more: false })),
      )
    const { store } = await setupStore()

    await store.loadMore()

    expect(store.sessions.map((s) => s.id)).toEqual([firstId, 'second-id'])
    expect(store.hasMore).toBe(false)

    const secondCall = getSessions.mock.calls[1]![0] as { params: { query: Record<string, unknown> } }
    expect(secondCall.params.query.cursor).toBe('cursor-1')
  })

  it('has_more: false means loadMore is a no-op', async () => {
    const { store } = await setupStore()
    expect(store.hasMore).toBe(false)

    getSessions.mockClear()
    await store.loadMore()
    expect(getSessions).not.toHaveBeenCalled()
  })

  it('applySessionUpdate replaces a row already in the list by id, and is a no-op for an unknown id', async () => {
    const { store } = await setupStore()
    const original = listSessions200Default.data[0]!
    const updated = { ...original, event_count: original.event_count + 1 }

    store.applySessionUpdate(updated)
    expect(store.sessions[0]!.event_count).toBe(original.event_count + 1)

    const before = store.sessions.slice()
    store.applySessionUpdate({ ...original, id: 'not-in-the-list' })
    expect(store.sessions).toEqual(before)
  })

  // PLAN.md P5-06 / SPEC §6.4: the store itself reacts to `liveStore.sessions` (SessionListView.vue
  // owns the actual `subscribe()` call — see sessions.ts's own doc comment on this watcher), so a
  // frame can be asserted here by writing straight into `liveStore.sessions`, with no EventSource or
  // fake factory involved at all.
  it('applies a live session frame onto an already-loaded row, and never inserts a row outside the loaded page', async () => {
    const { store } = await setupStore()
    const { useLiveStore } = await import('../live')
    const live = useLiveStore()
    const original = listSessions200Default.data[0]!

    live.sessions.set(original.id, { ...original, event_count: original.event_count + 5 })
    await flushPromises()
    expect(store.sessions.find((s) => s.id === original.id)?.event_count).toBe(original.event_count + 5)

    const before = store.sessions.length
    live.sessions.set('session-not-in-the-loaded-page', { ...original, id: 'session-not-in-the-loaded-page' })
    await flushPromises()
    expect(store.sessions).toHaveLength(before)
    expect(store.sessions.some((s) => s.id === 'session-not-in-the-loaded-page')).toBe(false)
  })

  it('refresh() re-fetches and retry from an error clears it', async () => {
    getSessions = vi.fn(() => apiErrorResponse(500))
    const { store } = await setupStore()

    expect(store.error).not.toBeNull()
    expect(store.sessions).toEqual([])

    getSessions.mockImplementation(() => okResponse(page(listSessions200Default.data)))
    await store.refresh()

    expect(store.error).toBeNull()
    expect(store.sessions).toEqual(listSessions200Default.data)
  })

  it('meta store facets fixture stays importable alongside sessions (sanity: no cross-store drift)', () => {
    expect(getFacets200Default.projects).toContain('argus')
  })
})
