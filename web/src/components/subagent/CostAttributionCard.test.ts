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
})
