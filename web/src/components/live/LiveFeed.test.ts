import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'

import { listSessions200Default } from '@/test/fixtures'
import { makeTimelineEvent, secondSessionSummary } from '@/test/fixtures.extra'
import { formatAbsoluteTime, formatWallClockTime } from '@/lib/format'
import type { TimelineEvent } from '@/stores/live'
import { Select } from '@/components/ui/select'
import EventDetailSheet from '@/components/timeline/EventDetailSheet.vue'
import EventRow from '@/components/timeline/EventRow.vue'
import LiveFeed from './LiveFeed.vue'

const firstSession = listSessions200Default.data[0]!

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

  // Round-5 critic gap: "it's a fleet-wide firehose" — a row must say whose session it is, since
  // (unlike the single-session timeline this same EventRow also renders) many sessions interleave.
  describe('session identity column', () => {
    it('shows "project · shortId" when the sessions prop resolves that session_id', () => {
      const events = [makeTimelineEvent({ session_id: firstSession.id })]
      const wrapper = mount(LiveFeed, { props: { events, sessions: [firstSession], paused: false, bufferedCount: 0 } })

      expect(wrapper.get('[data-testid="event-row-session"]').text()).toBe(`${firstSession.project} · ${firstSession.id.slice(0, 8)}`)
    })

    it('falls back to the short id alone when no session summary is known for that session_id yet', () => {
      const events = [makeTimelineEvent({ session_id: 'unresolved-session-id-0001' })]
      const wrapper = mount(LiveFeed, { props: { events, sessions: [], paused: false, bufferedCount: 0 } })

      expect(wrapper.get('[data-testid="event-row-session"]').text()).toBe('unresolved-session-id-0001'.slice(0, 8))
    })

    it('falls back to the short id alone when the known session has no project signal yet (project: "")', () => {
      const noProjectYet = { ...firstSession, project: '' }
      const events = [makeTimelineEvent({ session_id: noProjectYet.id })]
      const wrapper = mount(LiveFeed, { props: { events, sessions: [noProjectYet], paused: false, bufferedCount: 0 } })

      expect(wrapper.get('[data-testid="event-row-session"]').text()).toBe(noProjectYet.id.slice(0, 8))
    })

    it('distinguishes two interleaved sessions on the same feed', () => {
      const events = [
        makeTimelineEvent({ session_id: firstSession.id }),
        makeTimelineEvent({ session_id: secondSessionSummary.id }),
      ]
      const wrapper = mount(LiveFeed, { props: { events, sessions: [firstSession, secondSessionSummary], paused: false, bufferedCount: 0 } })
      const labels = wrapper.findAll('[data-testid="event-row-session"]').map((el) => el.text())

      // Reverse-chronological render order (LiveFeed's own convention): the second event renders first.
      expect(labels).toEqual([
        `${secondSessionSummary.project} · ${secondSessionSummary.id.slice(0, 8)}`,
        `${firstSession.project} · ${firstSession.id.slice(0, 8)}`,
      ])
    })
  })

  // Round-5 critic gap: "two identical '3.4s' landed 136px apart" — a row's fixed metric columns
  // (offset/duration/cost/tokens) must reserve their width even when a given row kind carries no
  // cost/tokens, or the columns after the gap slide into different x-offsets across row kinds.
  it('renders the same set of fixed metric columns whether or not a row carries cost/tokens', () => {
    const events = [
      makeTimelineEvent({ kind: 'tool.result', cost: null, tokens: null, duration_ms: 3400 }),
      makeTimelineEvent({ kind: 'llm.request', cost: 0.02, tokens: { input: 6000, output: 100, cache_read: 0, cache_creation: 0 }, duration_ms: 3400 }),
    ]
    const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
    const rows = wrapper.findAll('[data-testid="event-row"]')
    expect(rows).toHaveLength(2)

    for (const row of rows) {
      // `event-row-time`, not `event-row-offset`: the live feed renders
      // `EventRow` in `wallClockTime` mode (round-6 critic gap — see the
      // "arrival time column" describe block below), so its leading numeric
      // column is a different testid from the session-detail timeline's.
      expect(row.find('[data-testid="event-row-time"]').exists()).toBe(true)
      expect(row.find('[data-testid="event-row-duration"]').exists()).toBe(true)
      expect(row.find('[data-testid="event-row-cost"]').exists()).toBe(true)
      expect(row.find('[data-testid="event-row-tokens"]').exists()).toBe(true)
    }

    // The no-cost/no-tokens row (rendered second, oldest-first input reversed to newest-first) still
    // shows the shared duration value in the same column — never dropped, never shifted.
    const noCostRow = rows[1]!
    expect(noCostRow.get('[data-testid="event-row-duration"]').text()).toBe('3.4s')
    expect(noCostRow.get('[data-testid="event-row-cost"]').text()).toBe('—')
    expect(noCostRow.get('[data-testid="event-row-tokens"]').text()).toBe('—')
  })

  // Round-6 critic gap: "the event feed has no column header row (and no
  // legend), leaving its four right-hand numeric tracks — mostly em-dashes —
  // unnameable". A sticky header now names every column `EventRow` renders.
  describe('column header row', () => {
    it('labels every column EventRow renders, sticky above the list', () => {
      const events = makeFrames(1)
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      const header = wrapper.get('[data-testid="live-feed-header"]')

      expect(header.classes()).toContain('sticky')
      expect(header.get('[data-testid="live-feed-header-session"]').text()).toBe('Session')
      expect(header.get('[data-testid="live-feed-header-event"]').text()).toBe('Event')
      expect(header.get('[data-testid="live-feed-header-received"]').text()).toBe('Received')
      expect(header.get('[data-testid="live-feed-header-duration"]').text()).toBe('Duration')
      expect(header.get('[data-testid="live-feed-header-cost"]').text()).toBe('Cost')
      expect(header.get('[data-testid="live-feed-header-tokens"]').text()).toBe('Tokens')
    })

    it('renders no header when there are no events to label (EmptyState instead)', () => {
      const wrapper = mount(LiveFeed, { props: { events: [], paused: false, bufferedCount: 0 } })
      expect(wrapper.find('[data-testid="live-feed-header"]').exists()).toBe(false)
    })
  })

  // Round-9 critic gap: the Event column's `flex-1` soaked up all remaining
  // row width, stranding the Received/Duration/Cost/Tokens cluster in a
  // 460–630px void from the actual event content — one row reading as two
  // disconnected halves. Every row now renders `EventRow` with
  // `compactEventColumn` (a fixed identity-cluster width instead of a
  // growing one, see `EventRow.vue`'s own doc on that prop), and the sticky
  // header's "Event" column is kept at that same fixed width so the two
  // never drift out of lockstep.
  describe('compact event-column layout', () => {
    it('renders every row with EventRow\'s compactEventColumn, not the timeline\'s default growing column', () => {
      const events = makeFrames(2)
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

      const rows = wrapper.findAllComponents(EventRow)
      expect(rows).toHaveLength(2)
      for (const row of rows) {
        expect(row.props('compactEventColumn')).toBe(true)
      }
    })

    it('gives the sticky header\'s Event column the same fixed width as the row\'s identity cluster, not the old flex-1', () => {
      const events = makeFrames(1)
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

      const headerEvent = wrapper.get('[data-testid="live-feed-header-event"]')
      expect(headerEvent.classes()).toContain('w-96')
      expect(headerEvent.classes()).not.toContain('flex-1')
    })
  })

  // Round-6 critic gap: the leading numeric column was `EM_DASH` on every
  // single row (no `originTs` for a firehose to offset against) — an
  // "unnameable" column because it never carried a real value to name.
  // Round-8 critic gap: once it *did* carry a value, that value was each
  // row's own `ts` rendered down a list ordered by arrival — real
  // out-of-order event clocks then read as a jumbled column (12:15:57, 50,
  // 55, 45 …) even though the row order itself is perfectly consistent
  // (newest arrival on top). The column is now "Received": `liveStore`'s
  // `receivedAt`, stamped once as each frame lands, which is monotonic by
  // construction — the event's own `ts` moves to the row's hover title and
  // stays untouched in `EventDetailSheet`'s inspector.
  describe('Received column', () => {
    it('shows the client receive time, not the event\'s own ts, when receivedAt is present', () => {
      const events = [{ ...makeTimelineEvent({ ts: '2026-08-19T09:05:03.000Z' }), receivedAt: '2026-08-19T09:06:40.000Z' }]
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

      const time = wrapper.get('[data-testid="event-row-time"]')
      expect(time.text()).toMatch(/^\d{2}:\d{2}:\d{2}$/)
      expect(time.text()).not.toBe('—')
      expect(time.text()).toBe(formatWallClockTime('2026-08-19T09:06:40.000Z'))
    })

    it('falls back to the event\'s own ts when no receivedAt stamp is present (a plain fixture that never went through liveStore)', () => {
      const events = [makeTimelineEvent({ ts: '2026-08-19T09:05:03.000Z' })]
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

      const time = wrapper.get('[data-testid="event-row-time"]')
      expect(time.text()).toBe(formatWallClockTime('2026-08-19T09:05:03.000Z'))
    })

    it('carries the event\'s own ts on the row\'s hover title instead of dropping it', () => {
      const events = [{ ...makeTimelineEvent({ ts: '2026-08-19T09:05:03.000Z' }), receivedAt: '2026-08-19T09:06:40.000Z' }]
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })

      const row = wrapper.get('[data-testid="event-row"]')
      expect(row.attributes('title')).toContain(formatAbsoluteTime('2026-08-19T09:05:03.000Z'))
    })

    // The round-8 critic's own example, reproduced: arrival order carries
    // strictly increasing receivedAt stamps (liveStore's "monotonic by
    // construction" guarantee) while the underlying event ts values are
    // shuffled — real clock skew / multi-source fan-in, not a bug to sort
    // away. The firehose keeps arrival order; only the displayed axis must
    // read monotonically.
    it('reads monotonically top-to-bottom by arrival even when the underlying event ts values are shuffled', () => {
      const base = Date.parse('2026-08-19T12:00:00.000Z')
      const shuffledTsOffsetSeconds = [57, 50, 55, 45, 37, 30]
      const events = shuffledTsOffsetSeconds.map((offsetSeconds, arrivalIndex) => ({
        ...makeTimelineEvent({ ts: new Date(base + offsetSeconds * 1000).toISOString() }),
        // Arrival order (index 0 = first received) with receivedAt strictly increasing by 1s per frame.
        receivedAt: new Date(base + arrivalIndex * 1000).toISOString(),
      }))

      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      const times = wrapper.findAll('[data-testid="event-row-time"]').map((el) => el.text())

      expect(times).toHaveLength(6)
      // Newest arrival renders first (LiveFeed's own reversal), so the "Received" column must be
      // non-increasing top-to-bottom — the exact property the critic's screenshot showed missing.
      for (let i = 1; i < times.length; i++) {
        expect(times[i - 1]! >= times[i]!).toBe(true)
      }
      // And it is not simply the sorted event ts values in disguise: it is arrival order.
      const expected = shuffledTsOffsetSeconds
        .map((_, arrivalIndex) => formatWallClockTime(new Date(base + arrivalIndex * 1000).toISOString()))
        .reverse()
      expect(times).toEqual(expected)
    })
  })

  it('shows a stream count near the pause control, counting every received frame regardless of the kind filter', async () => {
    const events = makeFrames(3)
    const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
    expect(wrapper.get('[data-testid="live-feed-event-count"]').text()).toBe('3 events this tab')

    await wrapper.findComponent(Select).vm.$emit('update:modelValue', ['unknown'])
    await nextTick()
    // Filtering to a kind with zero matches still reports the true received count, not 0.
    expect(wrapper.get('[data-testid="live-feed-event-count"]').text()).toBe('3 events this tab')
  })

  describe('duration-scale caption', () => {
    it('shows the log-scale caption once a visible row has a measured duration', () => {
      const events = [makeTimelineEvent({ duration_ms: 15800 })]
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      expect(wrapper.get('[data-testid="live-feed-duration-scale-note"]').text()).toBe('Duration bar: log scale, max 15.8s')
    })

    it('hides the caption when nothing on the feed has a measured duration', () => {
      const events = [makeTimelineEvent({ duration_ms: null })]
      const wrapper = mount(LiveFeed, { props: { events, paused: false, bufferedCount: 0 } })
      expect(wrapper.find('[data-testid="live-feed-duration-scale-note"]').exists()).toBe(false)
    })
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
