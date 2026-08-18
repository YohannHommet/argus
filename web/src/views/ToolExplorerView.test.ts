import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ToolExplorerView from './ToolExplorerView.vue'
import { listToolCalls200Default } from '@/test/fixtures'
import { CAPTURE_READY_ATTR } from '@/composables/useCaptureReady'

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

function page(data: typeof listToolCalls200Default.data, opts: { next_cursor?: string | null; has_more?: boolean } = {}) {
  return { data, page: { next_cursor: opts.next_cursor ?? null, has_more: opts.has_more ?? false } }
}

async function mountAt(path: string): Promise<{ router: Router; wrapper: ReturnType<typeof mount> }> {
  setActivePinia(createPinia())
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/tools', name: 'tools', component: ToolExplorerView },
      { path: '/sessions/:id', name: 'session-detail', component: { template: '<div/>' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(ToolExplorerView, { global: { plugins: [router] } })
  await flushPromises()
  return { router, wrapper }
}

describe('ToolExplorerView', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute(CAPTURE_READY_ATTR)
    getToolCalls = vi.fn(() => okResponse(page(listToolCalls200Default.data)))
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('sets data-capture-ready on <html> once the first fetch settles', async () => {
    await mountAt('/tools')
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  it('renders the loaded tool calls in a ToolCallTable with a session column', async () => {
    const { wrapper } = await mountAt('/tools')
    expect(wrapper.find('[data-testid="tool-call-table"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="tool-call-session-link"]').exists()).toBe(true)
  })

  describe('Phase-4 exit criterion 5: reachable from the analytics decision matrix with the filter applied', () => {
    it('mounting at /tools?decision_source=user_reject sends decision_source to the API and shows the active filter chip', async () => {
      const { wrapper } = await mountAt('/tools?decision_source=user_reject')

      const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
      expect(callArgs.params.query.decision_source).toEqual(['user_reject'])

      const chip = wrapper.get('[data-testid="filter-chip-decision-source"]')
      expect(chip.text()).toContain('user_reject')
    })

    it('mounting at /tools?tool_name=Edit&decision_source=user_reject applies both (DecisionMatrix.vue emits both)', async () => {
      const { wrapper } = await mountAt('/tools?tool_name=Edit&decision_source=user_reject')

      const callArgs = getToolCalls.mock.calls[0]![0] as { params: { query: Record<string, unknown> } }
      expect(callArgs.params.query.tool).toEqual(['Edit'])
      expect(callArgs.params.query.decision_source).toEqual(['user_reject'])

      expect(wrapper.get('[data-testid="filter-chip-tool"]').text()).toContain('Edit')
      expect(wrapper.get('[data-testid="filter-chip-decision-source"]').text()).toContain('user_reject')
    })

    it('no active-filter chips render when there is no incoming filter', async () => {
      const { wrapper } = await mountAt('/tools')
      expect(wrapper.find('[data-testid="active-filters"]').exists()).toBe(false)
    })
  })

  it('clicking a row navigates to that row\'s session detail', async () => {
    const { router, wrapper } = await mountAt('/tools')
    await wrapper.get('[data-testid="tool-call-row"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.name).toBe('session-detail')
    expect(router.currentRoute.value.params.id).toBe(listToolCalls200Default.data[0]!.session_id)
  })

  it('shows a skeleton (not the table) while the initial fetch is pending, and swaps to real content once it resolves (PLAN.md P4-10)', async () => {
    let resolveToolCalls!: () => void
    getToolCalls = vi.fn(
      () =>
        new Promise((resolve) => {
          resolveToolCalls = () => resolve({ data: page(listToolCalls200Default.data), error: undefined, response: new Response(null, { status: 200 }) })
        }),
    )
    const { wrapper } = await mountAt('/tools')

    expect(wrapper.find('[role="table"], table').exists()).toBe(false)
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).not.toBe('true')

    resolveToolCalls()
    await flushPromises()

    expect(wrapper.find('table').exists()).toBe(true)
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  describe('round-6 UI pass: client-side tool-name search over the loaded page', () => {
    const rowA = listToolCalls200Default.data[0]!
    const rowB = { ...rowA, id: 'search-fixture-row-b', tool_name: 'Bash' }
    const twoTools = [rowA, rowB]

    it('typing a substring of one tool name filters the table to matching rows only', async () => {
      getToolCalls = vi.fn(() => okResponse(page(twoTools)))
      const { wrapper } = await mountAt('/tools')
      expect(wrapper.findAll('[data-testid="tool-call-row"]')).toHaveLength(2)

      await wrapper.get('[data-testid="tools-search"]').setValue('bash')
      await flushPromises()

      const rows = wrapper.findAll('[data-testid="tool-call-row"]')
      expect(rows).toHaveLength(1)
      expect(rows[0]!.text()).toContain('Bash')
    })

    it('does not refetch from the API — filtering is client-side over the already-loaded page', async () => {
      getToolCalls = vi.fn(() => okResponse(page(twoTools)))
      const { wrapper } = await mountAt('/tools')
      const callsBefore = getToolCalls.mock.calls.length

      await wrapper.get('[data-testid="tools-search"]').setValue('bash')
      await flushPromises()

      expect(getToolCalls.mock.calls.length).toBe(callsBefore)
    })

    it('clearing the search restores every loaded row', async () => {
      getToolCalls = vi.fn(() => okResponse(page(twoTools)))
      const { wrapper } = await mountAt('/tools')

      await wrapper.get('[data-testid="tools-search"]').setValue('bash')
      await flushPromises()
      await wrapper.get('[data-testid="tools-search"]').setValue('')
      await flushPromises()

      expect(wrapper.findAll('[data-testid="tool-call-row"]')).toHaveLength(2)
    })
  })

  it('an error response shows ErrorState and data-capture-ready still becomes true', async () => {
    getToolCalls = vi.fn(() =>
      Promise.resolve({
        data: undefined,
        error: { type: 'urn:argus:error:boom', title: 'Boom', status: 500 },
        response: new Response(null, { status: 500 }),
      }),
    )
    await mountAt('/tools')
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })
})
