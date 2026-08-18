import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import CostAttributionCard from './CostAttributionCard.vue'
import { getSessionSubagentsDepth2Live } from '@/test/fixtures.extra'

describe('CostAttributionCard', () => {
  it('renders a loading skeleton while loading', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: null, loading: true } })

    expect(wrapper.find('[data-testid="cost-attribution-loading"]').exists()).toBe(true)
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('renders ErrorState and re-emits retry', async () => {
    const wrapper = mount(CostAttributionCard, { props: { data: null, error: new Error('boom') } })

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('[data-testid="error-state"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders EmptyState when there is no cost attribution at all', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: null } })

    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('renders every by_query_source key as a raw, un-special-cased row: "", "sdk", and an invented value all render the same way', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    const rows = wrapper.findAll('[data-testid="cost-attribution-row"]')
    expect(rows).toHaveLength(Object.keys(getSessionSubagentsDepth2Live.cost_attribution.by_query_source).length)

    const text = wrapper.text()
    expect(text).toContain('unattributed') // the '' key
    expect(text).toContain('sdk')
    expect(text).toContain('a_future_query_source') // invented value, rendered verbatim, no special-casing
  })

  it('sorts rows by cost descending', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    const keys = wrapper.findAll('[data-testid="cost-attribution-row"]').map((row) => row.text())
    // sdk (2.300948) is the highest by_query_source value in the fixture.
    expect(keys[0]).toContain('sdk')
  })

  it('renders the "other query sources: $X of $Y" honest framing using dominant_query_source and other_query_source_usd', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    const summary = wrapper.get('[data-testid="cost-attribution-other-share"]')
    expect(summary.text()).toContain('sdk')
    expect(summary.text()).toContain('other query sources')
    expect(summary.text()).toContain('$3.19')
  })

  it("renders the server's note verbatim", () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    expect(wrapper.get('[data-testid="cost-attribution-note"]').text()).toBe(getSessionSubagentsDepth2Live.cost_attribution.note)
  })

  // Deliberately absent: no test here asserts a per-node/per-agent cost
  // number — this card only ever renders the by_query_source split, which
  // is a whole-session aggregate, never a per-node figure (SPEC §1.9).

  // Round-6 critic gap: this card was surrendering roughly two-thirds of
  // the Subagents tab to an always-expanded table plus three disclaimer
  // sentences. It is now a collapsed-by-default secondary section.
  it('renders the table collapsed (not visible) by default, with an aria-expanded=false toggle', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    const toggle = wrapper.get('[data-testid="cost-attribution-toggle"]')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    const tableWrap = wrapper.get('[data-testid="cost-attribution-table-wrap"]')
    expect(tableWrap.isVisible()).toBe(false)
    // Rows stay in the DOM while collapsed (v-show, not v-if) — this is a
    // display-only collapse, not conditional rendering, so the data is
    // still directly queryable/testable without simulating a click.
    expect(wrapper.findAll('[data-testid="cost-attribution-row"]').length).toBeGreaterThan(0)
  })

  it('expands the table on toggle click', async () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    await wrapper.get('[data-testid="cost-attribution-toggle"]').trigger('click')

    expect(wrapper.get('[data-testid="cost-attribution-toggle"]').attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('[data-testid="cost-attribution-table-wrap"]').isVisible()).toBe(true)
  })

  it('collapses the per-node-unavailable and description disclaimers into tooltip triggers, not standalone sentences', () => {
    const wrapper = mount(CostAttributionCard, { props: { data: getSessionSubagentsDepth2Live.cost_attribution } })

    expect(wrapper.text()).not.toContain('Per-node cost is not available for this session')
    const hint = wrapper.get('[data-testid="cost-attribution-per-node-hint"]')
    expect(hint.attributes('title')).toContain('Per-node cost is not available')
  })
})
