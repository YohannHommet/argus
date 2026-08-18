import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resetEventSourceFactory, setEventSourceFactory } from '@/lib/sse'
import type { EventSourceLike } from '@/lib/sse'
import { getFacets200Default, getMeta200Default } from '@/test/fixtures'
import LiveView from './LiveView.vue'

/** Minimal structural fake, same contract as `stores/__tests__/live.spec.ts`'s own — only what this file's tests need (`open`/`emit`), not the full reconnect-scenario surface that file drives. */
class FakeEventSource implements EventSourceLike {
  static readonly CONNECTING = 0
  static readonly OPEN = 1

  readyState = FakeEventSource.CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  private readonly listeners = new Map<string, ((ev: MessageEvent) => void)[]>()

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? []
    list.push(listener)
    this.listeners.set(type, list)
  }

  close(): void {}

  open(): void {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.(new Event('open'))
  }

  emit(type: string, data: unknown): void {
    const ev = new MessageEvent(type, { data: JSON.stringify(data) })
    for (const listener of this.listeners.get(type) ?? []) listener(ev)
  }
}

let instances: FakeEventSource[] = []
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

async function mountLiveView() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/live', name: 'live', component: LiveView },
      { path: '/sessions/:id', name: 'session-detail', component: { template: '<div/>' } },
    ],
  })
  await router.push('/live')
  await router.isReady()
  const wrapper = mount(LiveView, { global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

describe('LiveView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    instances = []
    setEventSourceFactory((): EventSourceLike => {
      const instance = new FakeEventSource()
      instances.push(instance)
      return instance
    })
    getMeta = vi.fn(() => okResponse(getMeta200Default))
    getFacets = vi.fn(() => okResponse(getFacets200Default))
  })

  afterEach(() => {
    resetEventSourceFactory()
    document.documentElement.removeAttribute('data-capture-ready')
    vi.restoreAllMocks()
  })

  it('subscribes to the firehose on mount and renders the health strip, active-session cards and feed', async () => {
    const wrapper = await mountLiveView()

    expect(instances).toHaveLength(1)
    expect(wrapper.find('[data-testid="health-strip"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="active-session-cards"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="live-feed"]').exists()).toBe(true)
  })

  it('does not flip data-capture-ready before the stream has opened and a frame has arrived', async () => {
    await mountLiveView()
    expect(document.documentElement.getAttribute('data-capture-ready')).not.toBe('true')

    instances[0]!.open()
    await flushPromises()
    // Connected, but not one single frame has landed yet — still not photographable.
    expect(document.documentElement.getAttribute('data-capture-ready')).not.toBe('true')
  })

  it('flips data-capture-ready once the stream is open and an event frame has arrived', async () => {
    await mountLiveView()
    instances[0]!.open()
    instances[0]!.emit('event', {
      event_ref: 'ref-1',
      seq: 1,
      id: '0192abcd-0000-0000-0000-000000000001',
      ts: '2026-08-18T00:00:00.000Z',
      session_id: 'sess-1',
      prompt_id: null,
      kind: 'tool.pre',
      event_name: 'tool_pre',
      source: 'otel_log',
      vendor: 'claude_code',
      tool_name: 'Bash',
      tool_use_id: null,
      decision: null,
      decision_source: null,
      tool_source: null,
      query_source: null,
      model: null,
      tokens: null,
      cost: null,
      duration_ms: null,
      success: null,
      error_type: null,
      agent_id: null,
      agent_type: null,
      permission_mode: null,
      file_path: null,
      clock_skewed: false,
    })
    await flushPromises()

    expect(document.documentElement.getAttribute('data-capture-ready')).toBe('true')
  })

  it('flips data-capture-ready once the stream is open and a stats frame has arrived, even with zero events', async () => {
    await mountLiveView()
    instances[0]!.open()
    instances[0]!.emit('stats', { events_per_sec: 0, active_sessions: 0, queue_depth: 0, ingest_lag_ms: 0, dropped_total: 0 })
    await flushPromises()

    expect(document.documentElement.getAttribute('data-capture-ready')).toBe('true')
  })

  it('unsubscribes from the firehose on unmount', async () => {
    const wrapper = await mountLiveView()
    expect(instances).toHaveLength(1)

    wrapper.unmount()

    // The store's own reconcileConnection tears the EventSource down once the subscription stack
    // empties — asserted indirectly here since LiveView owns the only subscriber in this test.
    const { useLiveStore } = await import('@/stores/live')
    expect(useLiveStore().status).toBe('closed')
  })
})
