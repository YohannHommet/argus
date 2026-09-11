import { expect, test } from '@playwright/test'

import { apiGet, gotoReady } from './helpers'

/**
 * Flow 1 — the sessions list (SPEC §4.1). Assertions are structural/behavioural
 * (counts, ordering, narrowing) and cross-checked against the REST API rather
 * than pinned to volatile exact values. The stack is seeded with 60 demo
 * sessions (scripts/e2e.sh), above the 50-row page size, so pagination is real.
 */

const PAGE_LIMIT = 50

interface SessionsPage {
  data: { id: string; project: string; cost: { usd: number } }[]
  page: { next_cursor: string | null; has_more: boolean }
}

const rows = (page: import('@playwright/test').Page) => page.getByTestId('session-row')

/** Each row's cost is its last cell (SessionRow.vue), rendered by formatCost. */
async function rowCostCents(page: import('@playwright/test').Page): Promise<number[]> {
  const texts = await rows(page).locator('td').last().allInnerTexts()
  return texts.map((t) => {
    const m = t.replace(/,/g, '').match(/\$(-?[0-9]+(?:\.[0-9]+)?)/)
    return m ? Math.round(Number.parseFloat(m[1]) * 100) : Number.NaN
  })
}

test.describe('Sessions list', () => {
  test('loads at least 20 sessions, matching the API', async ({ page, request }) => {
    const all = await apiGet<SessionsPage>(request, '/api/v1/sessions?limit=500')
    expect(all.data.length, 'demo seed should produce >= 20 sessions').toBeGreaterThanOrEqual(20)

    await gotoReady(page, '/sessions')

    // First page is capped at the store's page size; the rest is behind "Load more".
    const expectedFirstPage = Math.min(all.data.length, PAGE_LIMIT)
    await expect(rows(page)).toHaveCount(expectedFirstPage)
    await expect(page.getByTestId('session-table')).toBeVisible()
  })

  test('a project filter narrows the set to that project', async ({ page, request }) => {
    // 'platform' is one of the demo projects (server/internal/sim/projects.go).
    const project = 'platform'
    const filtered = await apiGet<SessionsPage>(
      request,
      `/api/v1/sessions?limit=500&project=${encodeURIComponent(project)}`,
    )
    const all = await apiGet<SessionsPage>(request, '/api/v1/sessions?limit=500')
    expect(filtered.data.length, 'project must actually narrow the full set').toBeLessThan(all.data.length)
    expect(filtered.data.length).toBeGreaterThan(0)

    // The filter is URL-driven (stores/sessions.ts parses `project` from the query on load) — the
    // same store path the SelectFilter control drives, exercised without the reka-ui portal dance.
    await gotoReady(page, `/sessions?project=${encodeURIComponent(project)}`)

    await expect(rows(page)).toHaveCount(Math.min(filtered.data.length, PAGE_LIMIT))
    // Every visible row is that project — the narrowing is real, not just a smaller count.
    const projectCells = rows(page).locator('td').nth(1)
    for (const text of await projectCells.allInnerTexts()) {
      expect(text.trim()).toBe(project)
    }
  })

  test('pagination advances with no duplicate ids across pages', async ({ page, request }) => {
    // The requirement's own method: compare page1 and page2 ids from the keyset API and assert they
    // are disjoint (the server never repeats a row across pages).
    const p1 = await apiGet<SessionsPage>(request, `/api/v1/sessions?limit=${PAGE_LIMIT}`)
    expect(p1.page.has_more, 'need > 1 page to test pagination — seed >= 51 sessions').toBeTruthy()
    const p2 = await apiGet<SessionsPage>(
      request,
      `/api/v1/sessions?limit=${PAGE_LIMIT}&cursor=${encodeURIComponent(p1.page.next_cursor!)}`,
    )
    const p1ids = new Set(p1.data.map((s) => s.id))
    const overlap = p2.data.filter((s) => p1ids.has(s.id))
    expect(overlap, 'page 2 must share no ids with page 1').toHaveLength(0)

    const total = new Set([...p1.data, ...p2.data].map((s) => s.id)).size

    // The UI: first page renders, "Load more" reveals the rest, and the total row count equals the
    // number of DISTINCT sessions — so the appended page added new rows, never duplicates.
    await gotoReady(page, '/sessions')
    await expect(rows(page)).toHaveCount(PAGE_LIMIT)
    await page.getByTestId('load-more').click()
    await expect(rows(page)).toHaveCount(total)
  })

  test('sorting by cost reorders the rows into descending cost', async ({ page }) => {
    await gotoReady(page, '/sessions')
    const before = await rowCostCents(page)

    // Click the sortable "Cost" header (SessionTable.vue). Sort is keyset desc-only.
    await page.getByTestId('session-table').locator('thead').getByText('Cost', { exact: true }).click()
    await expect(page).toHaveURL(/sort=cost_usd/)

    // Poll the rendered costs until the refetch lands and they are non-increasing — a DOM wait, no sleep.
    await expect
      .poll(async () => {
        const cents = await rowCostCents(page)
        return cents.every((c, i) => i === 0 || cents[i - 1] >= c)
      }, { message: 'rows should be sorted by descending cost' })
      .toBe(true)

    const after = await rowCostCents(page)
    expect(after, 'the order must actually change vs the default sort').not.toEqual(before)
  })

  test('the time-range control filters the list and changes results', async ({ page, request }) => {
    // Note on the demo seed: a session's started_at/last_event_at are ingest wall-clock (all clustered
    // at seed time), so no 24h/7d/30d preset can narrow the list — they all include "now". So this
    // exercises the control two honest ways: (1) a preset syncs the URL and refetches consistently
    // with the API; (2) an excluding custom window (a future `from`) demonstrably changes the results.
    const all = await apiGet<SessionsPage>(request, '/api/v1/sessions?limit=500')
    const last24h = await apiGet<SessionsPage>(request, '/api/v1/sessions?limit=500&from=-24h')

    await gotoReady(page, '/sessions')
    await expect(rows(page)).toHaveCount(Math.min(all.data.length, PAGE_LIMIT))

    // (1) The 24h preset writes ?from=-24h and refetches; the rendered count stays consistent with
    // what the API returns for that window.
    await page.getByTestId('filter-range-24h').click()
    await expect(page).toHaveURL(/from=-24h/)
    await expect(rows(page)).toHaveCount(Math.min(last24h.data.length, PAGE_LIMIT))

    // (2) A custom window entirely in the future excludes every session — the filter provably changes
    // the result set, all the way to the empty state.
    const future = await apiGet<SessionsPage>(request, '/api/v1/sessions?limit=500&from=2027-01-01T00:00:00Z')
    expect(future.data, 'a future window should exclude every demo session').toHaveLength(0)

    await page.getByTestId('filter-range-custom').click()
    await page.getByTestId('filter-from').fill('2027-01-01T00:00:00Z')
    await page.getByTestId('filter-range-apply').click()
    await expect(rows(page)).toHaveCount(0)
    await expect(page.getByTestId('empty-state')).toBeVisible()
  })

  test('severity coloring is present via data-attr on the rows', async ({ page }) => {
    await gotoReady(page, '/sessions')

    const severities = page.locator('[data-testid="session-row"] [data-severity]')
    await expect(severities.first()).toBeVisible()

    const values = await severities.evaluateAll((els) => els.map((e) => e.getAttribute('data-severity')))
    expect(values.length).toBeGreaterThan(0)
    for (const v of values) expect(v).toMatch(/^(neutral|warn|critical)$/)
    // With 50 rows and percentile-based thresholds, at least one row is graded above neutral.
    expect(values.some((v) => v === 'warn' || v === 'critical')).toBeTruthy()
  })
})
