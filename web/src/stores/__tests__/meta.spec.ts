import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/errors'
import { getFacets200Default, getMeta200Default } from '@/test/fixtures'

// The OpenAPI-generated fixtures are the real spec examples — using them
// here (rather than hand-rolling a Meta/Facets literal) is exactly the
// point of P4-01's fixtures.ts: this test breaks if the two ever drift
// out of sync with the contract handlers must satisfy.
const metaExample = getMeta200Default
const facetsExample = getFacets200Default

// The store calls `useApiClient()` from `@/api/context` — mocked directly
// rather than exercised through real Vue injection, since injection
// context (hasInjectionContext()) isn't active once an async store action
// runs past its first `await` anyway. `client.test.ts`/`context.ts`'s own
// wiring is covered elsewhere; this test is about `meta.ts`'s fetch/merge/
// refresh logic against a controllable fake client.
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

function apiErrorResponse(status: number) {
  const problem = { type: 'urn:argus:error:boom', title: 'Boom', status }
  return Promise.resolve({
    data: undefined,
    error: problem,
    response: new Response(JSON.stringify(problem), { status, headers: { 'Content-Type': 'application/problem+json' } }),
  })
}

describe('useMetaStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getMeta = vi.fn(() => okResponse(metaExample))
    getFacets = vi.fn(() => okResponse(facetsExample))
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('fetches meta and facets in parallel and populates both', async () => {
    const { useMetaStore } = await import('../meta')
    const store = useMetaStore()

    await store.load()

    expect(store.meta).toEqual(metaExample)
    expect(store.facets).toEqual(facetsExample)
    expect(store.projects).toEqual(['argus', 'platform'])
    expect(store.error).toBeNull()
  })

  it('leaves the other endpoint populated when one of the two fails', async () => {
    getFacets = vi.fn(() => apiErrorResponse(500))
    const { useMetaStore } = await import('../meta')
    const store = useMetaStore()

    await store.load()

    expect(store.meta).toEqual(metaExample)
    expect(store.facets).toBeNull()
    expect(store.error).toBeInstanceOf(ApiError)
  })

  it('hasNoData is true once loaded with zero projects, false while unloaded', async () => {
    getFacets = vi.fn(() => okResponse({ ...facetsExample, projects: [] }))
    const { useMetaStore } = await import('../meta')
    const store = useMetaStore()

    expect(store.hasNoData).toBe(false)
    await store.load()
    expect(store.hasNoData).toBe(true)
  })

  it('skips a refetch within the refresh window unless forced', async () => {
    const { useMetaStore } = await import('../meta')
    const store = useMetaStore()

    await store.load()
    await store.load()
    expect(getMeta).toHaveBeenCalledTimes(1)

    await store.load({ force: true })
    expect(getMeta).toHaveBeenCalledTimes(2)
  })

  it('startAutoRefresh fires load() every 5 minutes, stopAutoRefresh clears it', async () => {
    const { useMetaStore, META_REFRESH_INTERVAL_MS } = await import('../meta')
    const store = useMetaStore()

    await store.load()
    expect(getMeta).toHaveBeenCalledTimes(1)

    store.startAutoRefresh()
    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS)
    expect(getMeta).toHaveBeenCalledTimes(2)

    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS)
    expect(getMeta).toHaveBeenCalledTimes(3)

    store.stopAutoRefresh()
    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS * 2)
    expect(getMeta).toHaveBeenCalledTimes(3)
  })

  it('reset() clears state and stops auto-refresh', async () => {
    const { useMetaStore, META_REFRESH_INTERVAL_MS } = await import('../meta')
    const store = useMetaStore()

    await store.load()
    store.startAutoRefresh()
    store.reset()

    expect(store.meta).toBeNull()
    expect(store.facets).toBeNull()
    expect(store.lastFetchedAt).toBeNull()

    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS)
    expect(getMeta).toHaveBeenCalledTimes(1)
  })

  describe('getters on a freshly-created, unloaded store', () => {
    it('every facet-derived list getter is empty', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      expect(store.projects).toEqual([])
      expect(store.models).toEqual([])
      expect(store.vendors).toEqual([])
      expect(store.tools).toEqual([])
      expect(store.decisionSources).toEqual([])
      expect(store.querySources).toEqual([])
    })

    it('hasNoData is false — "unknown" (null facets/meta) is not "empty"', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      // hasNoData's whole point (see meta.ts's own doc comment) is that a
      // pre-boot-fetch screen must not flash an empty state: null means
      // "we don't know yet", not "there is nothing".
      expect(store.meta).toBeNull()
      expect(store.facets).toBeNull()
      expect(store.hasNoData).toBe(false)
    })

    it('every data-quality boolean getter defaults to false', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      expect(store.logsExporterSeen).toBe(false)
      expect(store.metricsExporterSeen).toBe(false)
      expect(store.hooksSeen).toBe(false)
      expect(store.toolDetailsSeen).toBe(false)
    })

    it('metricsOnlyProjects is always [] (not sourced from this store)', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      expect(store.metricsOnlyProjects).toEqual([])
    })

    it('endpointUrl reads the browser origin', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      expect(store.endpointUrl).toBe(window.location.origin)
    })
  })

  describe('getters after a successful load()', () => {
    it('every facet-derived list getter returns the real fixture values', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(store.projects).toEqual(facetsExample.projects)
      expect(store.models).toEqual(facetsExample.models)
      expect(store.vendors).toEqual(facetsExample.vendors)
      expect(store.tools).toEqual(facetsExample.tools)
      expect(store.decisionSources).toEqual(facetsExample.decision_sources)
      expect(store.querySources).toEqual(facetsExample.query_sources)
    })

    it('every data-quality boolean getter reflects the meta fixture', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(store.logsExporterSeen).toBe(metaExample.data_quality.logs_exporter_seen)
      expect(store.metricsExporterSeen).toBe(metaExample.data_quality.metrics_exporter_seen)
      expect(store.hooksSeen).toBe(metaExample.data_quality.hooks_seen)
      expect(store.toolDetailsSeen).toBe(metaExample.data_quality.tool_details_seen)
    })
  })

  describe('hasNoData', () => {
    it('is true once loaded with an empty projects facet', async () => {
      getFacets = vi.fn(() => okResponse({ ...facetsExample, projects: [] }))
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(store.hasNoData).toBe(true)
    })

    it('is false once loaded with a non-empty projects facet', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(facetsExample.projects.length).toBeGreaterThan(0)
      expect(store.hasNoData).toBe(false)
    })
  })

  describe('load({force}) freshness short-circuit', () => {
    it('a second load() inside the freshness window issues no new requests', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()
      expect(getMeta).toHaveBeenCalledTimes(1)
      expect(getFacets).toHaveBeenCalledTimes(1)

      await store.load()
      expect(getMeta).toHaveBeenCalledTimes(1)
      expect(getFacets).toHaveBeenCalledTimes(1)
    })

    it('load({force: true}) inside the freshness window issues new requests', async () => {
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()
      await store.load({ force: true })

      expect(getMeta).toHaveBeenCalledTimes(2)
      expect(getFacets).toHaveBeenCalledTimes(2)
    })
  })

  it('reset() stops a running auto-refresh timer: advancing past the interval issues no request', async () => {
    const { useMetaStore, META_REFRESH_INTERVAL_MS } = await import('../meta')
    const store = useMetaStore()

    await store.load()
    store.startAutoRefresh()
    store.reset()

    await vi.advanceTimersByTimeAsync(META_REFRESH_INTERVAL_MS * 3)

    expect(getMeta).toHaveBeenCalledTimes(1)
    expect(getFacets).toHaveBeenCalledTimes(1)
  })

  describe('non-Error rejection reasons', () => {
    it('wraps a string rejection from the meta request in an Error', async () => {
      getMeta = vi.fn(() => Promise.reject('boom-meta'))
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(store.error).toBeInstanceOf(Error)
      expect(store.error).not.toBeInstanceOf(ApiError)
      expect(store.error?.message).toBe('boom-meta')
    })

    it('keeps a real Error rejection from the meta request as-is (no re-wrap)', async () => {
      getMeta = vi.fn(() => apiErrorResponse(500))
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(store.error).toBeInstanceOf(ApiError)
    })

    it('wraps a string rejection from the facets request in an Error', async () => {
      getFacets = vi.fn(() => Promise.reject('boom-facets'))
      const { useMetaStore } = await import('../meta')
      const store = useMetaStore()

      await store.load()

      expect(store.error).toBeInstanceOf(Error)
      expect(store.error).not.toBeInstanceOf(ApiError)
      expect(store.error?.message).toBe('boom-facets')
    })
  })
})
