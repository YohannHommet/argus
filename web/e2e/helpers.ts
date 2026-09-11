import { expect, type APIRequestContext, type Page } from '@playwright/test'

/**
 * Shared E2E helpers. Two jobs: (1) hit the same REST API the SPA hits, so a
 * test can assert the rendered UI against ground truth (SPEC §6.1's whole
 * thesis is that the UI must not invent or drop numbers — the only honest check
 * is API-vs-UI); (2) a single readiness primitive so no spec falls back to a
 * bare sleep.
 */

export const BASE_URL = process.env.ARGUS_E2E_BASE_URL ?? 'http://localhost:18090'

/** Demo-seed project names as they appear in the read API (verified against a
 * running seed, not just the sim source). `legacy-app` is metrics-only, so it
 * never produces rows in the sessions list — only the first four are visible
 * there. Kept here so a seed change is a one-line update, not a hunt. */
export const DEMO_PROJECTS = ['argus', 'platform', 'studio', 'dotfiles', 'legacy-app'] as const

/** GET a JSON endpoint on the running stack, failing loudly (with the path in
 * the message) on any non-2xx — a silent `{}` would turn an API regression into
 * a confusing UI assertion failure three lines later. */
export async function apiGet<T = unknown>(request: APIRequestContext, path: string): Promise<T> {
  const res = await request.get(`${BASE_URL}${path}`)
  expect(res.ok(), `GET ${path} -> ${res.status()} ${res.statusText()}`).toBeTruthy()
  return res.json() as Promise<T>
}

/**
 * Navigate to a route and wait for the view's OWN readiness contract, never a
 * timeout: every top-level view sets `data-capture-ready="true"` on <html> once
 * its initial fetch has settled (data, empty state, or error — see
 * useCaptureReady.ts). The attribute is removed on client-side navigation and
 * re-applied by the next view, so this is also the right wait after an in-app
 * link click.
 */
export async function gotoReady(page: Page, path: string): Promise<void> {
  await page.goto(path, { waitUntil: 'domcontentloaded' })
  await waitForReady(page)
}

/** Wait for the current view to (re)assert `data-capture-ready` — for use after
 * an in-app navigation that doesn't go through `gotoReady`. */
export async function waitForReady(page: Page): Promise<void> {
  await expect(page.locator('html[data-capture-ready="true"]')).toHaveCount(1, { timeout: 30_000 })
}

/** Parse a formatted currency string (`$1,234.56`, `$0.0004`) back to whole
 * cents, so an API-vs-UI cost check compares integers and never flaps on
 * float representation. Returns NaN if no number is present. */
export function moneyToCents(text: string): number {
  const match = text.replace(/,/g, '').match(/\$?(-?[0-9]+(?:\.[0-9]+)?)/)
  return match ? Math.round(Number.parseFloat(match[1]) * 100) : Number.NaN
}

/** The em-dash every formatter renders for a null/unknown value (SPEC §6.1). A
 * rendered `0` is a real measurement and must never collapse to this. */
export const EM_DASH = '—'
