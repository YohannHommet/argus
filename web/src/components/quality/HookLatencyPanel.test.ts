import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import { ApiError } from '@/api/errors'
import { getQualityHookLatency200Default } from '@/test/fixtures'
import { emptyQualityHookLatency } from '@/test/fixtures.extra'
import HookLatencyPanel from './HookLatencyPanel.vue'

describe('HookLatencyPanel', () => {
  it('renders per-hook_event percentiles from the live-shaped fixture', () => {
    const wrapper = mount(HookLatencyPanel, { props: { rows: getQualityHookLatency200Default.rows } })

    expect(wrapper.text()).toContain('PostToolUse')
    expect(wrapper.get('[data-testid="hook-latency-executions"]').text()).toBe('412')
    expect(wrapper.text()).toContain('9ms')
    expect(wrapper.text()).toContain('41ms')
    expect(wrapper.text()).toContain('120ms')
  })

  it('renders measured-zero errors/cancelled as "0", never a dash', () => {
    const wrapper = mount(HookLatencyPanel, { props: { rows: getQualityHookLatency200Default.rows } })

    const cells = wrapper.findAll('td')
    const rowText = cells.map((c) => c.text())
    // errors and cancelled are the last two columns, both 0 in the fixture.
    expect(rowText.slice(-2)).toEqual(['0', '0'])
  })

  it('renders the empty state (AC) when no hook.execution_end events exist, explaining hooks_seen: false as normal', () => {
    const wrapper = mount(HookLatencyPanel, { props: { rows: emptyQualityHookLatency.rows, hooksSeen: false } })

    const empty = wrapper.get('[data-testid="empty-state"]')
    expect(empty.text()).toContain('No hooks have reported yet')
    expect(empty.text().toLowerCase()).toContain('otlp-only')
  })

  it('the empty state explains a coverage gap differently when hooksSeen is true', () => {
    const wrapper = mount(HookLatencyPanel, { props: { rows: emptyQualityHookLatency.rows, hooksSeen: true } })

    const empty = wrapper.get('[data-testid="empty-state"]')
    expect(empty.text()).not.toContain('OTLP-only')
    expect(empty.text()).toContain('hook.execution_end')
  })

  it('renders a loading skeleton, not the table or empty state, while loading', () => {
    const wrapper = mount(HookLatencyPanel, { props: { rows: [], loading: true } })

    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(false)
  })

  it('renders ErrorState and emits retry on failure', async () => {
    const error = new ApiError({ type: 'urn:argus:error:boom', title: 'Boom', status: 500 }, new Response(null, { status: 500 }))
    const wrapper = mount(HookLatencyPanel, { props: { rows: [], error } })

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('[data-testid="error-state"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders an unrecognised hook_event verbatim via RawValue', () => {
    const wrapper = mount(HookLatencyPanel, {
      props: {
        rows: [{ hook_event: 'SomeFutureHookEvent', executions: 5, p50_ms: 1, p95_ms: 2, p99_ms: 3, errors: 0, cancelled: 0 }],
      },
    })

    expect(wrapper.text()).toContain('SomeFutureHookEvent')
  })

  it('the panel explains it measures Argus\'s own hook overhead, not Claude Code itself', () => {
    const wrapper = mount(HookLatencyPanel, { props: { rows: getQualityHookLatency200Default.rows } })

    expect(wrapper.get('[data-testid="hook-latency-panel-explanation"]').text().toLowerCase()).toContain("argus's own")
  })
})
