import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'

import { makeTimelineEvent } from '@/test/fixtures.extra'
import type { TimelineEvent } from '@/stores/live'
import { Select } from '@/components/ui/select'
import EventDetailSheet from '@/components/timeline/EventDetailSheet.vue'
import LiveFeed from './LiveFeed.vue'

/**
 * `n` fixture events, chronological (oldest-first, matching `liveStore.events`'s own convention),
 * each tagged with a `frame-N` tool_name so a test can identify which frame rendered where without
 * depending on `event_ref` (left to the fixture helper's own counter, so concatenating several
 * `makeFrames()` calls in one test never collides on a duplicate `:key`).
 */
function makeFrames(n: number, overrides: Partial<TimelineEvent> = {}): TimelineEvent[] {
  return Array.from({ length: n }, (_, i) => makeTimelineEvent({ kind: 'tool.pre', tool_name: `frame-${i}`, ...overrides }))
}

describe('LiveFeed', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('renders 100 fake frames in reverse-chronological order (newest first), capped at what is shown', () => {
    const events = makeFrames(100)
    const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

    const rows = wrapper.findAll('[data-testid="event-row"]')
    expect(rows).toHaveLength(100)
    // Newest (frame-99, last in the chronological input) renders first; oldest (frame-0) renders last.
    expect(rows[0]!.find('[data-testid="event-row-detail"]').text()).toBe('frame-99')
    expect(rows[rows.length - 1]!.find('[data-testid="event-row-detail"]').text()).toBe('frame-0')
  })

  it('pause freezes the list and the resume badge shows the buffered count', async () => {
    const initial = makeFrames(3)
    const wrapper = mount(LiveFeed, { props: { events: initial, paused: false, bufferedCount: 0 } })
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(3)

    await wrapper.setProps({ paused: true })
    expect(wrapper.find('[data-testid="live-feed-buffered-badge"]').exists()).toBe(false)

    // More frames arrive (as `liveStore.events` would grow) while paused — the rendered list must
    // not react to them, and the buffered badge must show the count.
    const grown = [...initial, ...makeFrames(2, { session_id: 'other-session' })]
    await wrapper.setProps({ events: grown, paused: true, bufferedCount: 2 })

    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(3)
    const badge = wrapper.get('[data-testid="live-feed-buffered-badge"]')
    expect(badge.text()).toContain('2')

    await wrapper.setProps({ events: grown, paused: false, bufferedCount: 0 })
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(5)
    expect(wrapper.find('[data-testid="live-feed-buffered-badge"]').exists()).toBe(false)
  })

  it('the kind filter narrows rendered rows, and "unknown" (Kind\'s own closed-vocabulary catch-all) is selectable and still renders', async () => {
    const events = [
      makeTimelineEvent({ kind: 'tool.pre', tool_name: 'Bash' }),
      makeTimelineEvent({ kind: 'unknown', event_name: 'some_future_vendor_event' }),
    ]
    const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
    expect(wrapper.findAll('[data-testid="event-row"]')).toHaveLength(2)

    await wrapper.findComponent(Select).vm.$emit('update:modelValue', ['unknown'])
    await nextTick()

    const rows = wrapper.findAll('[data-testid="event-row"]')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('Unknown')
  })

  describe('auto-scroll', () => {
    /**
     * jsdom performs no layout, so `scrollTop`/`scrollHeight`/`clientHeight` are driven by hand
     * (as instructed) rather than by actually scrolling. This does verify the real contract — the
     * `scroll` handler reading these three properties and `following`'s resulting effect on the
     * DOM — it just cannot verify that a real browser's pixel geometry would produce the same
     * `scrollTop` a user's mouse wheel would; that half is left to manual/visual verification.
     */
    function setGeometry(el: Element, { scrollTop, scrollHeight, clientHeight }: { scrollTop: number; scrollHeight: number; clientHeight: number }) {
      Object.defineProperty(el, 'scrollTop', { value: scrollTop, writable: true, configurable: true })
      Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
      Object.defineProperty(el, 'clientHeight', { value: clientHeight, configurable: true })
    }

    it('stays following (no "jump to latest") while scrolled to the top as new frames arrive', async () => {
      const events = makeFrames(3)
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      const scrollEl = wrapper.get('[data-testid="live-feed-scroll"]').element

      // Newest-first layout (see LiveFeed.vue's own doc comment): "following" means pinned to
      // scrollTop 0, the inverse of a bottom-anchored chat log.
      setGeometry(scrollEl, { scrollTop: 0, scrollHeight: 300, clientHeight: 200 })
      await wrapper.get('[data-testid="live-feed-scroll"]').trigger('scroll')
      expect(wrapper.find('[data-testid="live-feed-jump-latest"]').exists()).toBe(false)

      await wrapper.setProps({ events: [...events, ...makeFrames(1)] })
      expect(wrapper.find('[data-testid="live-feed-jump-latest"]').exists()).toBe(false)
    })

    it('stops following and shows "jump to latest" once scrolled away from the top', async () => {
      const events = makeFrames(3)
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      const scrollEl = wrapper.get('[data-testid="live-feed-scroll"]').element

      setGeometry(scrollEl, { scrollTop: 120, scrollHeight: 300, clientHeight: 200 })
      await wrapper.get('[data-testid="live-feed-scroll"]').trigger('scroll')

      expect(wrapper.find('[data-testid="live-feed-jump-latest"]').exists()).toBe(true)
    })

    it('clicking "jump to latest" resumes following and scrolls back to the top', async () => {
      const events = makeFrames(3)
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      const scrollEl = wrapper.get('[data-testid="live-feed-scroll"]').element
      setGeometry(scrollEl, { scrollTop: 120, scrollHeight: 300, clientHeight: 200 })
      await wrapper.get('[data-testid="live-feed-scroll"]').trigger('scroll')
      expect(wrapper.find('[data-testid="live-feed-jump-latest"]').exists()).toBe(true)

      await wrapper.get('[data-testid="live-feed-jump-latest"]').trigger('click')

      expect((scrollEl as HTMLElement).scrollTop).toBe(0)
      expect(wrapper.find('[data-testid="live-feed-jump-latest"]').exists()).toBe(false)
    })
  })

  // Known integration gap (flagged for the lead, not papered over here): `EventDetailSheet`'s
  // content fetch (`EventDetailContent.vue` -> `useSessionDetailStore().loadEvent`) is gated on
  // `sessionDetail.currentId`, which is P5-06's session-detail-page concept and is never set from
  // the Live view (a firehose spans many sessions, there is no one "current" session). With no
  // active session, `loadEvent` resolves `null` *before* calling the API, so the sheet opens with
  // the right `event_ref` (asserted below) but its content stays blank — no error, no spinner.
  // Neither `sessionDetail.ts` nor `EventDetailContent.vue` are in this ticket's scope to fix.
  it('a row click opens EventDetailSheet with that row\'s event_ref', async () => {
    const events = [makeTimelineEvent({ event_ref: 'ref-abc' })]
    const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

    await wrapper.get('[data-testid="event-row"]').trigger('click')

    const sheet = wrapper.findComponent(EventDetailSheet)
    expect(sheet.props('eventRef')).toBe('ref-abc')
    expect(sheet.props('open')).toBe(true)
  })
})

describe('LiveFeed — pause toggle and sheet lifecycle', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  // The one button carries both directions, so both branches of its click
  // handler and its icon have to be exercised — a toggle stuck emitting
  // 'pause' while already paused is a dead control that still looks alive.
  it('emits pause when running and resume when paused', async () => {
    const events = makeFrames(3)
    const running = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
    await running.get('[data-testid="live-feed-pause-toggle"]').trigger('click')
    expect(running.emitted('pause')).toHaveLength(1)
    expect(running.emitted('resume')).toBeUndefined()

    const paused = mount(LiveFeed, { props: { events, paused: true, bufferedCount: 7 } })
    await paused.get('[data-testid="live-feed-pause-toggle"]').trigger('click')
    expect(paused.emitted('resume')).toHaveLength(1)
    expect(paused.emitted('pause')).toBeUndefined()
    // The resume badge must show what accumulated while frozen.
    expect(paused.get('[data-testid="live-feed-buffered-badge"]').text()).toContain('7')
  })

  it('closing the detail sheet clears its open state so the next row click reopens it', async () => {
    const events = makeFrames(2)
    const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
    const sheet = wrapper.findComponent(EventDetailSheet)

    await wrapper.findAll('[data-testid="event-row"]')[0]!.trigger('click')
    expect(sheet.props('open')).toBe(true)

    // The sheet owns its own dismissal (overlay click, Escape) and reports it
    // upward; the host must honour it, or the sheet can never be reopened.
    sheet.vm.$emit('update:open', false)
    await nextTick()
    expect(sheet.props('open')).toBe(false)
  })
})
