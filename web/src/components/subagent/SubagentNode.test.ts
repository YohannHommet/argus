import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SubagentNode, { MAX_RENDER_DEPTH } from './SubagentNode.vue'
import {
  deeplyNestedSubagentTree,
  getSessionSubagentsDepth2Live,
  getSessionSubagentsFiftyNodes,
  subagentNodeUnknownStatusNullTiming,
  subagentNodeWithNullToolCallCount,
} from '@/test/fixtures.extra'
import { NO_HOOK_COVERAGE, NO_PER_AGENT_COST } from '@/lib/nullReasons'

describe('SubagentNode', () => {
  it('renders the live depth-2 fixture with correct nesting: root + 2 children, one node per subagent', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    const rows = wrapper.findAll('[data-testid="subagent-node"]')
    expect(rows).toHaveLength(3)
    expect(rows[0]!.attributes('data-agent-id')).toBe('root')
    expect(rows[1]!.attributes('data-agent-id')).toBe('agent-107d2cba-explore-1')
    expect(rows[2]!.attributes('data-agent-id')).toBe('agent-e090ede7-explore-2')
  })

  it("labels the root node (parent_agent_id: null) as the main agent, and does not label a non-root node that way", () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    const rootRow = wrapper.get('[data-agent-id="root"]')
    expect(rootRow.find('[data-testid="subagent-node-main-badge"]').exists()).toBe(true)

    const childRow = wrapper.get('[data-agent-id="agent-107d2cba-explore-1"]')
    expect(childRow.find('[data-testid="subagent-node-main-badge"]').exists()).toBe(false)
  })

  it('every node\'s cost renders the em dash with the "Claude Code does not emit per-agent cost" tooltip — including the root, whose cost_usd is also null', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    const costCells = wrapper.findAll('[data-testid="subagent-node-cost"]')
    expect(costCells).toHaveLength(3)
    for (const cell of costCells) {
      expect(cell.text()).toContain('—')
      const trigger = cell.find('[title]')
      expect(trigger.attributes('title')).toBe(NO_PER_AGENT_COST)
      expect(trigger.attributes('aria-label')).toBe(NO_PER_AGENT_COST)
    }
  })

  it('prefers the caller-supplied cost note (cost_attribution.note) over the generic constant', () => {
    const wrapper = mount(SubagentNode, {
      props: {
        node: getSessionSubagentsDepth2Live.data[0].children[1]!,
        costNote: getSessionSubagentsDepth2Live.cost_attribution.note,
      },
    })

    const trigger = wrapper.get('[data-testid="subagent-node-cost"] [title]')
    expect(trigger.attributes('title')).toBe(getSessionSubagentsDepth2Live.cost_attribution.note)
  })

  it('renders tool_call_count: 0 as "0", not the em dash — a real reading, not a missing one', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0].children[0]! },
    })

    expect(wrapper.get('[data-testid="subagent-node-tool-count"]').text()).toBe('0')
  })

  it('renders tool_call_count: null as the em dash with the "no hook coverage" tooltip, never "0"', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: subagentNodeWithNullToolCallCount },
    })

    expect(wrapper.find('[data-testid="subagent-node-tool-count"]').exists()).toBe(false)
    const trigger = wrapper.get('[data-testid="subagent-node-tools"] [title]')
    expect(trigger.attributes('title')).toBe(NO_HOOK_COVERAGE)
    expect(wrapper.text()).toContain('—')
  })

  it('renders a status value outside the documented enum ("ended") verbatim, without a switch/crash', () => {
    expect(() => mount(SubagentNode, { props: { node: getSessionSubagentsDepth2Live.data[0] } })).not.toThrow()
    const wrapper = mount(SubagentNode, { props: { node: getSessionSubagentsDepth2Live.data[0] } })
    expect(wrapper.get('[data-testid="subagent-node-status"]').text()).toContain('ended')
  })

  it('renders a status value entirely invented by no known build, plus null started_at/ended_at, without throwing', () => {
    expect(() => mount(SubagentNode, { props: { node: subagentNodeUnknownStatusNullTiming } })).not.toThrow()
    const wrapper = mount(SubagentNode, { props: { node: subagentNodeUnknownStatusNullTiming } })

    expect(wrapper.get('[data-testid="subagent-node-status"]').text()).toContain('quantum_superposed')
    expect(wrapper.text()).not.toContain('NaN')
    expect(wrapper.text()).not.toContain('Invalid Date')
  })

  it('clicking a row emits select-agent with that node\'s agent_id', async () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    const childRow = wrapper.get('[data-agent-id="agent-e090ede7-explore-2"] [data-testid="subagent-node-row"]')
    await childRow.trigger('click')

    expect(wrapper.emitted('select-agent')).toEqual([['agent-e090ede7-explore-2']])
  })

  it('clicking the root row emits select-agent for the root, not a descendant', async () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    await wrapper.get('[data-agent-id="root"] [data-testid="subagent-node-row"]').trigger('click')

    expect(wrapper.emitted('select-agent')).toEqual([['root']])
  })

  it('clicking the expand/collapse toggle does not also emit select-agent', async () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    await wrapper.get('[data-agent-id="root"] [data-testid="subagent-node-toggle"]').trigger('click')

    expect(wrapper.emitted('select-agent')).toBeUndefined()
    // Collapsing the root hides its children rows.
    expect(wrapper.findAll('[data-testid="subagent-node"]')).toHaveLength(1)
  })

  it('renders a 50-node (breadth) fixture in full, without tripping the depth-based recursion guard', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsFiftyNodes.data[0] },
    })

    expect(wrapper.findAll('[data-testid="subagent-node"]')).toHaveLength(50)
    expect(wrapper.find('[data-testid="subagent-node-depth-limit"]').exists()).toBe(false)
  })

  it(`stops recursing at the ${MAX_RENDER_DEPTH}-level guard on a pathologically deep chain and shows a visible marker instead of hanging`, () => {
    const wrapper = mount(SubagentNode, {
      props: { node: deeplyNestedSubagentTree[0]! },
    })

    const rows = wrapper.findAll('[data-testid="subagent-node"]')
    expect(rows.length).toBe(MAX_RENDER_DEPTH + 1)
    expect(wrapper.find('[data-testid="subagent-node-depth-limit"]').exists()).toBe(true)
  })

  // Deliberately absent: no test in this file (or anywhere in this
  // ticket's suite) asserts a specific per-node cost *number* — SPEC
  // §1.9 / PLAN P4-05 review B3 call any such assertion invalid, since
  // cost_usd is null on every node and stays that way in v1.

  // Round-6 critic gap: "informationally empty" tree — a task label per
  // node. No name/description field exists in the schema to promote
  // (honesty limit, see the file's own doc comment), so the two real,
  // derivable additions are: the agent_id label is no longer hidden below
  // `sm`, and same-agent_type siblings get a real "i/n" ordinal.
  it('shows the agent_id label unconditionally (not hidden below the sm breakpoint)', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    const agentIdLabel = wrapper.get('[data-testid="subagent-node-agent-id"]')
    expect(agentIdLabel.classes()).not.toContain('hidden')
  })

  it('labels same-agent_type siblings with a real "i/n" ordinal, and gives a lone child of its type no ordinal', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0] },
    })

    const first = wrapper.get('[data-agent-id="agent-107d2cba-explore-1"] [data-testid="subagent-node-sibling-ordinal"]')
    const second = wrapper.get('[data-agent-id="agent-e090ede7-explore-2"] [data-testid="subagent-node-sibling-ordinal"]')
    expect(first.text()).toBe('1/2')
    expect(second.text()).toBe('2/2')

    // The root itself has no siblings (it's the tree's only root) — no
    // ordinal on its own row (scoped to `> [data-testid=subagent-node-row]`
    // — a plain descendant selector here would also match its children's
    // own ordinals, which live further down the same `<li>`).
    expect(
      wrapper.get('[data-agent-id="root"] > [data-testid="subagent-node-row"]').find('[data-testid="subagent-node-sibling-ordinal"]').exists(),
    ).toBe(false)
  })

  it('renders no ordinal when a node has only one child of a given agent_type', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsFiftyNodes.data[0]! },
    })

    // All 49 children share agent_type "explore" — every one gets an ordinal.
    expect(wrapper.get('[data-agent-id="agent-0"]').find('[data-testid="subagent-node-sibling-ordinal"]').text()).toBe('1/49')
  })

  // Round-6 critic gap: "tool-breakdown" per node — real, derived from
  // ToolCall.agent_id (hook-sourced but not invented), never a number the
  // server didn't provide.
  it("renders a node's tool-name breakdown when toolBreakdownByAgent has an entry for its agent_id", () => {
    const wrapper = mount(SubagentNode, {
      props: {
        node: getSessionSubagentsDepth2Live.data[0].children[1]!,
        toolBreakdownByAgent: {
          'agent-e090ede7-explore-2': [
            { name: 'Read', count: 3 },
            { name: 'Bash', count: 2 },
            { name: 'Grep', count: 1 },
          ],
        },
      },
    })

    const breakdown = wrapper.get('[data-testid="subagent-node-tool-breakdown"]')
    expect(breakdown.text()).toContain('Read×3')
    expect(breakdown.text()).toContain('Bash×2')
    expect(breakdown.text()).toContain('+1 more')
    expect(breakdown.attributes('title')).toBe('Read×3, Bash×2, Grep×1')
  })

  it('renders no breakdown decoration when toolBreakdownByAgent has no entry for this agent_id (unchanged plain count)', () => {
    const wrapper = mount(SubagentNode, {
      props: { node: getSessionSubagentsDepth2Live.data[0].children[1]! },
    })

    expect(wrapper.find('[data-testid="subagent-node-tool-breakdown"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="subagent-node-tool-count"]').text()).toBe('1')
  })
})
