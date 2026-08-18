/**
 * `collapseEvents` — SPEC §1.5.3(b), described in SPEC §6.3 as "the
 * highest-value frontend test in the project". A pure function, no Vue
 * imports: the timeline endpoint always returns raw events (server-side
 * `?collapse=` is reserved and ignored per D-24) and this is the client
 * that turns them into display rows.
 *
 * Collapse rule: a group is one row when, for every pair of members, all
 * of — same `kind`, same correlation key (`tool_use_id`, else `prompt_id`,
 * else `session_id`), and `|Δts| ≤ window` ms (default 2000). The row
 * exposes the union of fields per SPEC §1.5.3(a)'s precedence and the raw
 * member events for the "N sources" affordance.
 *
 * Design decisions (see PLAN.md P4-04 for the questions this answers):
 *
 * - **Δts is measured against the group's first (anchor) member**, not the
 *   previous member. Anchoring to the previous member lets a chain of
 *   events each ≤2000ms apart from its neighbour drift arbitrarily far from
 *   where the group started (SPEC's own example: three events 1.5s apart
 *   are 4.5s apart end-to-end). The window is meant to bound how stale a
 *   *collapsed row* can be, not how long a chain can grow one hop at a
 *   time, so every candidate is compared to the group's first member.
 * - **`clock_skewed` events never merge on the basis of `ts`.** A skewed
 *   clock makes `|Δts|` meaningless — trusting it either way (permissive or
 *   strict) fabricates a signal that isn't there. If either the anchor or
 *   the candidate is `clock_skewed`, the window check fails and the event
 *   starts (or stays in) its own group. This keeps a clock-skewed event
 *   visible on its own, on the conservative assumption that an operator
 *   debugging a skew issue wants to see it raw, not folded into a
 *   neighbour by a rule that just admitted it can't trust the timestamp.
 * - **Grouping is not limited to adjacent events.** A matching correlation
 *   key can appear non-adjacently in the input (interleaved sources), so a
 *   candidate event is checked against every currently-open group, not
 *   just the most recently created one.
 * - **Ordering is preserved everywhere**: this is a single forward pass
 *   (no sort), and both the item list (ordered by each group's first
 *   occurrence) and each item's own `events` array (input order) reflect
 *   the input order exactly. `collapse: false` returns one item per input
 *   event, 1:1, in input order — untouched.
 */
import type { components } from '@/api/schema'

export type TimelineEvent = components['schemas']['TimelineEvent']
export type EventSource = components['schemas']['EventSource']
export type Kind = components['schemas']['Kind']
export type TokenUsage = components['schemas']['TokenUsage']

export interface TimelineItem {
  /** Stable, unique key — the anchor (first) member's `event_ref`. */
  key: string
  kind: Kind
  /** Anchor member's timestamp — the group's earliest-seen ts. */
  ts: string
  session_id: string
  prompt_id: string | null
  event_name: string
  vendor: string
  tool_name: string | null
  tool_use_id: string | null
  decision: string | null
  decision_source: string | null
  tool_source: string | null
  query_source: string | null
  model: string | null
  tokens: TokenUsage | null
  cost: number | null
  duration_ms: number | null
  success: boolean | null
  error_type: string | null
  agent_id: string | null
  agent_type: string | null
  permission_mode: string | null
  file_path: string | null
  /** True if *any* member is clock_skewed. */
  clock_skewed: boolean
  /** Deduped member sources, first-seen order — the "N sources" affordance. */
  sources: EventSource[]
  /** Raw member events, input order. */
  events: TimelineEvent[]
}

export interface CollapseOptions {
  /** Max |Δts| in ms from the group's anchor member for a candidate to join. Default 2000 (SPEC §1.5.3(b)). */
  window?: number
  /** `false` disables collapsing entirely — display-only, reversible (SPEC §1.5.3(b), `?collapse=false`). Default true. */
  collapse?: boolean
}

/** SPEC §1.5.3(a): otel_log=30, hook=20, otel_metric=10; sim = rank of the source it imitates. */
const SOURCE_RANK: Record<EventSource, number> = {
  otel_log: 30,
  hook: 20,
  otel_metric: 10,
  // TimelineEvent carries no field naming which real source a `sim` event
  // imitates, so this implementation cannot honour "sim = rank of the
  // source it imitates" literally. Documented deviation: `sim` ranks below
  // every real source, so a genuine telemetry reading always wins over a
  // simulated stand-in when both are present in a group.
  sim: 0,
}

function isNonNull<T>(v: T | null | undefined): v is T {
  return v !== null && v !== undefined
}

/**
 * Highest-ranked (by `rank`) non-null value across members; ties keep the
 * earliest member (input order).
 *
 * Generic over the member type because `toolThreads.ts` runs the same argmax
 * over `TimelineItem`s with its own `kindRank`: the precedence *tables*
 * differ per caller, the "surface the best-sourced non-null field" loop does
 * not, and two copies of it would be free to drift on tie-breaking.
 */
export function pickByRank<M, T>(members: readonly M[], get: (m: M) => T | null | undefined, rank: (m: M) => number): T | null {
  let best: { rank: number; value: T } | null = null
  for (const m of members) {
    const v = get(m)
    if (!isNonNull(v)) continue
    const r = rank(m)
    if (!best || r > best.rank) best = { rank: r, value: v }
  }
  return best ? best.value : null
}

const genericRank = (e: TimelineEvent) => SOURCE_RANK[e.source]

/** decision / decision_source / tool_source: otel_log/tool.decision > otel_log/tool.result > hook > (rest, by generic rank). Only tool.decision carries the authoritative 6-valued decision_source (SPEC §1.5). */
function decisionRank(e: TimelineEvent): number {
  if (e.source === 'otel_log' && e.kind === 'tool.decision') return 1000
  if (e.source === 'otel_log' && e.kind === 'tool.result') return 900
  if (e.source === 'hook') return 800
  return genericRank(e)
}

/** duration_ms: otel_log's tool.result wins (measured by the agent) over everything else. */
function durationRank(e: TimelineEvent): number {
  if (e.source === 'otel_log' && e.kind === 'tool.result') return 1000
  return genericRank(e)
}

/** success / error_type: otel_log > hook (richer error vocabulary) > rest. */
function successRank(e: TimelineEvent): number {
  if (e.source === 'otel_log') return 1000
  return genericRank(e)
}

function mergeFields(members: TimelineEvent[]): Omit<TimelineItem, 'key' | 'sources' | 'events'> {
  const anchor = members[0]!
  return {
    kind: anchor.kind,
    ts: anchor.ts,
    session_id: anchor.session_id,
    prompt_id: anchor.prompt_id,
    event_name: anchor.event_name,
    vendor: anchor.vendor,
    tool_name: pickByRank(members, (e) => e.tool_name, genericRank),
    tool_use_id: pickByRank(members, (e) => e.tool_use_id, genericRank),
    decision: pickByRank(members, (e) => e.decision, decisionRank),
    decision_source: pickByRank(members, (e) => e.decision_source, decisionRank),
    tool_source: pickByRank(members, (e) => e.tool_source, decisionRank),
    query_source: pickByRank(members, (e) => e.query_source, genericRank),
    model: pickByRank(members, (e) => e.model, genericRank),
    tokens: pickByRank(members, (e) => e.tokens, genericRank),
    cost: pickByRank(members, (e) => e.cost, genericRank),
    duration_ms: pickByRank(members, (e) => e.duration_ms, durationRank),
    success: pickByRank(members, (e) => e.success, successRank),
    error_type: pickByRank(members, (e) => e.error_type, successRank),
    // agent_id: hook-only field (SPEC §1.9) — no other source ever carries
    // it, so a plain rank-based pick is equivalent to "the hook member's
    // value" without needing a bespoke priority function.
    agent_id: pickByRank(members, (e) => e.agent_id, genericRank),
    agent_type: pickByRank(members, (e) => e.agent_type, genericRank),
    permission_mode: pickByRank(members, (e) => e.permission_mode, genericRank),
    file_path: pickByRank(members, (e) => e.file_path, genericRank),
    clock_skewed: members.some((e) => e.clock_skewed),
  }
}

function toItem(members: TimelineEvent[]): TimelineItem {
  const anchor = members[0]!
  const sources: EventSource[] = []
  for (const e of members) {
    if (!sources.includes(e.source)) sources.push(e.source)
  }
  return {
    key: anchor.event_ref,
    sources,
    events: members,
    ...mergeFields(members),
  }
}

function singleItem(event: TimelineEvent): TimelineItem {
  return {
    key: event.event_ref,
    kind: event.kind,
    ts: event.ts,
    session_id: event.session_id,
    prompt_id: event.prompt_id,
    event_name: event.event_name,
    vendor: event.vendor,
    tool_name: event.tool_name,
    tool_use_id: event.tool_use_id,
    decision: event.decision,
    decision_source: event.decision_source,
    tool_source: event.tool_source,
    query_source: event.query_source,
    model: event.model,
    tokens: event.tokens,
    cost: event.cost,
    duration_ms: event.duration_ms,
    success: event.success,
    error_type: event.error_type,
    agent_id: event.agent_id,
    agent_type: event.agent_type,
    permission_mode: event.permission_mode,
    file_path: event.file_path,
    clock_skewed: event.clock_skewed,
    sources: [event.source],
    events: [event],
  }
}

/** The correlation key precedence (SPEC §1.5.3(b)): tool_use_id, else prompt_id, else session_id. Two events correlate only when their *own* keys are the same type and equal — an event whose key falls through to prompt_id never correlates with one whose key is a (possibly different) tool_use_id, even by coincidence. */
function correlates(anchor: TimelineEvent, candidate: TimelineEvent): boolean {
  if (anchor.tool_use_id !== null || candidate.tool_use_id !== null) {
    return anchor.tool_use_id !== null && anchor.tool_use_id === candidate.tool_use_id
  }
  if (anchor.prompt_id !== null || candidate.prompt_id !== null) {
    return anchor.prompt_id !== null && anchor.prompt_id === candidate.prompt_id
  }
  return anchor.session_id === candidate.session_id
}

function withinWindow(anchor: TimelineEvent, candidate: TimelineEvent, window: number): boolean {
  if (anchor.clock_skewed || candidate.clock_skewed) return false
  const dt = Math.abs(new Date(candidate.ts).getTime() - new Date(anchor.ts).getTime())
  return dt <= window
}

/**
 * Collapses raw `TimelineEvent`s into display rows per SPEC §1.5.3(b).
 * See the module doc for the Δts-anchor and clock_skewed rationale.
 */
export function collapseEvents(events: TimelineEvent[], opts: CollapseOptions = {}): TimelineItem[] {
  const window = opts.window ?? 2000
  const collapse = opts.collapse ?? true

  if (!collapse) return events.map(singleItem)

  const groups: TimelineEvent[][] = []
  for (const event of events) {
    const openGroup = groups.find((group) => {
      const anchor = group[0]!
      return anchor.kind === event.kind && correlates(anchor, event) && withinWindow(anchor, event, window)
    })
    if (openGroup) {
      openGroup.push(event)
    } else {
      groups.push([event])
    }
  }

  return groups.map(toItem)
}
