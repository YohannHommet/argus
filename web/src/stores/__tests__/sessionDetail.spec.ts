import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/errors'
import {
  getEvent200Default,
  getSession200Default,
  getSessionSubagents200Default,
  getSessionTimeline200Default,
  listSessionToolCalls200Default,
  listSessionTurns200Default,
} from '@/test/fixtures'

// Same style as stores/__tests__/meta.spec.ts: mock `useApiClient()` directly
// with a fake `GET` that dispatches on path, rather than exercising real Vue
// injection — the injection plumbing itself is covered by api/context's own
// tests.
let getSession: ReturnType<typeof vi.fn>
let getTimeline: ReturnType<typeof vi.fn>
let getTurns: ReturnType<typeof vi.fn>
let getToolCalls: ReturnType<typeof vi.fn>
let getSubagents: ReturnType<typeof vi.fn>
let getEvent: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, options?: { params?: { path?: Record<string, string> } }) => {
      const id = options?.params?.path?.id ?? options?.params?.path?.ref
      if (path === '/api/v1/sessions/{id}') return getSession(id)
      if (path === '/api/v1/sessions/{id}/timeline') return getTimeline(id)
      if (path === '/api/v1/sessions/{id}/turns') return getTurns(id)
      if (path === '/api/v1/sessions/{id}/tool-calls') return getToolCalls(id)
      if (path === '/api/v1/sessions/{id}/subagents') return getSubagents(id)
      if (path === '/api/v1/events/{ref}') return getEvent(id)
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function apiErrorResponse(status: number) {
  const problem = { type: 'urn:argus:error:boom', title: 'Boom', status }
  return Promise.resolve({
    data: undefined,
    error: problem,
    response: new Response(JSON.stringify(problem), { status, headers: { 'Content-Type': 'application/problem+json' } }),
  })
}

function sessionWithId(id: string) {
  return { ...getSession200Default, id }
}

describe('useSessionDetailStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSession = vi.fn((id: string) => okResponse(sessionWithId(id)))
    getTimeline = vi.fn(() => okResponse(getSessionTimeline200Default))
    getTurns = vi.fn(() => okResponse(listSessionTurns200Default))
    getToolCalls = vi.fn(() => okResponse(listSessionToolCalls200Default))
    getSubagents = vi.fn(() => okResponse(getSessionSubagents200Default))
    getEvent = vi.fn(() => okResponse(getEvent200Default))
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('loadSession fetches and exposes the session, loading and error state', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()

    expect(store.loading).toBe(false)
    const promise = store.loadSession('s1')
    expect(store.loading).toBe(true)
    await promise

    expect(store.loading).toBe(false)
    expect(store.session).toEqual(sessionWithId('s1'))
    expect(store.error).toBeNull()
    expect(getSession).toHaveBeenCalledTimes(1)
  })

  it('surfaces a 404 as an ApiError and nulls the session, not a crash', async () => {
    getSession = vi.fn(() => apiErrorResponse(404))
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()

    await store.loadSession('missing')

    expect(store.session).toBeNull()
    expect(store.error).toBeInstanceOf(ApiError)
    expect((store.error as ApiError).status).toBe(404)
  })

  it('turns/toolCalls/subagents are lazy: not fetched until their own load action is called', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()

    await store.loadSession('s1')
    expect(getTurns).not.toHaveBeenCalled()
    expect(getToolCalls).not.toHaveBeenCalled()
    expect(getSubagents).not.toHaveBeenCalled()

    await store.loadTurns()
    expect(store.turns).toEqual(listSessionTurns200Default.data)
    expect(getTurns).toHaveBeenCalledTimes(1)

    await store.loadToolCalls()
    expect(store.toolCalls).toEqual(listSessionToolCalls200Default.data)

    await store.loadSubagents()
    expect(store.subagents).toEqual(getSessionSubagents200Default.data)
    expect(store.costAttribution).toEqual(getSessionSubagents200Default.cost_attribution)

    // A second activation of the same tab does not refetch.
    await store.loadTurns()
    await store.loadToolCalls()
    await store.loadSubagents()
    expect(getTurns).toHaveBeenCalledTimes(1)
    expect(getToolCalls).toHaveBeenCalledTimes(1)
    expect(getSubagents).toHaveBeenCalledTimes(1)
  })

  it('loadTimeline resets on first call, loadMoreTimeline appends and stops at has_more: false', async () => {
    getTimeline = vi
      .fn()
      .mockReturnValueOnce(
        okResponse({ data: [getSessionTimeline200Default.data[0]], page: { next_cursor: 'c1', has_more: true } }),
      )
      .mockReturnValueOnce(
        okResponse({ data: [{ ...getSessionTimeline200Default.data[0], event_ref: 'ref2' }], page: { next_cursor: null, has_more: false } }),
      )
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')

    await store.loadTimeline()
    expect(store.timelineItems).toHaveLength(1)
    expect(store.timelineHasMore).toBe(true)

    await store.loadMoreTimeline()
    expect(store.timelineItems).toHaveLength(2)
    expect(store.timelineHasMore).toBe(false)
    expect(getTimeline).toHaveBeenCalledTimes(2)

    // No more pages: a further call is a no-op.
    await store.loadMoreTimeline()
    expect(getTimeline).toHaveBeenCalledTimes(2)
  })

  it('loadEvent caches by event_ref: a second call for the same ref does not refetch', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')

    const first = await store.loadEvent(getEvent200Default.event_ref)
    const second = await store.loadEvent(getEvent200Default.event_ref)

    expect(first).toEqual(getEvent200Default)
    expect(second).toEqual(getEvent200Default)
    expect(getEvent).toHaveBeenCalledTimes(1)
  })

  it('LRU of 3: back-navigation within the cache does not refetch the session', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()

    await store.loadSession('a')
    await store.loadSession('b')
    await store.loadSession('c')
    expect(getSession).toHaveBeenCalledTimes(3)

    // Back-navigation to 'a' (still in the 3-slot cache) must not refetch.
    await store.loadSession('a')
    expect(getSession).toHaveBeenCalledTimes(3)
    expect(store.session).toEqual(sessionWithId('a'))
  })

  it('LRU of 3: loading a 4th session evicts the true least-recently-used entry, which then refetches', async () => {
    // Deviation from PLAN.md's literal walkthrough ("load A,B,C; back to A;
    // load D; A refetches"): that sequence describes FIFO-by-insertion
    // eviction, not "least-recently-*used*" as the ticket's own prose
    // requires. Under real LRU-by-use semantics, revisiting 'a' makes it
    // MRU, so 'b' — untouched since its initial load — is the one evicted
    // when 'd' is loaded. This test exercises that real semantics; the
    // eviction half (a fresh fetch on the next visit to the evicted id) is
    // still what makes it non-vacuous, per the ticket's own stated intent.
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()

    await store.loadSession('a')
    await store.loadSession('b')
    await store.loadSession('c')
    await store.loadSession('a') // 'a' becomes most-recently-used
    await store.loadSession('d') // cache is full: evicts the LRU entry, 'b'
    expect(getSession).toHaveBeenCalledTimes(4)

    await store.loadSession('a')
    expect(getSession).toHaveBeenCalledTimes(4) // still cached

    await store.loadSession('b')
    expect(getSession).toHaveBeenCalledTimes(5) // evicted -> refetched
  })
})
