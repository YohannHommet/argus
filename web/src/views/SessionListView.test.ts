import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SessionListView from './SessionListView.vue'
import { getFacets200Default, getMeta200Default, listSessions200Default } from '@/test/fixtures'
import { CAPTURE_READY_ATTR } from '@/composables/useCaptureReady'

let getSessions: ReturnType<typeof vi.fn>
let getMeta: ReturnType<typeof vi.fn>
let getFacets: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      if (path === '/api/v1/sessions') return getSessions(init)
      if (path === '/api/v1/meta') return getMeta()
      if (path === '/api/v1/facets') return getFacets()
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function emptySessionsPage() {
  return { data: [], page: { next_cursor: null, has_more: false } }
}

function neverResolves(): Promise<never> {
  return new Promise(() => {})
}

async function mountAt(path = '/sessions'): Promise<{ router: Router; wrapper: ReturnType<typeof mount> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/sessions', name: 'sessions', component: SessionListView },
      { path: '/sessions/:id', name: 'session-detail', component: { template: '<div/>' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(SessionListView, { global: { plugins: [router] } })
  await flushPromises()
  return { router, wrapper }
}

describe('SessionListView (PLAN.md P4-10)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.documentElement.removeAttribute(CAPTURE_READY_ATTR)
    getSessions = vi.fn(() => okResponse(listSessions200Default))
    getMeta = vi.fn(() => okResponse(getMeta200Default))
    getFacets = vi.fn(() => okResponse(getFacets200Default))
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows the skeleton table while the initial fetch is pending, and swaps to real content once it resolves', async () => {
    getSessions = vi.fn(neverResolves)
    const { wrapper } = await mountAt()

    expect(wrapper.find('[data-testid="skeleton-table"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="session-table"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="setup-card"]').exists()).toBe(false)
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).not.toBe('true')

    getSessions = vi.fn(() => okResponse(listSessions200Default))
    // The pending promise above never settles the store's own fetch, so resolve it by driving a
    // fresh one through the same store instance rather than waiting on the never-resolving call.
    const { useSessionsStore } = await import('@/stores/sessions')
    await useSessionsStore().refresh()
    await flushPromises()

    expect(wrapper.find('[data-testid="skeleton-table"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="session-table"]').exists()).toBe(true)
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  describe('a genuinely empty database (facets.projects === [])', () => {
    beforeEach(() => {
      getSessions = vi.fn(() => okResponse(emptySessionsPage()))
      getFacets = vi.fn(() => okResponse({ projects: [], models: [], vendors: [], tools: [], decision_sources: [], query_sources: [] }))
    })

    it('renders SetupCard, not a blank table (Phase-4 exit criterion 9)', async () => {
      const { wrapper } = await mountAt()

      expect(wrapper.find('[data-testid="setup-card"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="session-table"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(false)
    })

    it("the env block's endpoint is derived from meta's endpointUrl, not hardcoded (an AC)", async () => {
      vi.stubGlobal('location', { ...window.location, origin: 'https://argus.example.com' })
      const { wrapper } = await mountAt()

      const text = wrapper.get('[data-testid="setup-step-env"]').text()
      expect(text).toContain('https://argus.example.com')
      expect(text).not.toContain('http://localhost:8080')

      const hookText = wrapper.get('[data-testid="setup-step-hook"]').text()
      expect(hookText).toContain('https://argus.example.com/ingest/hook')

      vi.unstubAllGlobals()
    })

    it('the copied env block includes OTEL_LOG_TOOL_DETAILS=1 (an AC)', async () => {
      const { wrapper } = await mountAt()
      expect(wrapper.get('[data-testid="setup-step-env"]').text()).toContain('OTEL_LOG_TOOL_DETAILS=1')
    })

    it('the hook block keeps the SessionEnd timeout at 1, not 5', async () => {
      const { wrapper } = await mountAt()
      const hookText = wrapper.get('[data-testid="setup-step-hook"]').text()
      expect(hookText).toContain('"timeout": 1')
      // Both hooks point at the same ingest URL; only the SessionEnd one is timeout 1.
      expect(hookText).toContain('"timeout": 5')
    })

    it('the sim command is present and does not promise 25 sessions', async () => {
      const { wrapper } = await mountAt()
      const text = wrapper.get('[data-testid="setup-step-sim"]').text()
      expect(text).toContain('argusd sim --mode=demo --seed=42')
      expect(text).not.toContain('produces 25')
    })

    it('has a working copy button for each step', async () => {
      const writeText = vi.fn().mockResolvedValue(undefined)
      Object.assign(navigator, { clipboard: { writeText } })

      const { wrapper } = await mountAt()
      const copyButtons = wrapper.findAll('button').filter((b) => b.text().includes('Copy'))
      expect(copyButtons.length).toBeGreaterThanOrEqual(3)

      await copyButtons[0]!.trigger('click')
      await flushPromises()
      expect(writeText).toHaveBeenCalledTimes(1)
    })

    it('shows SetupCard even with an active filter — an empty DB has nothing to filter either way', async () => {
      const { wrapper } = await mountAt('/sessions?project=argus')
      expect(wrapper.find('[data-testid="setup-card"]').exists()).toBe(true)
    })
  })

  describe('these filters match nothing, but the database is not empty', () => {
    beforeEach(() => {
      getSessions = vi.fn(() => okResponse(emptySessionsPage()))
      // facets.projects is non-empty: the deployment has real data, just not under this filter.
      getFacets = vi.fn(() => okResponse(getFacets200Default))
    })

    it('renders EmptyState with a clear-filters action, not SetupCard', async () => {
      const { wrapper } = await mountAt('/sessions?project=argus')

      expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="setup-card"]').exists()).toBe(false)
      expect(wrapper.get('[data-testid="empty-state"]').text()).toContain('No sessions match these filters')

      const clearButton = wrapper.get('[data-testid="clear-filters"]')
      await clearButton.trigger('click')
      // setFilters debounces the refetch by FILTER_DEBOUNCE_MS (0) via a real setTimeout — a bare
      // flushPromises() (microtask-only) races it, so wait out an actual macrotask tick first.
      await new Promise((resolve) => setTimeout(resolve, 10))
      await flushPromises()

      const lastCall = getSessions.mock.calls.at(-1)![0] as { params: { query: { project: string[] } } }
      expect(lastCall.params.query.project).toEqual([])
    })
  })
})
