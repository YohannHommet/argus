import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import DecisionBadge from './DecisionBadge.vue'

const KNOWN_SOURCES = ['config', 'hook', 'user_permanent', 'user_temporary', 'user_reject', 'user_abort']

describe('DecisionBadge', () => {
  it('renders — with a reason when there is no decision', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: null, decisionSource: null } })
    expect(wrapper.text()).toContain('—')
  })

  it.each(KNOWN_SOURCES)('renders a distinct label for the documented decision_source %s', (source) => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: source } })
    expect(wrapper.text().length).toBeGreaterThan(0)
  })

  it('gives every documented decision_source a distinct label from every other', () => {
    const labels = KNOWN_SOURCES.map((source) => {
      const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: source } })
      return wrapper.get('[data-testid="decision-badge-source"]').text()
    })
    expect(new Set(labels).size).toBe(labels.length)
  })

  it('renders an unknown decision_source ("an_invented_decision_source") verbatim via RawValue', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: 'an_invented_decision_source' } })
    expect(wrapper.get('[data-testid="decision-badge-source"]').text()).toBe('an_invented_decision_source')
  })

  it('renders decisionSource "sdk" verbatim (not one of the 6 documented values — RawValue, no special-casing)', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: 'sdk' } })
    expect(wrapper.get('[data-testid="decision-badge-source"]').text()).toBe('sdk')
  })

  it('renders decisionSource "a_value_from_the_future" verbatim', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: 'a_value_from_the_future' } })
    expect(wrapper.get('[data-testid="decision-badge-source"]').text()).toBe('a_value_from_the_future')
  })

  it('renders an unknown decision value verbatim too', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'a_future_decision', decisionSource: 'config' } })
    expect(wrapper.get('[data-testid="decision-badge"]').text()).toContain('a_future_decision')
  })

  it('renders no caveat for an exact correlation', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: 'config', correlation: 'exact' } })
    expect(wrapper.find('[data-testid="decision-badge-caveat"]').exists()).toBe(false)
  })

  it('renders no caveat when correlation is not provided', () => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: 'config' } })
    expect(wrapper.find('[data-testid="decision-badge-caveat"]').exists()).toBe(false)
  })

  it.each(['otel_only', 'hook_only', 'heuristic'] as const)('renders the heuristic-match caveat for a non-exact correlation (%s)', (correlation) => {
    const wrapper = mount(DecisionBadge, { props: { decision: 'accept', decisionSource: 'config', correlation } })
    const caveat = wrapper.get('[data-testid="decision-badge-caveat"]')
    expect(caveat.attributes('title')).toContain('heuristically')
  })
})
