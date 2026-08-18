import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/errors'
import { resetEventSourceFactory, setEventSourceFactory } from '@/lib/sse'
import type { EventSourceLike } from '@/lib/sse'
import {
  getEvent200Default,
  getSession200Default,
  getSessionSubagents200Default,
  getSessionTimeline200Default,
  listSessionToolCalls200Default,
  listSessionTurns200Default,
} from '@/test/fixtures'
import { makeTimelineEvent } from '@/test/fixtures.extra'

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

/** Structural fake for `EventSourceLike`, same pattern as `stores/__tests__/live.spec.ts` — never touches the network, lets a test drive frames by hand. */
class FakeEventSource implements EventSourceLike {
  readyState = 0
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  closed = false
  private readonly listeners = new Map<string, ((ev: MessageEvent) => void)[]>()

  constructor(public readonly url: string) {}

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? []
    list.push(listener)
    this.listeners.set(type, list)
  }

  close(): void {
    this.closed = true
    this.readyState = 2
  }

  emit(type: string, data: unknown): void {
    const ev = new MessageEvent(type, { data: JSON.stringify(data) })
    for (const listener of this.listeners.get(type) ?? []) listener(ev)
  }
}

let liveInstances: FakeEventSource[] = []

function fakeEventSourceFactory(url: string): EventSourceLike {
  const instance = new FakeEventSource(url)
  liveInstances.push(instance)
  return instance
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

  // P5-05 integration gap: on `/live` there is no current session (a firehose
  // has none), and loadEvent used to `return null` in that case — so clicking a
  // live-feed row opened the detail sheet with the correct event_ref and
  // permanently blank content. GET /events/{ref} is addressed by event_ref
  // alone (SPEC §4.1: "there is no lookup by id"), so the session was never
  // required; only the choice of cache slot was.
  it('loadEvent works with no session open at all (the /live firehose) and caches by event_ref', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()

    expect(store.currentId).toBeNull()

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

describe('useSessionDetailStore — live (PLAN.md P5-06)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    liveInstances = []
    setEventSourceFactory(fakeEventSourceFactory)
    getSession = vi.fn((id: string) => okResponse(sessionWithId(id)))
    getTimeline = vi.fn(() => okResponse({ data: [], page: { next_cursor: null, has_more: false } }))
    getTurns = vi.fn(() => okResponse(listSessionTurns200Default))
    getToolCalls = vi.fn(() => okResponse(listSessionToolCalls200Default))
    getSubagents = vi.fn(() => okResponse(getSessionSubagents200Default))
    getEvent = vi.fn(() => okResponse(getEvent200Default))
  })

  afterEach(() => {
    resetEventSourceFactory()
    vi.restoreAllMocks()
  })

  it('a live event for the open session appends exactly once, and a later REST page redelivering the same event_ref does not duplicate it (live-then-REST)', async () => {
    getTimeline = vi
      .fn()
      .mockReturnValueOnce(okResponse({ data: [], page: { next_cursor: 'c1', has_more: true } }))
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    await store.loadTimeline()
    store.startLive('s1')

    const event = makeTimelineEvent({ session_id: 's1', event_ref: 'dup-ref', ts: '2026-08-14T01:00:00.000Z' })
    liveInstances[0]!.emit('event', event)
    // The events watcher is a default (pre-flush) Vue watcher, not `flush: 'sync'` — its callback is
    // scheduled, not run synchronously inside `emit()`, so a real reactive tick has to be awaited.
    await flushPromises()
    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['dup-ref'])

    // A later "load more" page re-delivers the very ref the live frame already appended.
    getTimeline.mockReturnValueOnce(okResponse({ data: [event], page: { next_cursor: null, has_more: false } }))
    await store.loadMoreTimeline()

    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['dup-ref'])
  })

  it('a REST page delivers an event_ref, and the same event later arriving live does not duplicate it (REST-then-live)', async () => {
    const event = makeTimelineEvent({ session_id: 's1', event_ref: 'dup-ref', ts: '2026-08-14T01:00:00.000Z' })
    getTimeline = vi
      .fn()
      .mockReturnValueOnce(okResponse({ data: [], page: { next_cursor: 'c1', has_more: true } }))
      .mockReturnValueOnce(okResponse({ data: [event], page: { next_cursor: null, has_more: false } }))
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    await store.loadTimeline()
    await store.loadMoreTimeline()
    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['dup-ref'])

    store.startLive('s1')
    liveInstances[0]!.emit('event', event)
    await flushPromises()

    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['dup-ref'])
  })

  it('ignores a live event for a different session', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    await store.loadTimeline()
    store.startLive('s1')

    liveInstances[0]!.emit('event', makeTimelineEvent({ session_id: 'some-other-session', event_ref: 'ref-x' }))
    await flushPromises()

    expect(store.timelineItems).toHaveLength(0)
  })

  it('a live session frame updates the KPI counters (merged into the SessionDetail, not replacing it)', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    store.startLive('s1')

    liveInstances[0]!.emit('session', { ...sessionWithId('s1'), event_count: 999, tool_call_count: 42 })
    await flushPromises()

    expect(store.session?.event_count).toBe(999)
    expect(store.session?.tool_call_count).toBe(42)
    // Detail-only fields (absent from the `SessionSummary`-shaped frame) survive the merge — this is
    // exactly what lets SessionKpiStrip.vue need no change of its own.
    expect(store.session?.raw_events_expired).toBe(getSession200Default.raw_events_expired)
  })

  it('toggling live off stops appending, but loadMoreTimeline still works and already-loaded rows stay', async () => {
    getTimeline = vi
      .fn()
      .mockReturnValueOnce(okResponse({ data: [makeTimelineEvent({ session_id: 's1', event_ref: 'r1' })], page: { next_cursor: 'c1', has_more: true } }))
      .mockReturnValueOnce(okResponse({ data: [makeTimelineEvent({ session_id: 's1', event_ref: 'r2' })], page: { next_cursor: null, has_more: false } }))
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    await store.loadTimeline()
    store.startLive('s1')
    store.setLiveEnabled(false)

    liveInstances[0]!.emit('event', makeTimelineEvent({ session_id: 's1', event_ref: 'live-ref' }))
    await flushPromises()
    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['r1'])

    await store.loadMoreTimeline()
    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['r1', 'r2'])
  })

  it('an out-of-order live event is inserted at the correct position for the active order, not merely appended', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    await store.loadTimeline()
    store.startLive('s1')

    const early = makeTimelineEvent({ session_id: 's1', event_ref: 'early', ts: '2026-08-14T01:00:00.000Z', seq: 100 })
    const late = makeTimelineEvent({ session_id: 's1', event_ref: 'late', ts: '2026-08-14T01:00:10.000Z', seq: 200 })

    // Arrives out of chronological order: "late" first, "early" second.
    liveInstances[0]!.emit('event', late)
    await flushPromises()
    liveInstances[0]!.emit('event', early)
    await flushPromises()

    // `order` defaults to 'asc' — a naive push would leave this as ['late', 'early'].
    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['early', 'late'])
  })

  it('a live event whose kind is excluded by the active kinds filter does not appear', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    store.setTimelineFilters({ kinds: ['tool.result'] })
    await store.loadSession('s1')
    await store.loadTimeline()
    store.startLive('s1')

    liveInstances[0]!.emit('event', makeTimelineEvent({ session_id: 's1', kind: 'llm.request', tool_use_id: null, event_ref: 'filtered-out' }))
    await flushPromises()
    expect(store.timelineItems).toHaveLength(0)

    liveInstances[0]!.emit('event', makeTimelineEvent({ session_id: 's1', kind: 'tool.result', event_ref: 'kept' }))
    await flushPromises()
    expect(store.timelineItems.map((e) => e.event_ref)).toEqual(['kept'])
  })

  it('a reset frame triggers exactly one loadTimeline({reset:true}) refetch', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')
    await store.loadTimeline()
    store.startLive('s1')
    getTimeline.mockClear()

    liveInstances[0]!.emit('reset', { reason: 'replay_window_exceeded', from: '2026-08-18T00:00:00.000Z' })
    await flushPromises()

    expect(getTimeline).toHaveBeenCalledTimes(1)
  })
})

describe('sessionDetailStore — non-ApiError failures still surface as store errors', () => {
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

  // A transport-level failure (fetch itself throwing: offline, DNS, CORS) is not
  // an ApiError, so it takes the other half of every `err instanceof ApiError`
  // branch. It must still land in the store's error slot — an unhandled
  // rejection here would leave the tab spinning on `loading` forever with
  // nothing on screen to say why.
  it('loadToolCalls records a thrown non-ApiError and clears loading', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')

    getToolCalls = vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))
    await store.loadToolCalls({ force: true })

    expect(store.toolCallsError).toBeInstanceOf(Error)
    expect(store.toolCallsError?.message).toContain('Failed to fetch')
    expect(store.toolCallsLoading).toBe(false)
  })

  it('loadSubagents records a thrown non-ApiError and clears loading', async () => {
    const { useSessionDetailStore } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    await store.loadSession('s1')

    getSubagents = vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))
    await store.loadSubagents({ force: true })

    expect(store.subagentsError).toBeInstanceOf(Error)
    expect(store.subagentsError?.message).toContain('Failed to fetch')
    expect(store.subagentsLoading).toBe(false)
  })
})

describe('sessionDetailStore — sessionless event cache bound', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // The /live path has no current session, so its loadEvent calls land in a
  // separate, bounded cache (a firehose has no natural limit on how many
  // distinct events a user can click through, unlike one session's timeline).
  // This pins the eviction actually happening: without it the map grows for the
  // lifetime of the tab, and with an off-by-one it would evict the entry it
  // just inserted.
  it('evicts the oldest entry past the cap and keeps the newest, refetching only the evicted ref', async () => {
    const { useSessionDetailStore, ORPHAN_EVENT_CACHE_MAX } = await import('../sessionDetail')
    const store = useSessionDetailStore()
    expect(store.currentId).toBeNull()

    getEvent = vi.fn((ref: string) => okResponse({ ...getEvent200Default, event_ref: ref }))

    const refs = Array.from({ length: ORPHAN_EVENT_CACHE_MAX + 1 }, (_, i) => `ref-${i}`)
    for (const ref of refs) await store.loadEvent(ref)
    expect(getEvent).toHaveBeenCalledTimes(refs.length)

    // The newest is still cached — no refetch.
    await store.loadEvent(refs[refs.length - 1]!)
    expect(getEvent).toHaveBeenCalledTimes(refs.length)

    // The oldest was evicted — one more fetch.
    await store.loadEvent(refs[0]!)
    expect(getEvent).toHaveBeenCalledTimes(refs.length + 1)
  })
})
