import { expect, test, type Page } from '@playwright/test'

import { gotoReady, waitForReady } from './helpers'

/**
 * Flow 5 — the live SSE view (SPEC §5/§6.2), the highest-value E2E because it
 * cannot be unit-tested for real: it asserts a real EventSource streaming into
 * the real embedded app.
 *
 * Tagged `@live` and self-skipped unless ARGUS_E2E_LIVE is set: scripts/e2e.sh
 * runs it in a second phase (`--grep @live`, ARGUS_E2E_LIVE=1) with
 * `argusd sim --mode=load` streaming into the stack — that sim adds sessions the
 * other specs' count/total assertions must not see, so it runs last.
 *
 * Every wait is on the page's OWN DOM (feed row counts, the health strip's
 * state), never a bare sleep. WSL2 headless chromium is flaky on this busy page
 * under Docker load — timeouts are generous, and CI (clean runner) is the
 * source of truth.
 */

const feedRows = (page: Page) => page.locator('[data-testid="live-feed"] [data-testid="event-row"]')

/** Every rendered live-feed row's data-event-ref, in DOM order. */
function feedRefs(page: Page): Promise<(string | null)[]> {
  return feedRows(page).evaluateAll((els) => els.map((e) => e.getAttribute('data-event-ref')))
}

/** The live feed's "N events this tab" counter (formatCount, may be grouped). */
async function eventCount(page: Page): Promise<number> {
  const text = await page.getByTestId('live-feed-event-count').innerText()
  const m = text.replace(/,/g, '').match(/(\d+)/)
  return m ? Number.parseInt(m[1], 10) : Number.NaN
}

test.describe('Live view @live', () => {
  // Self-skip unless the load-sim phase set ARGUS_E2E_LIVE — so a bare
  // `playwright test` (or `pnpm e2e`) never runs these against a stack with no
  // events streaming. scripts/e2e.sh sets it for the @live phase only.
  test.beforeEach(() => {
    test.skip(!process.env.ARGUS_E2E_LIVE, 'live-sim phase only — run via scripts/e2e.sh')
  })

  test('the feed grows live, an active-session card appears, and a KPI advances', async ({ page }) => {
    test.setTimeout(120_000)
    await gotoReady(page, '/live')

    // Streaming has established once the feed holds a handful of rows.
    await expect
      .poll(() => feedRows(page).count(), { timeout: 60_000, message: 'the live feed should start receiving rows' })
      .toBeGreaterThanOrEqual(5)

    const rowsBefore = await feedRows(page).count()
    const kpiBefore = await eventCount(page)

    // An active-session card is rendered for a currently-streaming session.
    await expect(page.locator('[data-testid="active-session-card"]').first()).toBeVisible({ timeout: 30_000 })

    // The feed grows without any reload — the row count strictly increases.
    await expect
      .poll(() => feedRows(page).count(), { timeout: 45_000, message: 'feed row count should strictly increase while streaming' })
      .toBeGreaterThan(rowsBefore)

    // A KPI advances: the "events this tab" counter climbs past its earlier value.
    await expect
      .poll(() => eventCount(page), { timeout: 45_000, message: 'the events-this-tab KPI should advance' })
      .toBeGreaterThan(kpiBefore)
  })

  test('a reconnect replays via Last-Event-ID with no duplicate rows', async ({ page }) => {
    test.setTimeout(120_000)
    await gotoReady(page, '/live')

    await expect
      .poll(() => feedRows(page).count(), { timeout: 60_000, message: 'feed should be streaming before the reconnect' })
      .toBeGreaterThanOrEqual(10)
    await expect(page.getByTestId('health-strip-connection')).toContainText('Connected', { timeout: 30_000 })

    const refsBefore = await feedRefs(page)
    const rowsBefore = refsBefore.length

    // Force a real close/reopen of the EventSource, in-app: navigating to a view that holds no live
    // subscription (Analytics) empties the store's subscription stack, so it tears the EventSource
    // down (stores/live.ts reconcileConnection -> status 'closed'). The store's ring buffer and
    // `lastEventRef` survive that (only a `reset` frame clears them), so navigating back to /live
    // re-opens the stream with `?after=<lastEventRef>` — exactly SPEC §5.2's Last-Event-ID resume
    // path. (Playwright's context.setOffline does NOT tear down an already-open SSE, so it can't
    // trigger this — a client-side route change reliably does.)
    await page.getByRole('link', { name: 'Analytics' }).click()
    await expect(page.getByTestId('analytics-view')).toBeVisible({ timeout: 30_000 })
    await page.getByRole('link', { name: 'Live', exact: true }).click()
    await waitForReady(page)
    await expect(page.getByTestId('health-strip-connection')).toContainText('Connected', { timeout: 60_000 })

    // Replay resumes: the retained ring plus the events the reconnect replays/streams push the row
    // count past where it was — a DOM-gated wait, no sleep.
    await expect
      .poll(() => feedRows(page).count(), { timeout: 60_000, message: 'feed should resume growing after the reconnect' })
      .toBeGreaterThan(rowsBefore)

    // No duplicate rows: the ring does NO client-side dedup, so an `after=`/Last-Event-ID replay that
    // re-sent an event already in the ring would render a repeated data-event-ref here. Every rendered
    // event_ref must be unique — and the pre-reconnect rows must survive (a resume, not a reset).
    const refsAfter = await feedRefs(page)
    expect(refsAfter.every((r) => r !== null && r !== ''), 'every live-feed row carries a data-event-ref').toBeTruthy()
    expect(new Set(refsAfter).size, 'no duplicate event_ref in the live feed after the reconnect').toBe(refsAfter.length)
    const retained = refsBefore.filter((r) => new Set(refsAfter).has(r)).length
    expect(retained, 'the pre-reconnect rows should be retained across the resume').toBeGreaterThan(0)
  })
})
