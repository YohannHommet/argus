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

  // --- D-30 (docs/review/phase-4-gauntlet.md, owner-ratified 2026-08-18):
  // an all-estimated session must not render a bare "Cost $0.00" as though
  // it were a vendor-reported measurement.

  it('shows no estimated marker when cost.estimated_share is 0 — no-regression pin, byte for byte with today', () => {
    expect(session.cost.estimated_share).toBe(0)
    const wrapper = mount(SessionKpiStrip, { props: { session } })
    expect(wrapper.get('[data-testid="kpi-cost"]').text()).toBe(formatCost(session.cost.usd))
    expect(wrapper.find('[data-testid="kpi-cost-estimated-badge"]').exists()).toBe(false)
  })

  it('shows a fully-estimated marker when cost.estimated_share is 1', () => {
    const fullyEstimated = {
      ...session,
      cost: { ...session.cost, usd: 90, reported_usd: 0, estimated_usd: 90, estimated_share: 1 },
    }
    const wrapper = mount(SessionKpiStrip, {
      props: { session: fullyEstimated },
      global: { stubs: { teleport: true } },
    })
    expect(wrapper.get('[data-testid="kpi-cost"]').text()).toBe(formatCost(90))
    const badge = wrapper.get('[data-testid="kpi-cost-estimated-badge"]')
    expect(badge.text()).toBe('Estimated')
    expect(badge.attributes('title')).toContain('entire cost is estimated')
  })

  it('shows a partly-estimated marker naming the percentage when cost.estimated_share is between 0 and 1', () => {
    const partiallyEstimated = {
      ...session,
      cost: { ...session.cost, estimated_share: 0.32 },
    }
    const wrapper = mount(SessionKpiStrip, {
      props: { session: partiallyEstimated },
      global: { stubs: { teleport: true } },
    })
    const badge = wrapper.get('[data-testid="kpi-cost-estimated-badge"]')
    expect(badge.text()).toBe('Partly est.')
    expect(badge.attributes('title')).toContain('32.0%')
  })
})
