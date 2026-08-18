import { describe, expect, it } from 'vitest'

import { ALL_KINDS, EVENT_KIND_META, eventKindMeta } from './eventKinds'
import type { Kind } from './eventKinds'

// The 43-value `Kind` union as it stood when this ticket (P4-04) was
// implemented — a fixed literal list (rather than iterating `ALL_KINDS`,
// which is itself derived from `EVENT_KIND_META`'s own keys and so could
// not catch a key silently dropped from the map). Cross-checking against
// this independent list is what makes the "every Kind" AC structural: if
// `Kind` grows, `Record<Kind, EventKindMeta>` fails to compile until this
// map (and, deliberately, this list) is updated too.
const EXPECTED_KINDS: Kind[] = [
  'session.start',
  'session.end',
  'turn.start',
  'turn.end',
  'turn.prompt_expanded',
  'llm.request',
  'llm.error',
  'llm.refusal',
  'llm.request_body',
  'llm.response_body',
  'assistant.message',
  'tool.pre',
  'tool.decision',
  'tool.permission_request',
  'tool.result',
  'tool.batch',
  'subagent.start',
  'subagent.stop',
  'task.created',
  'task.completed',
  'permission.mode_changed',
  'hook.registered',
  'hook.execution_start',
  'hook.execution_end',
  'fs.file_changed',
  'workspace.cwd_changed',
  'workspace.directory_added',
  'workspace.config_changed',
  'workspace.instructions_loaded',
  'workspace.worktree_created',
  'workspace.worktree_removed',
  'context.compact_start',
  'context.compact_end',
  'mcp.connection',
  'mcp.elicitation',
  'mcp.elicitation_result',
  'agent.auth',
  'agent.setup',
  'agent.plugin',
  'agent.internal_error',
  'agent.notification',
  'agent.idle',
  'unknown',
]

describe('eventKinds', () => {
  it('has exactly the 43 documented Kind values, no more, no fewer', () => {
    expect(ALL_KINDS.slice().sort()).toEqual(EXPECTED_KINDS.slice().sort())
    expect(ALL_KINDS).toHaveLength(43)
  })

  it('gives every Kind a non-empty label — a Record<Kind,…> can be satisfied by a blank placeholder, this catches that', () => {
    for (const kind of EXPECTED_KINDS) {
      expect(EVENT_KIND_META[kind].label.trim().length).toBeGreaterThan(0)
    }
  })

  it('gives every Kind an icon component', () => {
    for (const kind of EXPECTED_KINDS) {
      expect(EVENT_KIND_META[kind].icon).toBeTruthy()
    }
  })

  it('has entries for the three hook.* kinds specifically (PLAN.md P4-04 AC)', () => {
    expect(eventKindMeta('hook.registered').label).toBe('Hook registered')
    expect(eventKindMeta('hook.execution_start').label).toBe('Hook execution start')
    expect(eventKindMeta('hook.execution_end').label).toBe('Hook execution end')
  })

  it('has an entry for unknown', () => {
    expect(eventKindMeta('unknown').label).toBe('Unknown')
  })

  it('gives every Kind a distinct label', () => {
    const labels = EXPECTED_KINDS.map((k) => EVENT_KIND_META[k].label)
    expect(new Set(labels).size).toBe(labels.length)
  })
})
