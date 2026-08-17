import { describe, expect, it } from 'vitest'

import { hookToolResultEventClose, hookToolResultEventFar, makeTimelineEvent, otelToolResultEvent } from '@/test/fixtures.extra'
import { collapseEvents } from './collapseEvents'

describe('collapseEvents', () => {
  it('collapses an OTel and a hook tool.result 300ms apart with the same tool_use_id into one item listing 2 sources', () => {
    const items = collapseEvents([otelToolResultEvent, hookToolResultEventClose])
    expect(items).toHaveLength(1)
    expect(items[0]!.sources).toEqual(['otel_log', 'hook'])
    expect(items[0]!.events).toHaveLength(2)
  })

  it('does not collapse the same pair when they are 5s apart', () => {
    const items = collapseEvents([otelToolResultEvent, hookToolResultEventFar])
    expect(items).toHaveLength(2)
  })

  it('never collapses two different tool_use_ids, even within the window', () => {
    const a = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_A' })
    const b = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:00.100Z', tool_use_id: 'toolu_B' })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(2)
  })

  it('collapses events with no correlation key only on identical session_id + kind within the window', () => {
    const a = makeTimelineEvent({
      kind: 'hook.registered',
      source: 'otel_log',
      ts: '2026-08-14T01:00:00.000Z',
      tool_use_id: null,
      prompt_id: null,
      session_id: 'session-A',
    })
    const b = makeTimelineEvent({
      kind: 'hook.registered',
      source: 'hook',
      ts: '2026-08-14T01:00:01.000Z',
      tool_use_id: null,
      prompt_id: null,
      session_id: 'session-A',
    })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(1)
    expect(items[0]!.events).toHaveLength(2)
  })

  it('does not collapse no-correlation-key events with the same session_id but a different kind', () => {
    const a = makeTimelineEvent({ kind: 'hook.registered', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: null, prompt_id: null, session_id: 'session-A' })
    const b = makeTimelineEvent({ kind: 'hook.execution_start', source: 'hook', ts: '2026-08-14T01:00:00.100Z', tool_use_id: null, prompt_id: null, session_id: 'session-A' })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(2)
  })

  it('does not collapse no-correlation-key events across different sessions', () => {
    const a = makeTimelineEvent({ kind: 'hook.registered', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: null, prompt_id: null, session_id: 'session-A' })
    const b = makeTimelineEvent({ kind: 'hook.registered', source: 'hook', ts: '2026-08-14T01:00:00.100Z', tool_use_id: null, prompt_id: null, session_id: 'session-B' })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(2)
  })

  it('falls back to prompt_id when tool_use_id is absent on both events', () => {
    const a = makeTimelineEvent({ kind: 'turn.start', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: null, prompt_id: 'p_1' })
    const b = makeTimelineEvent({ kind: 'turn.start', source: 'hook', ts: '2026-08-14T01:00:00.500Z', tool_use_id: null, prompt_id: 'p_1' })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(1)
  })

  it('does not collapse across prompt_id vs no-prompt_id even if session_id matches', () => {
    const a = makeTimelineEvent({ kind: 'turn.start', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: null, prompt_id: 'p_1', session_id: 'session-A' })
    const b = makeTimelineEvent({ kind: 'turn.start', source: 'hook', ts: '2026-08-14T01:00:00.100Z', tool_use_id: null, prompt_id: null, session_id: 'session-A' })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(2)
  })

  it('collapse: false returns the input 1:1, untouched, in input order', () => {
    const events = [otelToolResultEvent, hookToolResultEventClose, hookToolResultEventFar]
    const items = collapseEvents(events, { collapse: false })
    expect(items).toHaveLength(3)
    items.forEach((item, i) => {
      expect(item.events).toEqual([events[i]])
      expect(item.key).toBe(events[i]!.event_ref)
    })
  })

  it('preserves input order across collapsed and non-collapsed groups alike', () => {
    // A (tool_use_id X), B (unrelated), C (joins A's group) — output item
    // order must reflect first-occurrence order: [group(A,C), item(B)].
    const a = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_X' })
    const b = makeTimelineEvent({ kind: 'tool.pre', source: 'otel_log', ts: '2026-08-14T01:00:00.200Z', tool_use_id: 'toolu_Y' })
    const c = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:00.400Z', tool_use_id: 'toolu_X' })
    const items = collapseEvents([a, b, c])
    expect(items).toHaveLength(2)
    expect(items[0]!.events).toEqual([a, c])
    expect(items[1]!.events).toEqual([b])
  })

  it('window defaults to 2000ms — exactly 2000ms apart collapses, 2001ms does not', () => {
    const a = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_edge' })
    const bExact = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:02.000Z', tool_use_id: 'toolu_edge' })
    const bOver = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:02.001Z', tool_use_id: 'toolu_edge' })
    expect(collapseEvents([a, bExact])).toHaveLength(1)
    expect(collapseEvents([a, bOver])).toHaveLength(2)
  })

  it('a custom window option is honoured', () => {
    const a = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_win' })
    const b = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:05.000Z', tool_use_id: 'toolu_win' })
    expect(collapseEvents([a, b], { window: 6000 })).toHaveLength(1)
    expect(collapseEvents([a, b], { window: 1000 })).toHaveLength(2)
  })

  it('a clock_skewed member never merges on ts, even at Δts=0', () => {
    const a = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_skew', clock_skewed: true })
    const b = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_skew', clock_skewed: false })
    const items = collapseEvents([a, b])
    expect(items).toHaveLength(2)
  })

  it('surfaces clock_skewed on the item — a skewed event can only ever be a singleton group under the never-merge-on-ts rule above', () => {
    const a = makeTimelineEvent({ kind: 'tool.pre', source: 'hook', ts: '2026-08-14T01:00:00.000Z', prompt_id: 'p_skew', tool_use_id: null, clock_skewed: true })
    const items = collapseEvents([a])
    expect(items).toHaveLength(1)
    expect(items[0]!.clock_skewed).toBe(true)
  })

  describe('field merge precedence (SPEC §1.5.3(a))', () => {
    it('decision/decision_source/tool_source: otel_log tool.decision beats otel_log tool.result beats hook', () => {
      const hook = makeTimelineEvent({
        kind: 'tool.decision',
        source: 'hook',
        ts: '2026-08-14T01:00:00.000Z',
        tool_use_id: 'toolu_prec',
        decision: 'reject',
        decision_source: 'hook',
        tool_source: 'builtin',
      })
      const otelResult = makeTimelineEvent({
        kind: 'tool.decision',
        source: 'otel_log',
        ts: '2026-08-14T01:00:00.100Z',
        tool_use_id: 'toolu_prec',
        decision: null,
        decision_source: null,
        tool_source: null,
      })
      const otelDecision = makeTimelineEvent({
        kind: 'tool.decision',
        source: 'otel_log',
        ts: '2026-08-14T01:00:00.200Z',
        tool_use_id: 'toolu_prec',
        decision: 'accept',
        decision_source: 'user_permanent',
        tool_source: 'mcp',
      })
      const items = collapseEvents([hook, otelResult, otelDecision])
      expect(items).toHaveLength(1)
      expect(items[0]!.decision).toBe('accept')
      expect(items[0]!.decision_source).toBe('user_permanent')
      expect(items[0]!.tool_source).toBe('mcp')
    })

    it('falls back to hook when no otel_log source carries the field', () => {
      const hook = makeTimelineEvent({ kind: 'tool.decision', source: 'hook', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_hookonly', decision: 'accept', decision_source: 'config' })
      const otelWithoutDecision = makeTimelineEvent({ kind: 'tool.decision', source: 'otel_log', ts: '2026-08-14T01:00:00.100Z', tool_use_id: 'toolu_hookonly', decision: null, decision_source: null })
      const items = collapseEvents([hook, otelWithoutDecision])
      expect(items[0]!.decision).toBe('accept')
      expect(items[0]!.decision_source).toBe('config')
    })

    it('duration_ms: otel_log tool.result wins over hook', () => {
      const items = collapseEvents([otelToolResultEvent, hookToolResultEventClose])
      expect(items[0]!.duration_ms).toBe(842)
    })

    it('success/error_type: otel_log wins over hook', () => {
      const otel = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_succ', success: true, error_type: null })
      const hook = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:00.100Z', tool_use_id: 'toolu_succ', success: false, error_type: 'permission_denied' })
      const items = collapseEvents([otel, hook])
      // otel_log's non-null `success` wins even though hook additionally
      // carries a richer error_type — each field is picked independently.
      expect(items[0]!.success).toBe(true)
      expect(items[0]!.error_type).toBe('permission_denied')
    })

    it('agent_id comes from the hook member — the only source that carries it (SPEC §1.9)', () => {
      const otel = makeTimelineEvent({ kind: 'tool.result', source: 'otel_log', ts: '2026-08-14T01:00:00.000Z', tool_use_id: 'toolu_agent', agent_id: null })
      const hook = makeTimelineEvent({ kind: 'tool.result', source: 'hook', ts: '2026-08-14T01:00:00.100Z', tool_use_id: 'toolu_agent', agent_id: 'agent-42' })
      const items = collapseEvents([otel, hook])
      expect(items[0]!.agent_id).toBe('agent-42')
    })
  })
})
