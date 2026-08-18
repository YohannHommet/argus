// Hand-maintained fixtures for shapes `server/api/openapi.yaml`'s examples
// don't cover. NOT generated — keep this file small; later tickets that
// need more derived shapes add their own exports here rather than
// growing this one indefinitely on P4-01's behalf.
//
// PLAN.md P4-01 needs exactly one: a "partial" session (SPEC §1.7 —
// `session.start` was never seen, so `started_at` is null) is the concrete
// case `formatRelativeTime`'s null-timestamp guard exists for, and no
// OpenAPI example models it (every listSessions/getSession example is a
// normal, fully-started session).

import type { components } from '@/api/schema'
import { getAnalyticsSummary200Default, getSession200Default, getSessionTimeline200Default, listSessions200Default } from './fixtures'

const baseSession = listSessions200Default.data[0]

export const partialSessionSummary = {
  ...baseSession,
  status: 'active',
  started_at: null,
  duration_ms: null,
  partial: true,
} satisfies components['schemas']['SessionSummary']

// PLAN.md P4-03's AC: "a `partial: true` fixture renders the badge and
// shows no NaN/Invalid Date anywhere" — `started_at`/`duration_ms` null is
// SPEC §1.7's stub-on-reference case (no session.start was ever seen).
export const partialSessionDetail = {
  ...getSession200Default,
  status: 'active',
  started_at: null,
  duration_ms: null,
  partial: true,
} satisfies components['schemas']['SessionDetail']

// PLAN.md P4-03's AC: "`raw_events_expired: true` renders the notice
// instead of an empty timeline" — retention pruned the raw events, but the
// session's own aggregates (turn_count, cost, etc.) are still real.
export const rawEventsExpiredSessionDetail = {
  ...getSession200Default,
  raw_events_expired: true,
} satisfies components['schemas']['SessionDetail']

// P4-02 (session list) needs two more concrete shapes no OpenAPI example covers: a second, distinct
// "normal" session (the 3-fixture-sessions render AC needs sessions that are actually different, not
// the same row three times) and a session with zero tool calls (the reject-rate "undefined, not
// zero" honesty rule — SPEC §6.1 — has to be exercised against a *valid* SessionSummary, and
// tool_call_count/tool_reject_count are non-nullable numbers in the schema, so the null-count branch
// of computeRejectRate is covered by a direct unit test instead, not a fixture).

export const secondSessionSummary = {
  ...baseSession,
  id: '3f7a3b1e-0000-0000-0000-000000000002',
  project: 'platform',
  vendor: 'codex',
  status: 'ended',
  started_at: '2026-08-10T14:00:00.000Z',
  ended_at: '2026-08-10T14:12:00.000Z',
  last_event_at: '2026-08-10T14:12:00.000Z',
  duration_ms: 720000,
  turn_count: 4,
  event_count: 55,
  tool_call_count: 20,
  tool_reject_count: 1,
  cost: {
    ...baseSession.cost,
    usd: 0.0031,
  },
} satisfies components['schemas']['SessionSummary']

// Round-4 UI gap ("no severity in color"): a session whose reject rate clears the 15% critical
// threshold (`classifyRejectRateSeverity`), for asserting the badge actually grades destructive.
export const criticalRejectRateSessionSummary = {
  ...baseSession,
  id: '3f7a3b1e-0000-0000-0000-000000000004',
  project: 'gateway',
  status: 'ended',
  tool_call_count: 20,
  tool_reject_count: 4,
} satisfies components['schemas']['SessionSummary']

export const zeroToolCallsSessionSummary = {
  ...baseSession,
  id: '3f7a3b1e-0000-0000-0000-000000000003',
  project: 'atelier',
  status: 'abandoned',
  tool_call_count: 0,
  tool_reject_count: 0,
} satisfies components['schemas']['SessionSummary']

// P4-07 (chart components) needs three shapes no OpenAPI example covers: a
// live group_by=model timeseries always contains a series whose key is
// the empty string (events with no model attributed); a live
// dimension=query_source breakdown contains both '' and a vocabulary
// value Argus has never seen; and a live decisions response contains a
// decision_source Argus has never seen. All three exercise the "unknown
// vendor values render as themselves" rule (SPEC §4.4).

export const timeseriesWithUnattributedSeries = {
  bucket: 'day',
  buckets: ['2026-08-10T00:00:00Z', '2026-08-11T00:00:00Z'],
  series: [
    { key: 'claude-opus-5', values: [12.4, 9.1] },
    { key: '', values: [0.8, 1.1] },
  ],
  other: { values: [0.2, 0.3] },
} satisfies components['schemas']['Series']

export const breakdownWithUnknownQuerySource = {
  dimension: 'query_source',
  rows: [
    { key: 'sdk', value: 812, share: 0.4 },
    { key: '', value: 300, share: 0.15 },
    { key: 'a_future_query_source', value: 120, share: 0.06 },
  ],
} satisfies components['schemas']['Breakdown']

export const decisionsWithUnknownSource = {
  rows: [
    {
      tool_name: 'Edit',
      accept: 300,
      reject: 41,
      by_source: {
        config: 210,
        hook: 12,
        user_permanent: 40,
        user_temporary: 38,
        user_reject: 37,
        user_abort: 4,
        an_invented_decision_source: 6,
      },
      exact_share: 1,
      p50_wait_ms: 1900,
      p95_wait_ms: 22400,
    },
    {
      tool_name: 'Bash',
      accept: 120,
      reject: 8,
      by_source: {
        config: 100,
        hook: 20,
      },
      exact_share: 0.62,
      p50_wait_ms: 900,
      p95_wait_ms: 5400,
    },
  ],
} satisfies components['schemas']['DecisionMatrix']

// P4-05 (subagent tree) fixtures. The exact depth-2 shape captured live
// against seed 42 (SPEC/PLAN P4-05's "THE DATA" block): `cost_usd` null on
// every node (root included), a root `status: "ended"` — outside the
// documented SubagentStatus enum entirely (server/internal/store/postgres/
// subagent_tree.go casts the *session* status into the subagent vocabulary
// for the synthetic root; logged as a server/spec defect) — and one child
// with `tool_call_count: 0` (a real reading) alongside one with a positive
// count, so both branches of "null -> em dash, 0 -> '0'" are exercised by
// one fixture.
export const getSessionSubagentsDepth2Live = {
  data: [
    {
      agent_id: 'root',
      parent_agent_id: null,
      agent_type: 'main',
      depth: 0,
      // Deliberately cast to the schema's string-backed enum type rather
      // than typed as SubagentStatus literally: the live server value is
      // "ended", which the enum does not contain (see comment above), and
      // this fixture exists precisely to prove the UI survives that.
      status: 'ended' as components['schemas']['SubagentStatus'],
      started_at: '2026-08-17T13:00:23.519563Z',
      ended_at: '2026-08-17T13:00:23.732591Z',
      tool_call_count: 163,
      cost_usd: null,
      children: [
        {
          agent_id: 'agent-107d2cba-explore-1',
          parent_agent_id: 'root',
          agent_type: 'explore',
          depth: 1,
          status: 'unknown',
          started_at: '2026-08-17T13:00:23.560000Z',
          ended_at: '2026-08-17T13:00:23.700000Z',
          tool_call_count: 0,
          cost_usd: null,
          children: [],
        },
        {
          agent_id: 'agent-e090ede7-explore-2',
          parent_agent_id: 'root',
          agent_type: 'explore',
          depth: 1,
          status: 'complete',
          started_at: '2026-08-17T13:00:23.610000Z',
          ended_at: '2026-08-17T13:00:23.690000Z',
          tool_call_count: 1,
          cost_usd: null,
          children: [],
        },
      ],
    },
  ],
  cost_attribution: {
    by_query_source: {
      '': 1.23201,
      a_future_query_source: 0.265497,
      auxiliary: 0.047934,
      generate_session_title: 0.219638,
      main: 1.0990900000000001,
      sdk: 2.300948,
      subagent: 0.325117,
    },
    dominant_query_source: 'sdk',
    other_query_source_usd: 3.189286,
    per_node_available: false,
    note: 'Claude Code does not emit per-agent cost; api_request carries query_source only.',
  },
} satisfies components['schemas']['SubagentTree']

/**
 * A subagent node whose `tool_call_count` is `null` — no hook coverage —
 * as distinct from `0` (a real "zero tool calls" reading), which the
 * sibling fixture above already covers on `agent-107d2cba-explore-1`.
 */
export const subagentNodeWithNullToolCallCount = {
  ...getSessionSubagentsDepth2Live.data[0].children[0],
  agent_id: 'agent-no-hook-coverage',
  tool_call_count: null,
} satisfies components['schemas']['SubagentNode']

/**
 * A node with a `status` entirely outside the documented enum (not even
 * the live server's own out-of-enum "ended" — something no build has ever
 * emitted) and a null `started_at`/`ended_at`, so a single fixture covers
 * both "status is free-form, render whatever arrives" and "a fully-null
 * timing pair must not throw when formatted".
 */
export const subagentNodeUnknownStatusNullTiming = {
  agent_id: 'agent-mystery',
  parent_agent_id: 'root',
  agent_type: 'a_future_agent_type',
  depth: 1,
  status: 'quantum_superposed' as components['schemas']['SubagentStatus'],
  started_at: null,
  ended_at: null,
  tool_call_count: null,
  cost_usd: null,
  children: [],
} satisfies components['schemas']['SubagentNode']

/**
 * 50 sibling leaves under one root — PLAN P4-05's "a 50-node fixture
 * renders within the recursion guard" AC is a breadth check: 50 nodes all
 * at depth 1 must render without tripping SubagentNode's own depth-based
 * recursion guard (which only fires on the vertical axis).
 */
export const getSessionSubagentsFiftyNodes = {
  data: [
    {
      agent_id: 'root',
      parent_agent_id: null,
      agent_type: 'main',
      depth: 0,
      status: 'running',
      started_at: '2026-08-17T13:00:00.000Z',
      ended_at: null,
      tool_call_count: 200,
      cost_usd: null,
      children: Array.from({ length: 49 }, (_, i) => ({
        agent_id: `agent-${i}`,
        parent_agent_id: 'root',
        agent_type: 'explore',
        depth: 1,
        status: 'complete' as const,
        started_at: '2026-08-17T13:00:01.000Z',
        ended_at: '2026-08-17T13:00:02.000Z',
        tool_call_count: i,
        cost_usd: null,
        children: [],
      })),
    },
  ],
  cost_attribution: {
    by_query_source: { sdk: 4.2 },
    dominant_query_source: 'sdk',
    other_query_source_usd: 0,
    per_node_available: false,
    note: 'Claude Code does not emit per-agent cost; api_request carries query_source only.',
  },
} satisfies components['schemas']['SubagentTree']

/**
 * A chain nested well past SubagentNode's own recursion guard — the
 * server caps synthetic-tree depth at 16 (subagent_tree.go), but this
 * fixture's whole point is to exercise the *client's* independent guard
 * against a malformed/cyclic payload that reaches the browser anyway, so
 * it goes deeper than the server's own cap.
 */
function buildDeepChain(depth: number): components['schemas']['SubagentNode'] {
  let node: components['schemas']['SubagentNode'] = {
    agent_id: `agent-depth-${depth}`,
    parent_agent_id: `agent-depth-${depth - 1}`,
    agent_type: 'explore',
    depth,
    status: 'complete',
    started_at: '2026-08-17T13:00:00.000Z',
    ended_at: '2026-08-17T13:00:01.000Z',
    tool_call_count: 1,
    cost_usd: null,
    children: [],
  }
  for (let d = depth - 1; d >= 0; d--) {
    node = {
      agent_id: d === 0 ? 'root' : `agent-depth-${d}`,
      parent_agent_id: d === 0 ? null : `agent-depth-${d - 1}`,
      agent_type: d === 0 ? 'main' : 'explore',
      depth: d,
      status: 'complete',
      started_at: '2026-08-17T13:00:00.000Z',
      ended_at: '2026-08-17T13:00:01.000Z',
      tool_call_count: 1,
      cost_usd: null,
      children: [node],
    }
  }
  return node
}

export const deeplyNestedSubagentTree: components['schemas']['SubagentNode'][] = [buildDeepChain(30)]

// PLAN.md P4-04 (Timeline) — `collapseEvents` scenario fixtures, sim-derived
// from a live server capture (seed 42): an OTel `tool.result` and a hook
// `tool.result` for the same `tool_use_id`, one pair close enough to
// collapse and one pair far enough apart not to. Building these as a
// factory rather than static literals lets the test suite construct many
// small variations (different Δts, different tool_use_id, no correlation
// key at all) without hand-writing the whole TimelineEvent shape each time.
export type TimelineEventFixture = components['schemas']['TimelineEvent']

const baseTimelineEvent = getSessionTimeline200Default.data[0]!

let refCounter = 0
/** Builds one TimelineEvent, defaulting every field to the base fixture's, with a fresh unique event_ref/seq/id per call. */
export function makeTimelineEvent(overrides: Partial<TimelineEventFixture>): TimelineEventFixture {
  refCounter += 1
  return {
    ...baseTimelineEvent,
    event_ref: `fixture-ref-${refCounter}`,
    seq: baseTimelineEvent.seq + refCounter,
    id: `0192abcd-0000-0000-0000-${String(refCounter).padStart(12, '0')}`,
    ...overrides,
  }
}

// The AC's concrete pair: an otel_log tool.result and a hook tool.result,
// same tool_use_id, 300ms apart — must collapse to one item listing 2 sources.
export const otelToolResultEvent = makeTimelineEvent({
  kind: 'tool.result',
  event_name: 'tool_result',
  source: 'otel_log',
  ts: '2026-08-14T01:00:18.000Z',
  tool_use_id: 'toolu_close_01',
  duration_ms: 842,
  success: true,
})

export const hookToolResultEventClose = makeTimelineEvent({
  kind: 'tool.result',
  event_name: 'PostToolUse',
  source: 'hook',
  ts: '2026-08-14T01:00:18.300Z',
  tool_use_id: 'toolu_close_01',
  agent_id: 'agent-main',
  duration_ms: null,
  success: true,
})

// Same pair, 5s apart — must NOT collapse.
export const hookToolResultEventFar = makeTimelineEvent({
  kind: 'tool.result',
  event_name: 'PostToolUse',
  source: 'hook',
  ts: '2026-08-14T01:00:23.000Z',
  tool_use_id: 'toolu_close_01',
  agent_id: 'agent-main',
  duration_ms: null,
  success: true,
})

// P4-08's null-vs-zero AC, verified live against the real server (seed 42,
// GET /api/v1/analytics/summary?from=-30d): `loc.added`/`loc.removed` and
// `active_seconds` came back as a *measured* `0`, not `null` — no OpenAPI
// example happens to carry a real all-zero LOC/active-time window, and the
// distinction (0 renders "0", null renders "—") is the entire point of
// SPEC §6.1, so it needs its own concrete fixture rather than reusing
// `getAnalyticsSummary200Default`'s non-zero loc/active_seconds.
export const getAnalyticsSummary200MeasuredZeros = {
  window: {
    from: '2026-07-18T13:01:15Z',
    to: '2026-08-17T13:01:15Z',
    bucket: 'day',
  },
  sessions: 20,
  turns: 163,
  api_requests: 316,
  api_errors: 2,
  tool_calls: 959,
  tool_rejects: 54,
  reject_rate: 0.056308654848800835,
  tokens: {
    input: 603204,
    output: 369490,
    cache_read: 21997293,
    cache_creation: 515661,
  },
  cost: {
    usd: 24.310156,
    reported_usd: 24.310156,
    estimated_usd: 0,
    estimated_share: 0,
  },
  loc: {
    added: 0,
    removed: 0,
  },
  active_seconds: 0,
  source: 'event',
  metrics_only_projects: [],
  not_attributable: [],
} satisfies components['schemas']['Summary']

// P4-06 (ToolCallTable / toolsStore / /tools) fixtures. No OpenAPI example covers either shape
// below: `listToolCalls200Default` (fixtures.ts) has exactly one row, `correlation: "exact"`, and a
// non-null `wait_ms` — none of the other three `Correlation` values, and not the null-`wait_ms` case
// the AC calls out by name.

// The literal response captured live against seed 42 (this ticket's "THE DATA" block): note
// `wait_ms: null` on real demo data (not a contrived edge case), and `decision_source:
// "an_invented_decision_source"` — a value Argus has never hardcoded, present in a real payload.
// Exercises both the null-vs-zero honesty rule and the "unknown vendor values render as themselves"
// rule (SPEC §6.1/§4.4) against one real row, not two separate synthetic ones.
export const toolCallLiveSeed42OtelOnly = {
  id: '2de5b226-ac84-5c1a-846a-ddd2ac8e6777',
  session_id: '26a17632-1715-4bcc-ba5f-2da832146439',
  prompt_id: 'prompt-26a17632-…-0000',
  tool_use_id: 'toolu_d75c0aaf-4da2-4b6c-b06a-a77df95faf24',
  tool_name: 'Read',
  tool_source: 'builtin',
  agent_id: null,
  decision: 'accept',
  decision_source: 'an_invented_decision_source',
  permission_mode: null,
  started_at: '2026-08-17T13:04:41.838644Z',
  decided_at: '2026-08-17T13:04:41.838644Z',
  ended_at: '2026-08-17T13:04:47.715644Z',
  duration_ms: 3987,
  wait_ms: null,
  success: true,
  error_type: null,
  file_path: null,
  input_size_bytes: 2726,
  result_size_bytes: 16724,
  correlation: 'otel_only',
  event_count: 2,
} satisfies components['schemas']['ToolCall']

// The three `Correlation` values `listToolCalls200Default`/`toolCallLiveSeed42OtelOnly` don't cover:
// `heuristic` and `hook_only` (best-effort/weakest provenance — SPEC §1.6's correlation ladder), plus
// a second `exact` row with a rejected decision so the decision+source badge's reject styling has a
// concrete fixture too. `hook_only` additionally has null decision/decision_source/permission_mode
// (SPEC §1.6: `hook_only` means "no tool_use_id anywhere for this call", so nothing to correlate the
// hook's own tool_decision fields against a decision event) to exercise RawValue's null branch
// alongside the correlation badge's own "weakest provenance" treatment.
export const toolCallHeuristic = {
  id: '5b1f7a2e-0000-0000-0000-00000000h001',
  session_id: '3f7a3b1e-0000-0000-0000-000000000001',
  prompt_id: 'p_88f2',
  tool_use_id: null,
  tool_name: 'Bash',
  tool_source: 'builtin',
  agent_id: null,
  decision: 'accept',
  decision_source: 'hook',
  permission_mode: 'default',
  started_at: '2026-08-11T09:20:00.000Z',
  decided_at: '2026-08-11T09:20:01.100Z',
  ended_at: '2026-08-11T09:20:03.400Z',
  duration_ms: 3400,
  wait_ms: 1100,
  success: true,
  error_type: null,
  file_path: null,
  input_size_bytes: 88,
  result_size_bytes: 512,
  correlation: 'heuristic',
  event_count: 1,
} satisfies components['schemas']['ToolCall']

export const toolCallHookOnly = {
  id: '5b1f7a2e-0000-0000-0000-00000000h002',
  session_id: '3f7a3b1e-0000-0000-0000-000000000001',
  prompt_id: 'p_88f3',
  tool_use_id: null,
  tool_name: 'Edit',
  tool_source: null,
  agent_id: 'ag_7',
  decision: null,
  decision_source: null,
  permission_mode: null,
  started_at: '2026-08-11T09:25:00.000Z',
  decided_at: null,
  ended_at: null,
  duration_ms: null,
  wait_ms: null,
  success: null,
  error_type: null,
  file_path: 'web/src/App.vue',
  input_size_bytes: null,
  result_size_bytes: null,
  correlation: 'hook_only',
  event_count: 1,
} satisfies components['schemas']['ToolCall']

export const toolCallExactRejected = {
  id: '5b1f7a2e-0000-0000-0000-00000000h003',
  session_id: '3f7a3b1e-0000-0000-0000-000000000001',
  prompt_id: 'p_88f4',
  tool_use_id: 'toolu_01Z',
  tool_name: 'Write',
  tool_source: 'builtin',
  agent_id: null,
  decision: 'reject',
  decision_source: 'user_reject',
  permission_mode: 'default',
  started_at: '2026-08-11T09:30:00.000Z',
  decided_at: '2026-08-11T09:30:04.000Z',
  ended_at: '2026-08-11T09:30:04.000Z',
  duration_ms: 4000,
  wait_ms: 4000,
  success: false,
  error_type: 'permission_denied',
  file_path: 'web/src/main.ts',
  input_size_bytes: 210,
  result_size_bytes: null,
  correlation: 'exact',
  event_count: 2,
} satisfies components['schemas']['ToolCall']

/** All four `Correlation` values in one array, for a single fixture render covering the AC:
 * "renders fixtures with all four correlation values and a distinct visual for `hook_only`". */
export const toolCallsAllCorrelations = [
  toolCallExactRejected,
  toolCallLiveSeed42OtelOnly,
  toolCallHeuristic,
  toolCallHookOnly,
] satisfies components['schemas']['ToolCall'][]

// P4-09 (data-quality view) needs the two empty-path shapes that are the
// *default* clean-data response for each `/quality/*` endpoint — the sim
// only emits unmapped events with `--chaos-unknown`, and a deployment with
// no hook coverage reports no hook-latency rows at all — plus a
// multi-row unknown-kinds response to exercise the "sum every row's
// count" aggregation the generated single-row fixture can't.

export const emptyQualityUnknownKinds = {
  rows: [],
} satisfies components['schemas']['QualityUnknownKindsResponse']

export const emptyQualityHookLatency = {
  rows: [],
} satisfies components['schemas']['QualityHookLatencyResponse']

// P4-10 (setup card / notices) needs two shapes no OpenAPI example covers.
//
// `getAnalyticsSummary200Default` already carries a non-zero
// `estimated_share` (0.0199) and a non-empty `metrics_only_projects`
// (`["legacy-app"]`), but the ticket's AC names exact values
// (`estimated_share: 0.02`, `metrics_only_projects: ["x"]`) to assert
// against directly, so this fixture pins both rather than relying on the
// generated default's incidental numbers staying what they are today.
export const getAnalyticsSummary200EstimatedCostAndMetricsOnly = {
  ...getAnalyticsSummary200Default,
  cost: {
    usd: 71.44,
    reported_usd: 70.01,
    estimated_usd: 1.43,
    estimated_share: 0.02,
  },
  metrics_only_projects: ['x'],
} satisfies components['schemas']['Summary']

// Live-verified against `--cost-mode=omit` data (this ticket's "THE OTHER
// NOTICES" block): every `llm.request` event in the window carried no
// reported cost, so `estimated_share` is a full `1` (100%), not "a
// sliver of the total" — the EstimatedCostNotice's copy has to read
// sensibly here too, not just at 2%.
export const getAnalyticsSummary200FullyEstimatedCost = {
  ...getAnalyticsSummary200Default,
  cost: {
    usd: 24.31028864,
    reported_usd: 0,
    estimated_usd: 24.31028864,
    estimated_share: 1,
  },
  metrics_only_projects: [],
} satisfies components['schemas']['Summary']

export const multiRowQualityUnknownKinds = {
  rows: [
    {
      event_name: 'some_new_event',
      source: 'otel_log',
      count: 41,
      first_seen: '2026-08-10T10:00:00Z',
      last_seen: '2026-08-11T09:00:00Z',
      sample: { 'raw.attr': 'value' },
    },
    {
      event_name: 'another_future_event',
      source: 'hook',
      count: 3,
      first_seen: '2026-08-11T08:00:00Z',
      last_seen: '2026-08-11T08:30:00Z',
      sample: { 'raw.other': 1 },
    },
  ],
} satisfies components['schemas']['QualityUnknownKindsResponse']
