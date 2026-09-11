import { expect, test, type Page } from '@playwright/test'

import { gotoReady } from './helpers'

/**
 * Flow 4 — the tool explorer (SPEC §6.2/§6.3) and the data-quality view
 * (SPEC §6.2). The tools view's search is client-side over whatever page is
 * already loaded (ToolExplorerView.vue), so the assertions are derived from
 * the rendered DOM rather than a fresh API call — the only honest ground
 * truth for "does the loaded page narrow correctly" is the page itself.
 */

const toolNameCells = (page: Page) => page.getByTestId('cell-tool-name')

/** Frequency of every distinct tool name among the currently-rendered rows. */
async function toolNameFrequency(page: Page): Promise<Map<string, number>> {
  const names = await toolNameCells(page).allInnerTexts()
  const freq = new Map<string, number>()
  for (const name of names) {
    const trimmed = name.trim()
    freq.set(trimmed, (freq.get(trimmed) ?? 0) + 1)
  }
  return freq
}

test.describe('Tool explorer', () => {
  test('loads and lists tool calls', async ({ page }) => {
    await gotoReady(page, '/tools')

    await expect(page.getByTestId('tool-call-table')).toBeVisible()
    const count = await page.getByTestId('tool-call-row').count()
    expect(count, 'demo seed should produce at least one tool call').toBeGreaterThanOrEqual(1)
  })

  test('search narrows the loaded rows to that tool name, then clears back', async ({ page }) => {
    await gotoReady(page, '/tools')

    const rows = page.getByTestId('tool-call-row')
    const originalCount = await rows.count()
    expect(originalCount, 'demo seed should produce at least one tool call').toBeGreaterThanOrEqual(1)

    // Pick a tool name that appears on some but not all loaded rows, so filtering by it
    // demonstrably narrows the set — the most frequent one under the total, for a robust signal.
    const freq = await toolNameFrequency(page)
    expect(freq.size, 'demo seed should load more than one distinct tool name').toBeGreaterThan(1)
    let target = ''
    let targetCount = 0
    for (const [name, count] of freq) {
      if (count < originalCount && count > targetCount) {
        target = name
        targetCount = count
      }
    }
    expect(target, 'should find a tool name that does not cover every loaded row').not.toBe('')

    await page.getByTestId('tools-search').fill(target)

    await expect(rows).toHaveCount(targetCount)
    const remainingNames = await toolNameCells(page).allInnerTexts()
    for (const name of remainingNames) {
      expect(name.trim().toLowerCase()).toContain(target.toLowerCase())
    }

    await page.getByTestId('tools-search').fill('')
    await expect(rows).toHaveCount(originalCount)
  })
})

test.describe('Data quality', () => {
  test('loads with tiles, unmapped-event section and hook-latency panel', async ({ page }) => {
    await gotoReady(page, '/data-quality')

    await expect(page.getByTestId('data-quality-view')).toBeVisible()
    await expect(page.getByTestId('quality-tiles')).toBeVisible()
    await expect(page.getByTestId('quality-tile-unknown-events-value')).toBeVisible()
    await expect(page.getByTestId('quality-tile-dropped-total')).toBeVisible()
    await expect(page.getByTestId('quality-tile-dropped-total-value')).toBeVisible()
    await expect(page.getByTestId('quality-tile-partial-sessions')).toBeVisible()
    await expect(page.getByTestId('quality-tile-clock-skewed')).toBeVisible()
    await expect(page.getByTestId('quality-tile-heuristic-share')).toBeVisible()
    await expect(page.getByTestId('quality-tile-oldest-raw-event')).toBeVisible()

    // Both UnknownKindTable and HookLatencyPanel render an unconditional outer wrapper — the
    // empty state is nested INSIDE it, not a replacement for it — and both reuse the same generic
    // `empty-state` testid, so each check is scoped to its own container to stay unambiguous.
    const unknownKindTable = page.getByTestId('unknown-kind-table')
    await expect(unknownKindTable).toBeVisible()
    const unknownHasRows = await unknownKindTable.getByTestId('unknown-kind-count').count()
    const unknownHasEmptyState = await unknownKindTable.getByTestId('empty-state').count()
    expect(
      unknownHasRows > 0 || unknownHasEmptyState > 0,
      'unmapped-event-names section should render either rows or its empty state',
    ).toBeTruthy()

    const hookLatencyPanel = page.getByTestId('hook-latency-panel')
    await expect(hookLatencyPanel).toBeVisible()
    const hookHasRows = await hookLatencyPanel.getByTestId('hook-latency-executions').count()
    const hookHasEmptyState = await hookLatencyPanel.getByTestId('empty-state').count()
    expect(
      hookHasRows > 0 || hookHasEmptyState > 0,
      'hook-latency section should render either rows or its empty state',
    ).toBeTruthy()
  })
})
