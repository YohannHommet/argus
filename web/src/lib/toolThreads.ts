/**
 * Groups a turn's already-collapsed `TimelineItem`s into "tool threads":
 * a tool call (`tool.pre`) and its decision/permission/result — all sharing
 * one `tool_use_id` — rendered as one parent row with its children nested
 * underneath, instead of as unrelated flat siblings.
 *
 * This is deliberately a *second*, display-only grouping pass on top of
 * `collapseEvents`, not a change to it: `collapseEvents` only merges
 * members of the *same* `kind` (duplicate telemetry for one occurrence —
 * SPEC §1.5.3(b)), so a `tool.pre` and its `tool.result` are, correctly,
 * two different `TimelineItem`s even after collapsing. Nesting them is a
 * presentation concern (SPEC's span-tree idiom, critic gap: "tool
 * calls/results don't read as children"), not a correlation-key merge, so
 * it stays out of the pure collapse function and its "highest-value test in
 * the project" contract.
 *
 * `display` folds the decision/duration/cost/tokens/success worth showing
 * on the *parent* row across the whole thread (SPEC §1.5.3(a)'s own
 * precedence, restated at item granularity): a decision recorded by
 * `tool.decision` or `tool.result` must be visible on the call row itself,
 * not buried one click down in a child nobody expands.
 */
import type { TimelineItem, TokenUsage } from './collapseEvents'

export interface ToolThreadDisplay {
  decision: string | null
  decision_source: string | null
  duration_ms: number | null
  cost: number | null
  tokens: TokenUsage | null
  success: boolean | null
  error_type: string | null
}

export interface ToolThread {
  key: string
  primary: TimelineItem
  /** Members other than `primary`, in original (chronological) order. */
  children: TimelineItem[]
  display: ToolThreadDisplay
}

export type RenderNode = { type: 'thread'; thread: ToolThread } | { type: 'single'; item: TimelineItem }

/** Higher wins when picking which member's field to surface on the parent row. tool.decision is the authoritative source of a decision; tool.result is next-best (it also carries the agent-measured duration); everything else (tool.pre, tool.permission_request, hook-derived items) ranks lowest. */
function kindRank(kind: TimelineItem['kind']): number {
  if (kind === 'tool.decision') return 2
  if (kind === 'tool.result') return 1
  return 0
}

function pickDisplayField<T>(members: TimelineItem[], get: (item: TimelineItem) => T | null): T | null {
  let best: { rank: number; value: T } | null = null
  for (const item of members) {
    const value = get(item)
    if (value === null || value === undefined) continue
    const rank = kindRank(item.kind)
    if (!best || rank > best.rank) best = { rank, value }
  }
  return best ? best.value : null
}

function computeDisplay(members: TimelineItem[]): ToolThreadDisplay {
  return {
    decision: pickDisplayField(members, (m) => m.decision),
    decision_source: pickDisplayField(members, (m) => m.decision_source),
    duration_ms: pickDisplayField(members, (m) => m.duration_ms),
    cost: pickDisplayField(members, (m) => m.cost),
    tokens: pickDisplayField(members, (m) => m.tokens),
    success: pickDisplayField(members, (m) => m.success),
    error_type: pickDisplayField(members, (m) => m.error_type),
  }
}

/**
 * Builds the render list for one turn's items. Order is preserved: a
 * thread is emitted at the position of its first member's occurrence, and
 * a lone item (no `tool_use_id`, or the only item carrying it) renders as
 * `{ type: 'single' }` — identical to today's flat row, so a thread of one
 * costs nothing over the old rendering.
 */
export function buildToolThreads(items: TimelineItem[]): RenderNode[] {
  const membersByToolUseId = new Map<string, TimelineItem[]>()
  for (const item of items) {
    if (item.tool_use_id === null) continue
    const members = membersByToolUseId.get(item.tool_use_id) ?? []
    members.push(item)
    membersByToolUseId.set(item.tool_use_id, members)
  }

  const consumed = new Set<string>()
  const nodes: RenderNode[] = []
  for (const item of items) {
    const toolUseId = item.tool_use_id
    if (toolUseId === null) {
      nodes.push({ type: 'single', item })
      continue
    }
    if (consumed.has(toolUseId)) continue
    consumed.add(toolUseId)

    const members = membersByToolUseId.get(toolUseId)!
    if (members.length === 1) {
      nodes.push({ type: 'single', item: members[0]! })
      continue
    }

    const primary = members.find((m) => m.kind === 'tool.pre') ?? members[0]!
    const children = members.filter((m) => m !== primary)
    nodes.push({
      type: 'thread',
      thread: { key: toolUseId, primary, children, display: computeDisplay(members) },
    })
  }
  return nodes
}
