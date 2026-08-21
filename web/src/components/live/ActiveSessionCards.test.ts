import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import { listSessions200Default } from '@/test/fixtures'
import { makeTimelineEvent, secondSessionSummary } from '@/test/fixtures.extra'
import ActiveSessionCards from './ActiveSessionCards.vue'

const firstSession = listSessions200Default.data[0]!

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/live', component: { template: '<div/>' } },
      { path: '/sessions/:id', name: 'session-detail', component: { template: '<div/>' } },
    ],
  })
}

async function mountCards(props: { sessions: typeof listSessions200Default.data; events: ReturnType<typeof makeTimelineEvent>[] }) {
  const router = makeRouter()
  await router.push('/live')
  await router.isReady()
  return mount(ActiveSessionCards, { props, global: { plugins: [router] } })
}

describe('ActiveSessionCards', () => {
  it('renders an empty state when no sessions are active', async () => {
    const wrapper = await mountCards({ sessions: [], events: [] })
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="active-session-card"]').exists()).toBe(false)
  })

  it('renders a tile per session with its cost', async () => {
    const wrapper = await mountCards({ sessions: [firstSession, secondSessionSummary], events: [] })

    const cards = wrapper.findAll('[data-testid="active-session-card"]')
    expect(cards).toHaveLength(2)
    expect(cards[0]!.get('[data-testid="active-session-card-cost"]').text()).toBe('$4.27')
    expect(cards[1]!.get('[data-testid="active-session-card-cost"]').text()).toBe('$0.0031')
  })

  it('"current tool" renders — with a reason when no tool.* event has been seen for that session, rather than a fabricated value', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })

    const tool = wrapper.get('[data-testid="active-session-card-tool"]')
    expect(tool.text()).toBe('—')
    expect(tool.find('[title]').attributes('title')).toContain('No tool.* event observed')
  })

  it('"current tool" derives the most recent tool.* event\'s tool_name for that session, honestly (not a fabricated field)', async () => {
    const events = [
      makeTimelineEvent({ session_id: firstSession.id, kind: 'tool.pre', tool_name: 'Read' }),
      makeTimelineEvent({ session_id: firstSession.id, kind: 'tool.result', tool_name: 'Bash' }),
      // A different session's tool event must not leak onto firstSession's card.
      makeTimelineEvent({ session_id: secondSessionSummary.id, kind: 'tool.pre', tool_name: 'Edit' }),
    ]
    const wrapper = await mountCards({ sessions: [firstSession, secondSessionSummary], events })

    const cards = wrapper.findAll('[data-testid="active-session-card"]')
    expect(cards[0]!.get('[data-testid="active-session-card-tool"]').text()).toBe('Bash')
    expect(cards[1]!.get('[data-testid="active-session-card-tool"]').text()).toBe('Edit')
  })

  // Round-6 critic gap: "Cost"/"Current tool" used `justify-between` inside a
  // card that can span the full row width, flinging label and value ~1100px
  // apart. Each stat is now its own compact cell (label stacked directly
  // above its value, `SessionKpiStrip.vue`'s "detail KPI strip" idiom) —
  // asserted here by checking the label and value share the same immediate
  // parent element, which a full-width `justify-between` row never gives up
  // (there, the parent is the whole card-width flex row, not a tight cell).
  it('keeps each stat\'s label and value inside the same compact cell, not flung across a full-width row', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })

    const costValue = wrapper.get('[data-testid="active-session-card-cost"]')
    expect(costValue.element.parentElement?.textContent).toContain('Cost')

    const toolValue = wrapper.get('[data-testid="active-session-card-tool"]')
    expect(toolValue.element.parentElement?.textContent).toContain('Current tool')
  })

  // Round-6 critic gap: the same EM_DASH glyph rendered at three different
  // visual weights across the live view (this `NullValue` usage was one of
  // them — its default dotted-underline "hint text" styling, meant for a
  // null value sitting among real text, reads as a glitch on a lone glyph).
  it('renders "Current tool"\'s EM_DASH without NullValue\'s dotted-underline styling (plain)', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })
    const tool = wrapper.get('[data-testid="active-session-card-tool"]')

    expect(tool.text()).toBe('—')
    expect(tool.find('.underline').exists()).toBe(false)
  })

  it('the "follow" link points at /sessions/:id?live=1', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })
    const link = wrapper.get('[data-testid="active-session-card-follow"]')
    expect(link.attributes('href')).toBe(`/sessions/${firstSession.id}?live=1`)
  })

  // Round-5 critic gap: "the active-session card renders no session identity at all
  // (empty title row above 'Follow')". The row now carries the same identity block
  // SessionRow/SessionDetailView already establish: status, project, vendor, short id.
  // Round-7 critic gap: "Started" was dropped from the row entirely (not one of the
  // three metric columns this ticket asked for — last event, cost, current tool — and
  // a fourth column would undo the width win the round exists to deliver).
  it('renders the identity block — status dot, project, vendor, short id — and the last-event metric column', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })
    const card = wrapper.get('[data-testid="active-session-card"]')

    expect(card.find('[data-testid="status-dot"]').exists()).toBe(true)
    expect(card.get('[data-testid="active-session-card-title"]').text()).toBe(firstSession.project)
    expect(card.text()).toContain(firstSession.vendor)
    expect(card.get('[data-testid="active-session-card-short-id"]').text()).toBe(firstSession.id.slice(0, 8))
    expect(card.text()).toContain('Last event')
  })

  // D-31 widened `StreamSessionFrame` to the full `SessionSummary`, so `project` is genuinely
  // wired end to end — but it is only ever populated from a hook session.start/cwd_changed event
  // (server/internal/store/postgres/upsert_session.go), so a session still active on other
  // transports can legitimately arrive with `project: ''`. That must render an honest placeholder,
  // never blank space (the exact symptom the critic pixel-verified).
  it('a session with no project signal yet (project: "") shows a placeholder, never a blank title', async () => {
    const noProjectYet = { ...firstSession, project: '' }
    const wrapper = await mountCards({ sessions: [noProjectYet], events: [] })
    const title = wrapper.get('[data-testid="active-session-card-title"]')

    expect(title.text().trim()).not.toBe('')
    expect(title.text()).toContain('Unknown project')
  })

  it('the "follow" affordance renders as a button, not a bare link', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })
    const follow = wrapper.get('[data-testid="active-session-card-follow"]')
    expect(follow.attributes('data-slot')).toBe('button')
  })

  // Round-7 critic gap: multiple active sessions must stack as dense rows inside one
  // contained surface, not a responsive card grid (that grid was the ~85%-width-waste
  // culprit in the first place).
  it('renders multiple active sessions as stacked rows sharing one container, not a grid of cards', async () => {
    const wrapper = await mountCards({ sessions: [firstSession, secondSessionSummary], events: [] })
    const container = wrapper.get('[data-testid="active-session-cards"] > div')

    expect(container.classes()).not.toContain('grid')
    expect(wrapper.findAll('[data-testid="active-session-card"]')).toHaveLength(2)
  })
})
