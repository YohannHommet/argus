import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { beforeEach, describe, expect, it } from 'vitest'

import SubagentTree from './SubagentTree.vue'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import {
  getSessionSubagentsDepth2Live,
  getSessionSubagentsFiftyNodes,
} from '@/test/fixtures.extra'
import { listSessionToolCalls200Default } from '@/test/fixtures'

async function mountTree(props: Record<string, unknown>): Promise<{ router: Router; wrapper: ReturnType<typeof mount> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/sessions/:id', name: 'session-detail', component: { template: '<div />' } }],
  })
  await router.push('/sessions/3f7a3b1e-0000-0000-0000-000000000001')
  await router.isReady()

  const wrapper = mount(SubagentTree, {
    props,
    global: { plugins: [router] },
  })
  return { router, wrapper }
}

describe('SubagentTree', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('renders a loading skeleton, not the tree or an empty state, while loading', async () => {
    const { wrapper } = await mountTree({ nodes: [], loading: true })

    expect(wrapper.find('[data-testid="subagent-tree-loading"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="subagent-node"]').exists()).toBe(false)
  })

  it('renders ErrorState on error and re-emits retry', async () => {
    const { wrapper } = await mountTree({ nodes: [], error: new Error('boom') })

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('[data-testid="error-state"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders EmptyState for a session with no subagents (the common case)', async () => {
    const { wrapper } = await mountTree({ nodes: [] })

    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('renders the live depth-2 fixture: root + 2 children', async () => {
    const { wrapper } = await mountTree({ nodes: getSessionSubagentsDepth2Live.data })

    expect(wrapper.findAll('[data-testid="subagent-node"]')).toHaveLength(3)
  })

  it('renders a 50-node fixture in full via the tree entrypoint', async () => {
    const { wrapper } = await mountTree({ nodes: getSessionSubagentsFiftyNodes.data })

    expect(wrapper.findAll('[data-testid="subagent-node"]')).toHaveLength(50)
  })

  it('clicking a node navigates to ?tab=timeline&agent_id=… and the store applies the filter', async () => {
    const store = useSessionDetailStore()
    expect(store.agentId).toBeNull()

    const { router, wrapper } = await mountTree({ nodes: getSessionSubagentsDepth2Live.data })

    await wrapper.get('[data-agent-id="agent-107d2cba-explore-1"] [data-testid="subagent-node-row"]').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query.tab).toBe('timeline')
    expect(router.currentRoute.value.query.agent_id).toBe('agent-107d2cba-explore-1')
    expect(store.agentId).toBe('agent-107d2cba-explore-1')
    expect(wrapper.emitted('select-agent')).toEqual([['agent-107d2cba-explore-1']])
  })

  it('passes cost_attribution.note through to node cost tooltips', async () => {
    const { wrapper } = await mountTree({
      nodes: getSessionSubagentsDepth2Live.data,
      costNote: getSessionSubagentsDepth2Live.cost_attribution.note,
    })

    const trigger = wrapper.get('[data-testid="subagent-node-cost"] [title]')
    expect(trigger.attributes('title')).toBe(getSessionSubagentsDepth2Live.cost_attribution.note)
  })

  // Round-5 critic gap: "the Subagents duration scale reads '0-4ms' —
  // meaningless at these magnitudes". The live fixture's own spread (root
  // ~213ms, both children well under a second) is exactly that case: a
  // legend/bar chart comparing sub-second values is noise, not signal, so
  // both the scale legend and every node's comparative bar should be
  // withheld — the duration text itself stays (SubagentNode.test.ts covers
  // that unconditionally).
  it('hides the duration-scale legend and every node\'s duration bar when the tree\'s max duration is sub-second (spread not meaningful)', async () => {
    const { wrapper } = await mountTree({ nodes: getSessionSubagentsDepth2Live.data })

    expect(wrapper.find('[data-testid="subagent-tree-duration-scale"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="subagent-node-duration-track"]').exists()).toBe(false)
  })

  it('shows the duration-scale legend and duration bars once the tree\'s max duration crosses one second', async () => {
    const nodes = [
      {
        ...getSessionSubagentsDepth2Live.data[0],
        started_at: '2026-08-17T13:00:20.000000Z',
        ended_at: '2026-08-17T13:00:23.000000Z',
      },
    ]
    const { wrapper } = await mountTree({ nodes })

    expect(wrapper.get('[data-testid="subagent-tree-duration-scale"]').text()).toContain('3s')
    expect(wrapper.find('[data-testid="subagent-node-duration-track"]').exists()).toBe(true)
  })

  // Round-6 critic gap ("tool-breakdown"): SubagentTree, not SubagentNode,
  // owns turning the session's flat ToolCall list into a per-agent_id
  // breakdown map (grouping logic belongs at the tree level so every node
  // computes it once, not once per node in the recursion).
  it('groups toolCalls by agent_id into a per-node tool-name breakdown, ignoring calls with a null agent_id', async () => {
    const toolCalls = [
      { ...listSessionToolCalls200Default.data[0]!, tool_name: 'Read', agent_id: 'agent-107d2cba-explore-1' },
      { ...listSessionToolCalls200Default.data[0]!, tool_name: 'Read', agent_id: 'agent-107d2cba-explore-1' },
      { ...listSessionToolCalls200Default.data[0]!, tool_name: 'Bash', agent_id: 'agent-107d2cba-explore-1' },
      { ...listSessionToolCalls200Default.data[0]!, tool_name: 'Edit', agent_id: null },
    ]
    const { wrapper } = await mountTree({ nodes: getSessionSubagentsDepth2Live.data, toolCalls })

    const breakdown = wrapper.get('[data-agent-id="agent-107d2cba-explore-1"] [data-testid="subagent-node-tool-breakdown"]')
    expect(breakdown.attributes('title')).toBe('Read×2, Bash×1')
    // The null-agent_id Edit call is unattributable and must not leak onto any node.
    expect(wrapper.find('[data-agent-id="agent-e090ede7-explore-2"] [data-testid="subagent-node-tool-breakdown"]').exists()).toBe(false)
  })
})
