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
})
