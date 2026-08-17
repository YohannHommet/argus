// Verifies PLAN.md P4-10's "global toast for API failures" for the one case wired in this ticket:
// a background auto-refresh failure has no retry button and no inline slot to surface in, so it
// goes to a toast instead — without touching the store's primary (foreground) load path, which
// still just sets `error` for whatever view's inline ErrorState reads it.
import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast: { error: toastError } }))

let getMeta: ReturnType<typeof vi.fn>
let getFacets: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string) => {
      if (path === '/api/v1/meta') return getMeta()
      if (path === '/api/v1/facets') return getFacets()
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function errorResponse() {
  const problem = { type: 'urn:argus:error:boom', title: 'Boom', status: 500 }
  return Promise.resolve({ data: undefined, error: problem, response: new Response(JSON.stringify(problem), { status: 500 }) })
}

describe('meta store background auto-refresh toast', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    toastError.mockClear()
    getMeta = vi.fn(() => okResponse({ version: '1', commit: 'a', retention_days: 90, feature_flags: {}, vendors: [], logs_exporter_seen: true, metrics_exporter_seen: true, hooks_seen: true, tool_details_seen: true, estimated_cost_present: false, data_quality: { logs_exporter_seen: true, metrics_exporter_seen: true, hooks_seen: true, tool_details_seen: true } }))
    getFacets = vi.fn(() => okResponse({ projects: ['argus'], models: [], vendors: [], tools: [], decision_sources: [], query_sources: [] }))
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('does not toast on a successful background refresh', async () => {
    const { useMetaStore, META_REFRESH_INTERVAL_MS } = await import('@/stores/meta')
    const store = useMetaStore()
    await store.load()

    vi.useFakeTimers()
    store.startAutoRefresh()
    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS)
    await flushPromises()

    expect(toastError).not.toHaveBeenCalled()
    store.stopAutoRefresh()
  })

  it('toasts when a background refresh fails, without touching the foreground load path', async () => {
    const { useMetaStore, META_REFRESH_INTERVAL_MS } = await import('@/stores/meta')
    const store = useMetaStore()
    await store.load()
    expect(toastError).not.toHaveBeenCalled()

    getMeta = vi.fn(errorResponse)
    vi.useFakeTimers()
    store.startAutoRefresh()
    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS)
    await flushPromises()

    expect(toastError).toHaveBeenCalledTimes(1)
    expect(toastError.mock.calls[0]![0]).toBe('Background refresh failed')
    store.stopAutoRefresh()
  })
})
