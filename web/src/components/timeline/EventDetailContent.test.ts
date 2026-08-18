import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getEvent200Default } from '@/test/fixtures'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import EventDetailContent from './EventDetailContent.vue'

describe('EventDetailContent', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows a "no event selected" placeholder without fetching anything', () => {
    const store = useSessionDetailStore()
    const spy = vi.spyOn(store, 'loadEvent')

    const wrapper = mount(EventDetailContent, { props: { eventRef: null } })

    expect(wrapper.get('[data-testid="event-detail-empty"]')).toBeTruthy()
    expect(spy).not.toHaveBeenCalled()
  })

  // Round-3 critic ask: "structured summary at top (kind, tool, decision +
  // decision_source badge, duration, cost, tokens)" — not just the raw
  // attrs a click used to reveal.
  it('renders a structured summary with kind, tool, decision badge and ids before the raw attrs', async () => {
    const store = useSessionDetailStore()
    vi.spyOn(store, 'loadEvent').mockResolvedValue(getEvent200Default)

    const wrapper = mount(EventDetailContent, { props: { eventRef: getEvent200Default.event_ref } })
    await flushPromises()

    const summary = wrapper.get('[data-testid="event-detail-summary"]')
    expect(summary.text()).toContain('Tool decision')
    expect(summary.text()).toContain('Edit')
    expect(summary.get('[data-testid="decision-badge"]').text()).toContain('reject')
    expect(summary.text()).toContain(getEvent200Default.event_ref)
    expect(summary.text()).toContain(getEvent200Default.tool_use_id)

    // Raw attrs still render, after the summary.
    expect(wrapper.find('[data-testid="json-viewer"]').exists()).toBe(true)
  })

  it('gives event_ref and tool_use_id their own copy affordance (monospace ids nobody reads digit-by-digit)', async () => {
    const store = useSessionDetailStore()
    vi.spyOn(store, 'loadEvent').mockResolvedValue(getEvent200Default)

    const wrapper = mount(EventDetailContent, { props: { eventRef: getEvent200Default.event_ref } })
    await flushPromises()

    expect(wrapper.findAll('[data-testid="copy-icon-button"]').length).toBeGreaterThanOrEqual(2)
  })

  it('shows null-not-measured for cost/tokens rather than a blank cell when the event has neither', async () => {
    const store = useSessionDetailStore()
    vi.spyOn(store, 'loadEvent').mockResolvedValue({ ...getEvent200Default, cost: null, tokens: null })

    const wrapper = mount(EventDetailContent, { props: { eventRef: getEvent200Default.event_ref } })
    await flushPromises()

    const summary = wrapper.get('[data-testid="event-detail-summary"]')
    const notMeasuredTriggers = summary.findAll('[title="Not measured"]')
    expect(notMeasuredTriggers.length).toBeGreaterThanOrEqual(2)
  })
})
