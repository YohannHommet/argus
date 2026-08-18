import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import { collapseEvents } from '@/lib/collapseEvents'
import { hookToolResultEventClose, makeTimelineEvent, otelToolResultEvent } from '@/test/fixtures.extra'
import EventRow from './EventRow.vue'

describe('EventRow', () => {
  it('renders the kind label and tool name', () => {
    const [item] = collapseEvents([otelToolResultEvent])
    const wrapper = mount(EventRow, { props: { item: item! } })
    expect(wrapper.get('[data-testid="event-row"]').text()).toContain('Tool result')
    expect(wrapper.text()).toContain('Edit')
  })

  it('emits open with the primary event_ref on row click', async () => {
    const [item] = collapseEvents([otelToolResultEvent])
    const wrapper = mount(EventRow, { props: { item: item! } })
    await wrapper.get('[data-testid="event-row"]').trigger('click')
    expect(wrapper.emitted('open')).toEqual([[otelToolResultEvent.event_ref]])
  })

  it('shows a "N sources" affordance only when the item collapsed more than one event', () => {
    const single = collapseEvents([otelToolResultEvent])[0]!
    const collapsed = collapseEvents([otelToolResultEvent, hookToolResultEventClose])[0]!
    expect(mount(EventRow, { props: { item: single } }).find('[data-testid="event-row-sources"]').exists()).toBe(false)
    expect(mount(EventRow, { props: { item: collapsed } }).find('[data-testid="event-row-sources"]').exists()).toBe(true)
  })

  it('renders a DecisionBadge only when the item has a decision', () => {
    const noDecision = collapseEvents([makeTimelineEvent({ kind: 'hook.registered', decision: null, tool_use_id: null, prompt_id: null })])[0]!
    const withDecision = collapseEvents([makeTimelineEvent({ kind: 'tool.decision', decision: 'accept', decision_source: 'config' })])[0]!
    expect(mount(EventRow, { props: { item: noDecision } }).find('[data-testid="decision-badge"]').exists()).toBe(false)
    expect(mount(EventRow, { props: { item: withDecision } }).find('[data-testid="decision-badge"]').exists()).toBe(true)
  })

  it('shows a clock-skew warning when the item is clock_skewed', () => {
    const skewed = collapseEvents([makeTimelineEvent({ clock_skewed: true })])[0]!
    const notSkewed = collapseEvents([makeTimelineEvent({ clock_skewed: false })])[0]!
    expect(mount(EventRow, { props: { item: skewed } }).find('svg[aria-hidden="true"][title]').exists()).toBe(true)
    expect(mount(EventRow, { props: { item: notSkewed } }).findAll('svg[title]').length).toBe(0)
  })

  // Round-3 critic gap: "row selection state must be visible" — the
  // inspector can be open on some event, but nothing in the list shows
  // which row that is without this.
  it('marks the row selected via aria-selected/data-selected when selected is true, and not otherwise', () => {
    const [item] = collapseEvents([otelToolResultEvent])
    const selected = mount(EventRow, { props: { item: item!, selected: true } }).get('[data-testid="event-row"]')
    const notSelected = mount(EventRow, { props: { item: item!, selected: false } }).get('[data-testid="event-row"]')

    expect(selected.attributes('aria-selected')).toBe('true')
    expect(selected.attributes('data-selected')).toBe('true')
    expect(notSelected.attributes('aria-selected')).toBe('false')
  })

  // Round-5 critic gap: no `sessionLabel` (the default, and every existing caller above) must
  // render no session column at all — a single-session timeline is not the firehose.
  it('renders no session identity column when sessionLabel is not supplied', () => {
    const [item] = collapseEvents([otelToolResultEvent])
    const wrapper = mount(EventRow, { props: { item: item! } })
    expect(wrapper.find('[data-testid="event-row-session"]').exists()).toBe(false)
  })

  it('renders the given sessionLabel as its own column when supplied', () => {
    const [item] = collapseEvents([otelToolResultEvent])
    const wrapper = mount(EventRow, { props: { item: item!, sessionLabel: 'platform · a1b2c3d4' } })
    expect(wrapper.get('[data-testid="event-row-session"]').text()).toBe('platform · a1b2c3d4')
  })

  // Round-5 critic gap: "two identical '3.4s' landed 136px apart" — each fixed metric column must
  // keep its reserved width whether or not this row kind carries a value, so a `v-if` on the
  // *value* is fine but a `v-if` that drops the whole column is not.
  it('always renders the offset/duration/cost/tokens columns, falling back to EM_DASH per-value rather than dropping the column', () => {
    const [item] = collapseEvents([makeTimelineEvent({ cost: null, tokens: null, duration_ms: null })])
    const wrapper = mount(EventRow, { props: { item: item! } })

    expect(wrapper.get('[data-testid="event-row-offset"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="event-row-duration"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="event-row-cost"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="event-row-tokens"]').text()).toBe('—')
  })
})
