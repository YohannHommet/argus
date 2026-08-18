import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import { collapseEvents } from '@/lib/collapseEvents'
import { listSessionTurns200Default } from '@/test/fixtures'
import { makeTimelineEvent } from '@/test/fixtures.extra'
import TimelineGroup from './TimelineGroup.vue'

describe('TimelineGroup', () => {
  it('renders an explicit "No turn" header when promptId is null', () => {
    const items = collapseEvents([makeTimelineEvent({ prompt_id: null, tool_use_id: null, kind: 'hook.registered' })])
    const wrapper = mount(TimelineGroup, { props: { promptId: null, items } })
    expect(wrapper.get('[data-testid="timeline-group-header"]').text()).toContain('No turn')
  })

  it('renders the turn number for a real prompt_id', () => {
    const turn = listSessionTurns200Default.data[0]!
    const items = collapseEvents([makeTimelineEvent({ prompt_id: turn.prompt_id })])
    const wrapper = mount(TimelineGroup, { props: { promptId: turn.prompt_id, turn, items } })
    expect(wrapper.get('[data-testid="timeline-group-header"]').text()).toContain(String(turn.turn_index))
  })

  it("shows the turn's cost and tokens in the header when a Turn is supplied", () => {
    const turn = listSessionTurns200Default.data[0]!
    const items = collapseEvents([makeTimelineEvent({ prompt_id: turn.prompt_id })])
    const wrapper = mount(TimelineGroup, { props: { promptId: turn.prompt_id, turn, items } })
    expect(wrapper.get('[data-testid="timeline-group-header"]').text()).toContain('$0.42')
  })

  it('renders one EventRow per item and forwards open events', async () => {
    const items = collapseEvents([makeTimelineEvent({ prompt_id: 'p_1' }), makeTimelineEvent({ prompt_id: 'p_1', tool_use_id: null, kind: 'llm.request' })])
    const wrapper = mount(TimelineGroup, { props: { promptId: 'p_1', items } })
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(2)
    await wrapper.findAll('[data-testid="event-row"]')[0]!.trigger('click')
    expect(wrapper.emitted('open')).toBeTruthy()
  })

  it('labels a continuation run "Turn N · continued" when isContinuation is true', () => {
    const turn = listSessionTurns200Default.data[0]!
    const items = collapseEvents([makeTimelineEvent({ prompt_id: turn.prompt_id })])
    const wrapper = mount(TimelineGroup, { props: { promptId: turn.prompt_id, turn, items, isContinuation: true } })
    expect(wrapper.get('[data-testid="timeline-group-header"]').text()).toContain('continued')
  })

  it('does not say "continued" for a turn\'s first (non-continuation) run', () => {
    const turn = listSessionTurns200Default.data[0]!
    const items = collapseEvents([makeTimelineEvent({ prompt_id: turn.prompt_id })])
    const wrapper = mount(TimelineGroup, { props: { promptId: turn.prompt_id, turn, items, isContinuation: false } })
    expect(wrapper.get('[data-testid="timeline-group-header"]').text()).not.toContain('continued')
  })

  it("nests a tool.result under its tool.pre call (shared tool_use_id) instead of as a flat sibling", () => {
    const items = collapseEvents([
      makeTimelineEvent({ prompt_id: 'p_1', kind: 'tool.pre', tool_use_id: 'toolu_1', ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ prompt_id: 'p_1', kind: 'tool.result', tool_use_id: 'toolu_1', decision: 'accept', decision_source: 'config', ts: '2026-08-14T01:00:05.000Z' }),
    ])
    const wrapper = mount(TimelineGroup, { props: { promptId: 'p_1', items } })

    // Two rows total (the call and its result), but only the call sits
    // directly in the turn rail — its result is inside the nested
    // tool-thread container, not a flat sibling of it.
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(2)
    const nested = wrapper.get('[data-testid="tool-thread-children"]')
    expect(nested.findAll('[data-testid="event-row"]')).toHaveLength(1)
  })

  it("surfaces the tool.result's decision onto the parent tool.pre row's DecisionBadge", () => {
    const items = collapseEvents([
      makeTimelineEvent({ prompt_id: 'p_1', kind: 'tool.pre', tool_use_id: 'toolu_1', decision: null, ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ prompt_id: 'p_1', kind: 'tool.result', tool_use_id: 'toolu_1', decision: 'reject', decision_source: 'user_reject', ts: '2026-08-14T01:00:05.000Z' }),
    ])
    const wrapper = mount(TimelineGroup, { props: { promptId: 'p_1', items } })

    const parentRow = wrapper.findAll('[data-testid="event-row"]')[0]!
    expect(parentRow.find('[data-testid="decision-badge"]').exists()).toBe(true)
    expect(parentRow.text()).toContain('reject')
  })

  it('highlights the row matching selectedEventRef', () => {
    const event = makeTimelineEvent({ prompt_id: 'p_1', tool_use_id: null, kind: 'llm.request' })
    const items = collapseEvents([event])
    const wrapper = mount(TimelineGroup, { props: { promptId: 'p_1', items, selectedEventRef: event.event_ref } })
    expect(wrapper.get('[data-testid="event-row"]').attributes('aria-selected')).toBe('true')
  })
})
