import { flushPromises } from '@vue/test-utils'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { listToolCalls200Default } from '@/test/fixtures'
import { toolCallLiveSeed42OtelOnly } from '@/test/fixtures.extra'

let getToolCalls: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      if (path === '/api/v1/tool-calls') return getToolCalls(init)
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

function page(data: typeof listToolCalls200Default.data, opts: { next_cursor?: string | null; has_more?: boolean } = {}) {
  return { data, page: { next_cursor: opts.next_cursor ?? null, has_more: opts.has_more ?? false } }
}

/**
 * Same `app.runWithContext` trick as `sessions.spec.ts`'s `setupStore` — `useToolsStore` calls
 * `useRouter()`/`useRoute()` from its own setup, which throws without one.
 */
async function setupStore(initialPath = '/tools') {
  const { useToolsStore } = await import('../tools')
  const pinia = createPinia()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/tools', name: 'tools', component: { template: '<div/>' } }],
  })
  await router.push(initialPath)
  await router.isReady()

  const app = createApp({})
  app.use(pinia)
  app.use(router)

  let store!: ReturnType<typeof useToolsStore>
  app.runWithContext(() => {
    store = useToolsStore()
  })
  await vi.waitFor(() => expect(store.initialized).toBe(true))
  return { store, router }
}

describe('tools store — filters <-> URL query (pure helpers)', () => {
  it('round-trips: filters -> query -> filters reproduces the same state (reload stability)', async () => {
    const { filtersToQuery, parseFiltersFromQuery } = await import('../tools')
    const filters = {
      project: ['argus'],
      tool: ['Edit'],
      decisionSource: ['hook', 'user_reject'],
      from: '-7d',
      to: null,
    }

    const query = filtersToQuery(filters)
    expect(parseFiltersFromQuery(query)).toEqual(filters)
  })

  it('omits empty arrays/nulls entirely rather than serialising `?project=`', async () => {
    const { filtersToQuery, emptyToolCallFilters } = await import('../tools')
    expect(filtersToQuery(emptyToolCallFilters())).toEqual({})
  })

  it('never emits a `sort` key — GET /api/v1/tool-calls has no sort query param (schema.d.ts)', async () => {
    const { filtersToQuery, emptyToolCallFilters } = await import('../tools')
    const query = filtersToQuery({ ...emptyToolCallFilters(), tool: ['Bash'] })
    expect(query).not.toHaveProperty('sort')
  })

  it('reads `tool` from the API-shaped query param', async () => {
    const { parseFiltersFromQuery } = await import('../tools')
    expect(parseFiltersFromQuery({ tool: 'Read' }).tool).toEqual(['Read'])
  })

  it('also reads `tool_name` — the field name DecisionMatrix.vue emits — merging with `tool`', async () => {
    const { parseFiltersFromQuery } = await import('../tools')
    expect(parseFiltersFromQuery({ tool_name: 'Read' }).tool).toEqual(['Read'])
    expect(parseFiltersFromQuery({ tool: 'Read', tool_name: 'Read' }).tool).toEqual(['Read'])
    expect(parseFiltersFromQuery({ tool: 'Bash', tool_name: 'Read' }).tool).toEqual(['Bash', 'Read'])
  })

  it('supports repeated decision_source params (OR within the field, SPEC §4.1)', async () => {
    const { parseFiltersFromQuery } = await import('../tools')
    expect(parseFiltersFromQuery({ decision_source: ['user_reject', 'hook'] }).decisionSource).toEqual([
      'user_reject',
      'hook',
    ])
  })
})

describe('useToolsStore', () => {
  beforeEach(() => {
    vi.resetModules()
    getToolCalls = vi.fn(() => okResponse(page(listToolCalls200Default.data)))
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('fetches once on creation and exposes the page', async () => {
    const { store } = await setupStore()
    expect(getToolCalls).toHaveBeenCalledTimes(1)
    expect(store.toolCalls).toEqual(listToolCalls200Default.data)
  })

  it('never sends a `sort` query param — GET /api/v1/tool-calls has none (verified against schema.d.ts)', async () => {
    await setupStore()
    const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
    expect(callArgs.params.query).not.toHaveProperty('sort')
  })

  describe('exit criterion 5 — the DecisionMatrix deep link', () => {
    it('mounting at /tools?decision_source=user_reject applies the filter and sends it to the API', async () => {
      const { store } = await setupStore('/tools?decision_source=user_reject')

      expect(store.filters.decisionSource).toEqual(['user_reject'])

      const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
      expect(callArgs.params.query.decision_source).toEqual(['user_reject'])
    })

    it('also reads tool_name (DecisionMatrix.vue emits {tool_name, decision_source} together)', async () => {
      const { store } = await setupStore('/tools?tool_name=Edit&decision_source=user_reject')

      expect(store.filters.tool).toEqual(['Edit'])
      expect(store.filters.decisionSource).toEqual(['user_reject'])

      const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
      expect(callArgs.params.query.tool).toEqual(['Edit'])
      expect(callArgs.params.query.decision_source).toEqual(['user_reject'])
    })

    it('supports repeated decision_source query params (AND across fields, OR within one — SPEC §4.1)', async () => {
      const { store } = await setupStore('/tools?decision_source=user_reject&decision_source=user_abort')
      expect(store.filters.decisionSource).toEqual(['user_reject', 'user_abort'])
    })
  })

  it('setFilters updates the route query and refetches with the new params', async () => {
    const { store, router } = await setupStore()
    getToolCalls.mockClear()

    store.setFilters({ decisionSource: ['hook'] })
    await flushPromises()

    expect(router.currentRoute.value.query.decision_source).toEqual(['hook'])
    expect(getToolCalls).toHaveBeenCalledTimes(1)
    const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
    expect(callArgs.params.query.decision_source).toEqual(['hook'])
  })

  it('a filter change drops the cursor immediately — a stale cursor is never sent with new filters', async () => {
    getToolCalls = vi.fn(() => okResponse(page(listToolCalls200Default.data, { next_cursor: 'eyJrIjoi4oCm', has_more: true })))
    const { store } = await setupStore()

    expect(store.nextCursor).toBe('eyJrIjoi4oCm')
    expect(store.hasMore).toBe(true)

    getToolCalls.mockClear()
    getToolCalls.mockImplementation(() => okResponse(page(listToolCalls200Default.data)))

    store.setFilters({ tool: ['Edit'] })
    // Reset synchronously, not only once the refetch settles.
    expect(store.nextCursor).toBeNull()
    expect(store.hasMore).toBe(false)

    await flushPromises()
    const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
    expect(callArgs.params.query.cursor).toBeUndefined()
  })

  it('"load more" appends via next_cursor and never duplicates an id already in the list', async () => {
    const firstId = listToolCalls200Default.data[0]!.id
    const secondRow = { ...toolCallLiveSeed42OtelOnly, id: 'second-id' }
    getToolCalls = vi
      .fn()
      .mockImplementationOnce(() => okResponse(page([listToolCalls200Default.data[0]!], { next_cursor: 'cursor-1', has_more: true })))
      .mockImplementationOnce(() =>
        okResponse(page([listToolCalls200Default.data[0]!, secondRow], { next_cursor: null, has_more: false })),
      )
    const { store } = await setupStore()

    await store.loadMore()

    expect(store.toolCalls.map((t) => t.id)).toEqual([firstId, 'second-id'])
    expect(store.hasMore).toBe(false)

    const secondCall = getToolCalls.mock.calls[1]![0] as { params: { query: Record<string, unknown> } }
    expect(secondCall.params.query.cursor).toBe('cursor-1')
  })

  it('has_more: false means loadMore is a no-op', async () => {
    const { store } = await setupStore()
    expect(store.hasMore).toBe(false)

    getToolCalls.mockClear()
    await store.loadMore()
    expect(getToolCalls).not.toHaveBeenCalled()
  })

  it('refresh() re-fetches and clears a prior error', async () => {
    getToolCalls = vi.fn(() => apiErrorResponse(500))
    const { store } = await setupStore()

    expect(store.error).not.toBeNull()
    expect(store.toolCalls).toEqual([])

    getToolCalls.mockImplementation(() => okResponse(page(listToolCalls200Default.data)))
    await store.refresh()

    expect(store.error).toBeNull()
    expect(store.toolCalls).toEqual(listToolCalls200Default.data)
  })
})
