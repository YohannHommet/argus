import { describe, expect, it } from 'vitest'

import { collapseEvents } from './collapseEvents'
import { buildToolThreads } from './toolThreads'
import { makeTimelineEvent } from '@/test/fixtures.extra'

describe('buildToolThreads', () => {
  it('nests a tool.result under its tool.pre call via a shared tool_use_id', () => {
    const items = collapseEvents([
      makeTimelineEvent({ kind: 'tool.pre', tool_use_id: 'toolu_1', decision: null, ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ kind: 'tool.result', tool_use_id: 'toolu_1', decision: 'accept', decision_source: 'config', duration_ms: 120, ts: '2026-08-14T01:00:05.000Z' }),
    ])
    const nodes = buildToolThreads(items)

    expect(nodes).toHaveLength(1)
    expect(nodes[0]!.type).toBe('thread')
    if (nodes[0]!.type !== 'thread') throw new Error('unreachable')
    expect(nodes[0]!.thread.primary.kind).toBe('tool.pre')
    expect(nodes[0]!.thread.children).toHaveLength(1)
    expect(nodes[0]!.thread.children[0]!.kind).toBe('tool.result')
  })

  it('surfaces the decision and duration from the tool.result onto the thread display, for the parent row', () => {
    const items = collapseEvents([
      makeTimelineEvent({ kind: 'tool.pre', tool_use_id: 'toolu_1', decision: null, decision_source: null, duration_ms: null, ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ kind: 'tool.result', tool_use_id: 'toolu_1', decision: 'reject', decision_source: 'user_reject', duration_ms: 250, ts: '2026-08-14T01:00:05.000Z' }),
    ])
    const [node] = buildToolThreads(items)
    if (node!.type !== 'thread') throw new Error('unreachable')

    expect(node.thread.display.decision).toBe('reject')
    expect(node.thread.display.decision_source).toBe('user_reject')
    expect(node.thread.display.duration_ms).toBe(250)
  })

  it('prefers tool.decision over tool.result when both carry a decision', () => {
    const items = collapseEvents([
      makeTimelineEvent({ kind: 'tool.pre', tool_use_id: 'toolu_1', decision: null, ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ kind: 'tool.decision', tool_use_id: 'toolu_1', decision: 'accept', decision_source: 'hook', ts: '2026-08-14T01:00:01.000Z' }),
      makeTimelineEvent({ kind: 'tool.result', tool_use_id: 'toolu_1', decision: 'accept', decision_source: 'sdk', ts: '2026-08-14T01:00:05.000Z' }),
    ])
    const [node] = buildToolThreads(items)
    if (node!.type !== 'thread') throw new Error('unreachable')

    expect(node.thread.display.decision_source).toBe('hook')
  })

  it('renders a lone item (no correlating sibling) as a single node, not a one-child thread', () => {
    const items = collapseEvents([makeTimelineEvent({ kind: 'tool.pre', tool_use_id: 'toolu_lonely' })])
    const nodes = buildToolThreads(items)
    expect(nodes).toEqual([{ type: 'single', item: items[0] }])
  })

  it('items with no tool_use_id are always single nodes, never grouped with each other', () => {
    // Distinct prompt_id (and >2s apart) so collapseEvents' own
    // correlation fallback (tool_use_id, else prompt_id, else session_id)
    // doesn't fold these two into one collapsed item before nesting ever
    // sees them — this test is about buildToolThreads, not collapseEvents.
    const items = collapseEvents([
      makeTimelineEvent({ kind: 'llm.request', tool_use_id: null, prompt_id: 'p_a', ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ kind: 'llm.request', tool_use_id: null, prompt_id: 'p_b', ts: '2026-08-14T01:00:10.000Z' }),
    ])
    const nodes = buildToolThreads(items)
    expect(nodes).toEqual([
      { type: 'single', item: items[0] },
      { type: 'single', item: items[1] },
    ])
  })

  it('preserves top-level order: a thread is emitted at its first member\'s position', () => {
    const items = collapseEvents([
      makeTimelineEvent({ kind: 'llm.request', tool_use_id: null, ts: '2026-08-14T00:59:00.000Z' }),
      makeTimelineEvent({ kind: 'tool.pre', tool_use_id: 'toolu_1', ts: '2026-08-14T01:00:00.000Z' }),
      makeTimelineEvent({ kind: 'tool.result', tool_use_id: 'toolu_1', ts: '2026-08-14T01:00:05.000Z' }),
      makeTimelineEvent({ kind: 'llm.request', tool_use_id: null, ts: '2026-08-14T01:00:10.000Z' }),
    ])
    const nodes = buildToolThreads(items)
    expect(nodes.map((n) => n.type)).toEqual(['single', 'thread', 'single'])
  })
})
