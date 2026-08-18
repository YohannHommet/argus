import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/errors'
import { getQualityHookLatency200Default, getQualityUnknownKinds200Default } from '@/test/fixtures'
import { emptyQualityHookLatency, emptyQualityUnknownKinds, multiRowQualityUnknownKinds } from '@/test/fixtures.extra'

let getUnknownKinds: ReturnType<typeof vi.fn>
let getHookLatency: ReturnType<typeof vi.fn>

// Mirrors meta.spec.ts's own approach: the store calls `useApiClient()` from
// `@/api/context`, mocked directly against a controllable fake client
// rather than exercised through real Vue injection.
vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, options?: { params?: { query?: Record<string, unknown> } }) => {
      if (path === '/api/v1/quality/unknown-kinds') return getUnknownKinds(options?.params?.query)
      if (path === '/api/v1/quality/hook-latency') return getHookLatency(options?.params?.query)
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

describe('useQualityStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getUnknownKinds = vi.fn(() => okResponse(getQualityUnknownKinds200Default))
    getHookLatency = vi.fn(() => okResponse(getQualityHookLatency200Default))
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Both `/quality/*` requests fire on store creation (each `useApi(...,
  // { immediate: true })`) — no explicit `load()` call is needed, or made,
  // in any of these tests, so call counts below stay meaningful.

  it('fetches unknown-kinds with since=-24h and hook-latency with no window, immediately on creation', async () => {
    const { useQualityStore } = await import('../quality')
    useQualityStore()
    await flushPromises()

    expect(getUnknownKinds).toHaveBeenCalledWith({ since: '-24h' })
    expect(getHookLatency).toHaveBeenCalled()
  })

  it('unknownEventsTotal sums every row count across multiple groups', async () => {
    getUnknownKinds = vi.fn(() => okResponse(multiRowQualityUnknownKinds))
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.unknownEventsTotal).toBe(41 + 3)
    expect(store.unknownKindRows).toHaveLength(2)
  })

  it('unknownEventsTotal is a measured 0 (not null) for an empty rows: [] response', async () => {
    getUnknownKinds = vi.fn(() => okResponse(emptyQualityUnknownKinds))
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.unknownEventsTotal).toBe(0)
    expect(store.unknownKindRows).toEqual([])
  })

  it('unknownEventsTotal is null before the first fetch resolves', () => {
    const importAndCreate = async () => {
      const { useQualityStore } = await import('../quality')
      return useQualityStore()
    }
    // A never-resolving fetch keeps the store in its pre-settlement state
    // for this assertion, deliberately, without ever awaiting it.
    getUnknownKinds = vi.fn(() => new Promise(() => {}))
    return importAndCreate().then((store) => {
      expect(store.unknownEventsTotal).toBeNull()
    })
  })

  it('hookLatencyRows reflects an empty response distinctly from a populated one', async () => {
    getHookLatency = vi.fn(() => okResponse(emptyQualityHookLatency))
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.hookLatencyRows).toEqual([])
  })

  it('hookLatencyRows returns the live-shaped fixture rows verbatim', async () => {
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.hookLatencyRows).toEqual(getQualityHookLatency200Default.rows)
  })

  it('settled is false while either request is still loading, true once both have resolved', async () => {
    let resolveUnknown!: (v: unknown) => void
    getUnknownKinds = vi.fn(() => new Promise((resolve) => (resolveUnknown = resolve)))
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.settled).toBe(false)
    resolveUnknown(await okResponse(getQualityUnknownKinds200Default))
    await flushPromises()

    expect(store.settled).toBe(true)
  })

  it('settled is true once both requests fail, not just once they succeed', async () => {
    getUnknownKinds = vi.fn(() => apiErrorResponse(500))
    getHookLatency = vi.fn(() => apiErrorResponse(500))
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.settled).toBe(true)
    expect(store.unknownKindsError).toBeInstanceOf(ApiError)
    expect(store.hookLatencyError).toBeInstanceOf(ApiError)
  })

  it('a failed unknown-kinds fetch leaves unknownEventsTotal null (not a fabricated 0)', async () => {
    getUnknownKinds = vi.fn(() => apiErrorResponse(500))
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()

    expect(store.unknownEventsTotal).toBeNull()
    expect(store.unknownKindsError).toBeInstanceOf(ApiError)
  })

  it('refetchUnknownKinds re-issues only the unknown-kinds request', async () => {
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()
    expect(getUnknownKinds).toHaveBeenCalledTimes(1)

    await store.refetchUnknownKinds()
    expect(getUnknownKinds).toHaveBeenCalledTimes(2)
    expect(getHookLatency).toHaveBeenCalledTimes(1)
  })

  it('refetchHookLatency re-issues only the hook-latency request', async () => {
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()
    expect(getHookLatency).toHaveBeenCalledTimes(1)

    await store.refetchHookLatency()
    expect(getHookLatency).toHaveBeenCalledTimes(2)
    expect(getUnknownKinds).toHaveBeenCalledTimes(1)
  })

  it('load() re-issues both requests', async () => {
    const { useQualityStore } = await import('../quality')
    const store = useQualityStore()
    await flushPromises()
    expect(getUnknownKinds).toHaveBeenCalledTimes(1)
    expect(getHookLatency).toHaveBeenCalledTimes(1)

    await store.load()
    expect(getUnknownKinds).toHaveBeenCalledTimes(2)
    expect(getHookLatency).toHaveBeenCalledTimes(2)
  })
})
