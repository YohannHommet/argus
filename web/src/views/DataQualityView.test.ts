import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CAPTURE_READY_ATTR } from '@/composables/useCaptureReady'
import { getMeta200Default, getQualityHookLatency200Default, getQualityUnknownKinds200Default } from '@/test/fixtures'
import { emptyQualityHookLatency, emptyQualityUnknownKinds } from '@/test/fixtures.extra'
import DataQualityView from './DataQualityView.vue'

let getMeta: ReturnType<typeof vi.fn>
let getFacets: ReturnType<typeof vi.fn>
let getUnknownKinds: ReturnType<typeof vi.fn>
let getHookLatency: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string) => {
      if (path === '/api/v1/meta') return getMeta()
      if (path === '/api/v1/facets') return getFacets()
      if (path === '/api/v1/quality/unknown-kinds') return getUnknownKinds()
      if (path === '/api/v1/quality/hook-latency') return getHookLatency()
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

describe('DataQualityView (Phase-4 exit criterion 7)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getMeta = vi.fn(() => okResponse(getMeta200Default))
    getFacets = vi.fn(() => okResponse({ projects: ['argus'], models: [], vendors: [], tools: [], decision_sources: [], query_sources: [] }))
    getUnknownKinds = vi.fn(() => okResponse(getQualityUnknownKinds200Default))
    getHookLatency = vi.fn(() => okResponse(getQualityHookLatency200Default))
  })

  afterEach(() => {
    document.documentElement.removeAttribute(CAPTURE_READY_ATTR)
    vi.restoreAllMocks()
  })

  it('renders the quality tiles, the unknown-kind table, and the hook-latency panel', async () => {
    const wrapper = mount(DataQualityView)
    await flushPromises()

    expect(wrapper.find('[data-testid="quality-tiles"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="unknown-kind-table"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="hook-latency-panel"]').exists()).toBe(true)
  })

  it('wires the real unknown-kinds total into the tiles and the table', async () => {
    const wrapper = mount(DataQualityView)
    await flushPromises()

    expect(wrapper.get('[data-testid="quality-tile-unknown-events-value"]').text()).toBe('41')
    expect(wrapper.get('[data-testid="unknown-kind-count"]').text()).toBe('41')
  })

  it('wires the real hook-latency rows into the panel', async () => {
    const wrapper = mount(DataQualityView)
    await flushPromises()

    expect(wrapper.get('[data-testid="hook-latency-executions"]').text()).toBe('412')
  })

  it('marks the document ready for capture once meta and both quality fetches have settled', async () => {
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).not.toBe('true')

    mount(DataQualityView)
    await flushPromises()

    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  it('is capture-ready on the clean-data empty path too (empty unknown-kinds and hook-latency)', async () => {
    getUnknownKinds = vi.fn(() => okResponse(emptyQualityUnknownKinds))
    getHookLatency = vi.fn(() => okResponse(emptyQualityHookLatency))

    const wrapper = mount(DataQualityView)
    await flushPromises()

    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
    expect(wrapper.get('[data-testid="quality-tile-unknown-events-value"]').text()).toBe('0')
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('shows a skeleton (not the tile value) while unknown-kinds is pending, and swaps once it resolves (PLAN.md P4-10)', async () => {
    let resolveUnknownKinds!: () => void
    getUnknownKinds = vi.fn(
      () =>
        new Promise((resolve) => {
          resolveUnknownKinds = () => resolve({ data: getQualityUnknownKinds200Default, error: undefined, response: new Response(null, { status: 200 }) })
        }),
    )
    const wrapper = mount(DataQualityView)
    await flushPromises()

    expect(wrapper.find('[data-testid="quality-tile-unknown-events-value"]').exists()).toBe(false)
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).not.toBe('true')

    resolveUnknownKinds()
    await flushPromises()

    expect(wrapper.get('[data-testid="quality-tile-unknown-events-value"]').text()).toBe('41')
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  it('is capture-ready even when a quality endpoint errors (error is a legitimate first paint)', async () => {
    getHookLatency = vi.fn(() =>
      Promise.resolve({
        data: undefined,
        error: { type: 'urn:argus:error:boom', title: 'Boom', status: 500 },
        response: new Response(null, { status: 500 }),
      }),
    )

    const wrapper = mount(DataQualityView)
    await flushPromises()

    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
    expect(wrapper.find('[data-testid="hook-latency-panel"] [data-testid="error-state"]').exists()).toBe(true)
  })
})
