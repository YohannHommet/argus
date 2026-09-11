// PLAN.md P6-04 AC: a keyboard-only walk — session list -> open detail -> open an event (detail
// sheet) -> Esc closes it, focus returns to trigger — asserted end to end through real router
// navigation and real keydown events (no mouse `.trigger('click')`), so the assertion actually
// exercises the same path a keyboard-only user takes: SessionRow's own `@keydown.enter="activate"`
// (Enter opens the session), EventRow's own `@keydown.enter="openPrimary"` (Enter opens the sheet),
// and reka-ui's Dialog primitive under `EventDetailSheet.vue` (Escape closes it and restores focus
// to whatever had it before the sheet opened — that's reka-ui's own `FocusScope`/dismissable-layer
// behavior, not anything this app wires up itself; this test is what proves it actually holds).
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import { defineComponent, h } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SessionDetailView from '@/views/SessionDetailView.vue'
import SessionListView from '@/views/SessionListView.vue'
import { resetEventSourceFactory, setEventSourceFactory } from '@/lib/sse'
import type { EventSourceLike } from '@/lib/sse'
import {
  getFacets200Default,
  getMeta200Default,
  getSession200Default,
  getSessionSubagents200Default,
  getSessionTimeline200Default,
  listSessionToolCalls200Default,
  listSessionTurns200Default,
  listSessions200Default,
} from '@/test/fixtures'

function stubEventSource(): EventSourceLike {
  return { readyState: 0, addEventListener: () => {}, close: () => {}, onopen: null, onerror: null }
}

let getSessions: ReturnType<typeof vi.fn>
let getMeta: ReturnType<typeof vi.fn>
let getFacets: ReturnType<typeof vi.fn>
let getSessionDetail: ReturnType<typeof vi.fn>
let getTurns: ReturnType<typeof vi.fn>
let getTimeline: ReturnType<typeof vi.fn>
let getSubagents: ReturnType<typeof vi.fn>
let getSessionToolCalls: ReturnType<typeof vi.fn>

vi.mock('@/api/context', () => ({
  useApiClient: () => ({
    GET: (path: string, init: unknown) => {
      switch (path) {
        case '/api/v1/sessions':
          return getSessions(init)
        case '/api/v1/meta':
          return getMeta()
        case '/api/v1/facets':
          return getFacets()
        case '/api/v1/sessions/{id}':
          return getSessionDetail()
        case '/api/v1/sessions/{id}/turns':
          return getTurns()
        case '/api/v1/sessions/{id}/timeline':
          return getTimeline()
        case '/api/v1/sessions/{id}/subagents':
          return getSubagents()
        case '/api/v1/sessions/{id}/tool-calls':
          return getSessionToolCalls()
        default:
          throw new Error(`unexpected path ${path}`)
      }
    },
  }),
}))

function okResponse<T>(data: T) {
  return Promise.resolve({ data, error: undefined, response: new Response(null, { status: 200, headers: { 'Content-Length': '0' } }) })
}

function page<T>(data: T[]) {
  return { data, page: { next_cursor: null, has_more: false } }
}

/** Dispatches a real keydown so the row/element's own `@keydown.enter`/reka-ui's document-level Escape listener fires exactly as it would for a keyboard-only user (not `.trigger('click')`, which no keyboard-only path ever reaches). */
function pressKey(target: Element, key: string) {
  target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
}

describe('keyboard-only walk: session list -> detail -> event sheet -> Esc (PLAN.md P6-04 AC)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    setEventSourceFactory(stubEventSource)
    getSessions = vi.fn(() => okResponse(listSessions200Default))
    getMeta = vi.fn(() => okResponse(getMeta200Default))
    getFacets = vi.fn(() => okResponse(getFacets200Default))
    getSessionDetail = vi.fn(() => okResponse(getSession200Default))
    getTurns = vi.fn(() => okResponse(listSessionTurns200Default))
    getTimeline = vi.fn(() => okResponse(getSessionTimeline200Default))
    getSubagents = vi.fn(() => okResponse(getSessionSubagents200Default))
    getSessionToolCalls = vi.fn(() => okResponse(page(listSessionToolCalls200Default.data)))
  })

  afterEach(() => {
    resetEventSourceFactory()
    document.body.replaceChildren()
    document.documentElement.removeAttribute('data-capture-ready')
    vi.restoreAllMocks()
  })

  it('opens a session with Enter, opens an event with Enter, closes the sheet with Esc, and returns focus to the row that opened it', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/sessions', name: 'sessions', component: SessionListView },
        { path: '/sessions/:id', name: 'session-detail', component: SessionDetailView, props: true },
      ],
    })
    await router.push('/sessions')
    await router.isReady()

    const Root = defineComponent({ setup: () => () => h(RouterView) })
    const wrapper = mount(Root, { global: { plugins: [router] }, attachTo: document.body })
    await flushPromises()

    // Step 1: session list -> open detail, via Enter on the focused row (never a click).
    const sessionRow = wrapper.get('[data-testid="session-row"]').element as HTMLElement
    sessionRow.focus()
    expect(document.activeElement).toBe(sessionRow)
    pressKey(sessionRow, 'Enter')
    await flushPromises()

    expect(router.currentRoute.value.name).toBe('session-detail')
    expect(wrapper.find('[data-testid="timeline"]').exists()).toBe(true)

    // Step 2: open an event via Enter on the focused event row (the trigger focus must return to).
    const eventRow = wrapper.get('[data-testid="event-row"]').element as HTMLElement
    eventRow.focus()
    expect(document.activeElement).toBe(eventRow)
    pressKey(eventRow, 'Enter')
    await flushPromises()

    const sheet = document.body.querySelector('[data-testid="event-detail-sheet"]')
    expect(sheet).toBeTruthy()

    // Step 3: Esc closes the sheet and restores focus to the row that opened it — reka-ui's own
    // Dialog/FocusScope behavior (see EventDetailSheet.vue's doc comment), not app-level wiring.
    pressKey(document.body, 'Escape')
    await flushPromises()

    expect(document.body.querySelector('[data-testid="event-detail-sheet"]')).toBeFalsy()
    expect(document.activeElement).toBe(eventRow)
  })
})
