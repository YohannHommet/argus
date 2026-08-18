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

  it('the "follow" link points at /sessions/:id?live=1', async () => {
    const wrapper = await mountCards({ sessions: [firstSession], events: [] })
    const link = wrapper.get('[data-testid="active-session-card-follow"]')
    expect(link.attributes('href')).toBe(`/sessions/${firstSession.id}?live=1`)
  })
})
