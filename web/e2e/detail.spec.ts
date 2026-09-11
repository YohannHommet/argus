import { expect, test } from '@playwright/test'

import { apiGet, gotoReady } from './helpers'

/**
 * Flow 2 — the session detail view (SPEC §4.2/§4.3): timeline, event
 * inspector, subagents tree and tool-call table. Assertions are structural
 * and cross-checked against the REST API rather than pinned to volatile
 * exact values. Runs at the config's 1440x900 viewport, so the event
 * inspector is the persistent side panel (`event-inspector-panel`), not the
 * overlay sheet — EventInspector.vue's `useMediaQuery('(min-width: 1024px)')`.
 */

interface SessionSummary {
  id: string
  project: string
  tool_call_count: number
  subagent_count: number
}

interface SessionsPage {
  data: SessionSummary[]
}

/**
 * Mirrors scripts/ui-capture.mjs's `pickDetailSession`: the busiest session
 * (most tool calls) among those that actually have a subagent tree, so the
 * Subagents tab is non-empty too. Falls back to the busiest session overall
 * if the seed produced no subagents at all.
 */
async function pickDetailSession(request: import('@playwright/test').APIRequestContext): Promise<SessionSummary> {
  const { data } = await apiGet<SessionsPage>(request, '/api/v1/sessions?limit=500')
  expect(data.length, 'demo seed should produce at least one session').toBeGreaterThan(0)

  const byTools = (a: SessionSummary, b: SessionSummary) => (b.tool_call_count ?? 0) - (a.tool_call_count ?? 0)
  const withSubagents = data.filter((s) => (s.subagent_count ?? 0) > 0).sort(byTools)
  return withSubagents[0] ?? [...data].sort(byTools)[0]!
}

/**
 * The timeline's first loaded page is not guaranteed to contain a
 * tool-decision event just because the session is busy overall, so this
 * clicks "Load more" (if present) until a decision badge shows up or there
 * are no more pages — never a sleep, each click is followed by a DOM poll
 * for the row count to have actually grown.
 */
async function revealDecisionBadge(page: import('@playwright/test').Page): Promise<void> {
  const decisionRows = page.locator('[data-testid="event-row"]:has([data-testid="decision-badge"])')
  const loadMore = page.getByTestId('timeline-load-more')
  const rows = page.getByTestId('event-row')

  for (let i = 0; i < 10; i += 1) {
    if ((await decisionRows.count()) > 0) return
    if ((await loadMore.count()) === 0) return
    const before = await rows.count()
    await loadMore.click()
    await expect.poll(async () => rows.count(), { message: 'timeline should grow after Load more' }).toBeGreaterThan(before)
  }
}

test.describe('Session detail', () => {
  test('timeline renders turns, tool calls and LLM requests with a decision badge', async ({ page, request }) => {
    const session = await pickDetailSession(request)

    await gotoReady(page, `/sessions/${session.id}?tab=timeline`)

    await expect(page.getByTestId('timeline')).toBeVisible()
    // Turns: at least one group header (a real turn, or the "No turn" catch-all).
    await expect(page.getByTestId('timeline-group-header').first()).toBeVisible()

    // A substantial number of rows — tool calls and LLM requests collapsed onto the timeline.
    const rows = page.getByTestId('event-row')
    await expect(rows.first()).toBeVisible()
    await expect.poll(async () => rows.count(), { message: 'timeline should render several rows' }).toBeGreaterThanOrEqual(3)

    // At least one tool-call decision, with its provenance source.
    await revealDecisionBadge(page)
    const decisionBadge = page.getByTestId('decision-badge').first()
    await expect(decisionBadge).toBeVisible()
    await expect(decisionBadge).toContainText(/accept|reject/i)
    await expect(decisionBadge.getByTestId('decision-badge-source')).toBeVisible()
  })

  test('clicking an event row opens the inspector with payload and copy affordance', async ({ page, request }) => {
    const session = await pickDetailSession(request)

    await gotoReady(page, `/sessions/${session.id}?tab=timeline`)
    await expect(page.getByTestId('event-row').first()).toBeVisible()

    // Nothing selected yet.
    await expect(page.getByTestId('event-detail-empty')).toBeVisible()

    await revealDecisionBadge(page)
    const decisionRow = page.locator('[data-testid="event-row"]:has([data-testid="decision-badge"])').first()
    const target = (await decisionRow.count()) > 0 ? decisionRow : page.getByTestId('event-row').first()
    await target.click()

    await expect(page.getByTestId('event-inspector-panel')).toBeVisible()
    await expect(page.getByTestId('event-detail-empty')).toHaveCount(0)
    await expect(page.getByTestId('event-detail-summary')).toBeVisible()
    await expect(page.getByTestId('copy-icon-button').first()).toBeVisible()
    await expect(page.getByTestId('json-viewer')).toBeVisible()
  })

  test('subagents tab shows the tree', async ({ page, request }) => {
    const session = await pickDetailSession(request)

    await gotoReady(page, `/sessions/${session.id}?tab=subagents`)

    // Loads lazily after the session — no explicit wait needed, `toBeVisible` auto-retries
    // through the `subagent-tree-loading` skeleton until the fetch settles.
    await expect(page.getByTestId('subagent-node').first()).toBeVisible()
  })

  test('tools tab lists calls', async ({ page, request }) => {
    const session = await pickDetailSession(request)

    await gotoReady(page, `/sessions/${session.id}?tab=tools`)

    await expect(page.getByTestId('tool-call-table')).toBeVisible()
    await expect(page.getByTestId('tool-call-row').first()).toBeVisible()
  })
})
