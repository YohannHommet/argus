import { expect, test } from '@playwright/test'

import { apiGet, EM_DASH, gotoReady } from './helpers'
import { formatCost, formatTokens } from '../src/lib/format'

/**
 * Flow 3 — the fleet analytics dashboard (SPEC §4.3). Demo events are
 * backfilled over ~14 days, so every assertion here uses the 30-day window
 * (`?window=30d` -> `from=-30d`, see stores/analytics.ts's `PRESET_FROM`) —
 * the default 24h view only holds a thin slice of the seed. Assertions are
 * either cross-checked against the REST API (never a hardcoded $/token
 * figure) or structural, matching sessions.spec.ts's style.
 */

const ANALYTICS_30D = '/analytics?window=30d'
const SUMMARY_30D = '/api/v1/analytics/summary?from=-30d'

interface AnalyticsSummary {
  sessions: number | null
  turns: number | null
  api_requests: number
  api_errors: number
  tool_calls: number | null
  tool_rejects: number | null
  reject_rate: number | null
  tokens: { input: number; output: number; cache_read: number; cache_creation: number }
  cost: { usd: number; estimated_usd: number; estimated_share: number }
  loc: { added: number | null; removed: number | null }
  active_seconds: number | null
  not_attributable: string[]
  metrics_only_projects: string[]
}

interface Facets {
  projects: string[]
  models: string[]
  vendors: string[]
}

const statValue = (page: import('@playwright/test').Page, kpiTestId: string) =>
  page.getByTestId(kpiTestId).getByTestId('stat-tile-value')

test.describe('Analytics dashboard', () => {
  test('KPI tiles load for the 30-day window', async ({ page }) => {
    await gotoReady(page, ANALYTICS_30D)

    for (const kpi of ['kpi-cost', 'kpi-tokens', 'kpi-api-requests']) {
      await expect(page.getByTestId(kpi)).toBeVisible()
      // A non-empty value proves the tile settled on real data (or an honest
      // em-dash) rather than sitting on its loading skeleton.
      await expect(statValue(page, kpi)).toHaveText(/\S/)
    }
  })

  test('rendered Cost and Tokens match the API to the cent', async ({ page, request }) => {
    const summary = await apiGet<AnalyticsSummary>(request, SUMMARY_30D)

    await gotoReady(page, ANALYTICS_30D)

    // Import the real pure formatters (src/lib/format.ts has no Vue deps) so the
    // expected string is computed the exact same way the UI computes it — the
    // only honest way to assert "UI matches API to the cent".
    await expect(statValue(page, 'kpi-cost')).toHaveText(formatCost(summary.cost.usd))
    await expect(statValue(page, 'kpi-tokens')).toHaveText(formatTokens(summary.tokens.input + summary.tokens.output))
  })

  test('a model filter renders — (not 0) for non-attributable counters', async ({ page, request }) => {
    const facets = await apiGet<Facets>(request, '/api/v1/facets')
    expect(facets.models.length, 'demo seed should produce at least one model').toBeGreaterThan(0)
    const model = facets.models[0]

    // Ground truth first: the server itself must return null (never 0) for
    // sessions under a model filter, and list it in not_attributable.
    const filtered = await apiGet<AnalyticsSummary>(request, `${SUMMARY_30D}&model=${encodeURIComponent(model)}`)
    expect(filtered.sessions, 'sessions has no model column — must be null under a model filter').toBeNull()
    expect(filtered.not_attributable).toContain('sessions')

    // Drive the model filter through the URL — the same store path the Select
    // control feeds (stores/analytics.ts's parseAnalyticsQuery reads
    // `query.model`), and robust for a CI gate. Driving the control itself is
    // not an option here: its `[data-testid="filter-model"]` is set on <Select>,
    // but reka-ui's SelectRoot has `inheritAttrs:false` and renders no DOM node,
    // so the testid never reaches the page (all four analytics filter Selects
    // share this — worth a testability fix upstream, moving the testid onto the
    // SelectTrigger).
    await gotoReady(page, `${ANALYTICS_30D}&model=${encodeURIComponent(model)}`)

    const sessionsValue = statValue(page, 'kpi-sessions')
    await expect(sessionsValue).toHaveText(EM_DASH)
    await expect(sessionsValue).not.toHaveText('0')
  })

  test('the cost timeseries chart renders in the default 30d view', async ({ page }) => {
    await gotoReady(page, ANALYTICS_30D)

    const chart = page.locator('[data-testid="panel-cost-timeseries"] canvas, [data-testid="panel-cost-timeseries"] svg').first()
    await expect(chart).toBeVisible()

    // vue-echarts renders via CanvasRenderer (lib/echarts.ts) — at least one
    // canvas on the page proves a chart actually mounted, not just its panel chrome.
    const canvasCount = await page.locator('canvas').count()
    expect(canvasCount).toBeGreaterThanOrEqual(1)
  })
})
