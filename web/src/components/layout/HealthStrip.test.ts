import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import HealthStrip from './HealthStrip.vue'

const BASE_PROPS = {
  stats: null,
  clientDroppedTotal: 0,
  status: 'open' as const,
  logsExporterSeen: true,
  metricsExporterSeen: true,
  hooksSeen: true,
  toolDetailsSeen: false,
}

describe('HealthStrip', () => {
  it('renders — (not 0) for queue depth, ingest lag and server-dropped before the first stats frame arrives', () => {
    const wrapper = mount(HealthStrip, { props: { ...BASE_PROPS, stats: null } })

    expect(wrapper.get('[data-testid="health-strip-queue-depth-value"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="health-strip-ingest-lag-value"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="health-strip-dropped-server-value"]').text()).toBe('—')
  })

  it('renders real numbers, never a dash, once a stats frame has arrived — including a measured 0', () => {
    const wrapper = mount(HealthStrip, {
      props: { ...BASE_PROPS, stats: { events_per_sec: 3, active_sessions: 1, queue_depth: 0, ingest_lag_ms: 120, dropped_total: 0 } },
    })

    expect(wrapper.get('[data-testid="health-strip-queue-depth-value"]').text()).toBe('0')
    expect(wrapper.get('[data-testid="health-strip-ingest-lag-value"]').text()).toBe('120ms')
    expect(wrapper.get('[data-testid="health-strip-dropped-server-value"]').text()).toBe('0')
  })

  it('a non-zero server dropped_total renders the warn class, with a tooltip naming the server as the source of the loss', () => {
    const wrapper = mount(HealthStrip, {
      props: { ...BASE_PROPS, stats: { events_per_sec: 3, active_sessions: 1, queue_depth: 0, ingest_lag_ms: 5, dropped_total: 12 } },
    })

    const value = wrapper.get('[data-testid="health-strip-dropped-server-value"]')
    expect(value.text()).toBe('12')
    expect(value.classes()).toContain('text-warn')

    const info = wrapper.get('[data-testid="health-strip-dropped-server-info"]')
    const reason = info.attributes('title') ?? ''
    expect(reason).toContain('server')
    expect(reason.toLowerCase()).toContain('ingest')
  })

  it('a zero server dropped_total renders no warn indicator', () => {
    const wrapper = mount(HealthStrip, {
      props: { ...BASE_PROPS, stats: { events_per_sec: 3, active_sessions: 1, queue_depth: 0, ingest_lag_ms: 5, dropped_total: 0 } },
    })

    expect(wrapper.get('[data-testid="health-strip-dropped-server-value"]').classes()).not.toContain('text-warn')
  })

  it('a non-zero client-side droppedTotal renders the warn class, with a tooltip naming this tab as the source of the loss — distinct from the server figure', () => {
    const wrapper = mount(HealthStrip, { props: { ...BASE_PROPS, clientDroppedTotal: 4 } })

    const value = wrapper.get('[data-testid="health-strip-dropped-client-value"]')
    expect(value.text()).toBe('4')
    expect(value.classes()).toContain('text-warn')

    const info = wrapper.get('[data-testid="health-strip-dropped-client-info"]')
    const reason = info.attributes('title') ?? ''
    expect(reason.toLowerCase()).toContain('this tab')

    // The two numbers are independent — the server-side tile must not also light up.
    expect(wrapper.get('[data-testid="health-strip-dropped-server-value"]').classes()).not.toContain('text-warn')
  })

  it('a zero client-side droppedTotal renders no warn indicator', () => {
    const wrapper = mount(HealthStrip, { props: { ...BASE_PROPS, clientDroppedTotal: 0 } })
    expect(wrapper.get('[data-testid="health-strip-dropped-client-value"]').classes()).not.toContain('text-warn')
  })

  it('a disconnected state (reconnecting) renders the reconnect indicator', () => {
    const wrapper = mount(HealthStrip, { props: { ...BASE_PROPS, status: 'reconnecting' } })
    expect(wrapper.find('[data-testid="health-strip-reconnect"]').exists()).toBe(true)
  })

  it('a disconnected state (closed) renders the reconnect indicator', () => {
    const wrapper = mount(HealthStrip, { props: { ...BASE_PROPS, status: 'closed' } })
    expect(wrapper.find('[data-testid="health-strip-reconnect"]').exists()).toBe(true)
  })

  it('a connected state renders no reconnect indicator', () => {
    const wrapper = mount(HealthStrip, { props: { ...BASE_PROPS, status: 'open' } })
    expect(wrapper.find('[data-testid="health-strip-reconnect"]').exists()).toBe(false)
  })

  // Round-7 critic gap: the always-visible 2x2 chip grid ballooned this cell to 87px against
  // the other five cells' 47px. The per-exporter breakdown now lives in an info tooltip (same
  // idiom as Dropped (server)/Dropped (this tab)) instead of inline, so the cell shares the
  // strip's two-line height; the count stays the headline value and the tooltip's title carries
  // the same per-exporter facts the old chips did.
  it('renders exporters-seen count from the meta store flags, with per-exporter detail in the info tooltip', () => {
    const wrapper = mount(HealthStrip, {
      props: { ...BASE_PROPS, logsExporterSeen: true, metricsExporterSeen: false, hooksSeen: true, toolDetailsSeen: false },
    })

    expect(wrapper.get('[data-testid="health-strip-exporters-value"]').text()).toBe('2/4')

    const info = wrapper.get('[data-testid="health-strip-exporters-info"]')
    const reason = info.attributes('title') ?? ''
    expect(reason).toContain('Logs — seen')
    expect(reason).toContain('Metrics — not seen')
  })
})
