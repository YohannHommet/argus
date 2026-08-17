import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SessionKpiStrip from './SessionKpiStrip.vue'
import { formatCost } from '@/lib/format'
import { listSessions200Default } from '@/test/fixtures'
import { partialSessionSummary } from '@/test/fixtures.extra'

const session = listSessions200Default.data[0]

describe('SessionKpiStrip', () => {
  it('shows the session-list row\'s exact cost.usd, formatted with formatCost — Phase-4 exit criterion 2', () => {
    const wrapper = mount(SessionKpiStrip, { props: { session } })
    expect(wrapper.get('[data-testid="kpi-cost"]').text()).toBe(formatCost(session.cost.usd))
  })

  it('shows turns, tool calls and duration', () => {
    const wrapper = mount(SessionKpiStrip, { props: { session } })
    expect(wrapper.get('[data-testid="kpi-turns"]').text()).toBe('12')
    expect(wrapper.get('[data-testid="kpi-tools"]').text()).toBe('96')
    expect(wrapper.get('[data-testid="kpi-duration"]').text()).not.toBe('')
  })

  it('computes reject rate as reject/call when tool_call_count > 0', () => {
    const wrapper = mount(SessionKpiStrip, {
      props: { session: { ...session, tool_call_count: 96, tool_reject_count: 3 } },
    })
    expect(wrapper.get('[data-testid="kpi-reject-rate"]').text()).toBe('3.1%')
  })

  it('renders "—" (not 0%) when tool_call_count is 0 — the rate is undefined, not zero', () => {
    const wrapper = mount(SessionKpiStrip, {
      props: { session: { ...session, tool_call_count: 0, tool_reject_count: 0 } },
      global: { stubs: { teleport: true } },
    })
    const text = wrapper.get('[data-testid="kpi-reject-rate"]').text()
    expect(text).toContain('—')
    expect(text).not.toContain('0%')
  })

  it('a null session renders every field as "—" with no NaN or Invalid Date', () => {
    const wrapper = mount(SessionKpiStrip, { props: { session: null } })
    const text = wrapper.text()
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('Invalid Date')
  })

  it('partial (started_at/duration_ms null) session shows no NaN/Invalid Date anywhere', () => {
    const wrapper = mount(SessionKpiStrip, { props: { session: partialSessionSummary } })
    const text = wrapper.text()
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('Invalid Date')
    expect(wrapper.get('[data-testid="kpi-duration"]').text()).toBe('—')
  })
})
