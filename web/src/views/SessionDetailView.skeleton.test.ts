// Kept separate from SessionDetailView.test.ts (owned by the phase lead, who wires this view's
// tabs concurrently with this ticket) so this ticket's PLAN.md P4-10 AC — "every view has a
// skeleton state asserted with a pending promise" — gets its own coverage without touching a file
// another agent has in flight. SessionDetailView.vue's skeleton branch (its own `<Skeleton>` trio,
// data-testid="session-detail-loading") already exists; this only asserts it.
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SessionDetailView from './SessionDetailView.vue'
import { resetEventSourceFactory, setEventSourceFactory } from '@/lib/sse'
import type { EventSourceLike } from '@/lib/sse'
import { getSession200Default } from '@/test/fixtures'

// PLAN.md P5-06: SessionDetailView now subscribes to its session's live stream on mount (before the
// session fetch even resolves), which would otherwise construct a real `EventSource` — unavailable in
// jsdom. This test only asserts skeleton/ready timing, so a trivial stub is enough.
function stubEventSource(): EventSourceLike {
  return { readyState: 0, addEventListener: () => {}, close: () => {}, onopen: null, onerror: null }
}

let getSession: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string) => {
      if (path === '/api/v1/sessions/{id}') return getSession()
      if (path === '/api/v1/sessions/{id}/subagents') return Promise.resolve({ data: { data: [], cost_attribution: null }, error: undefined, response: new Response(null, { status: 200 }) })
      if (path === '/api/v1/sessions/{id}/tool-calls') return Promise.resolve({ data: { data: [], page: { next_cursor: null, has_more: false } }, error: undefined, response: new Response(null, { status: 200 }) })
      throw new Error(`unexpected path ${path}`)
    },
  }),
}))

describe('SessionDetailView skeleton (PLAN.md P4-10)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    setEventSourceFactory(stubEventSource)
    document.documentElement.removeAttribute('data-capture-ready')
  })

  afterEach(() => {
    resetEventSourceFactory()
    vi.restoreAllMocks()
  })

  it('shows the skeleton while the session fetch is pending, and swaps to real content once it resolves', async () => {
    let resolveSession!: () => void
    getSession = vi.fn(
      () =>
        new Promise((resolve) => {
          resolveSession = () => resolve({ data: getSession200Default, error: undefined, response: new Response(null, { status: 200 }) })
        }),
    )

    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/sessions/:id', name: 'session-detail', component: SessionDetailView, props: true }],
    })
    await router.push('/sessions/3f7a3b1e-0000-0000-0000-000000000001')
    await router.isReady()
    const wrapper = mount(SessionDetailView, {
      props: { id: router.currentRoute.value.params.id as string },
      global: { plugins: [router] },
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="session-detail-loading"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="session-kpi-strip"]').exists()).toBe(false)
    expect(document.documentElement.getAttribute('data-capture-ready')).not.toBe('true')

    resolveSession()
    await flushPromises()

    expect(wrapper.find('[data-testid="session-detail-loading"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="session-kpi-strip"]').exists()).toBe(true)
    expect(document.documentElement.getAttribute('data-capture-ready')).toBe('true')
  })
})
