// Playwright half of the Phase-4 screenshot harness. scripts/ui-capture.sh
// owns the stack (compose up, sim seed, readiness); this file owns the
// browser. Run it directly only if a stack is already up:
//
//   ARGUS_BASE_URL=http://localhost:18080 ARGUS_OUT_DIR=/tmp/shots \
//     node web/scripts/ui-capture.mjs
//
// Lives under web/ so Node resolves `playwright` from web/node_modules.
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'

import { chromium } from 'playwright'

const baseUrl = (process.env.ARGUS_BASE_URL ?? 'http://localhost:18080').replace(/\/+$/, '')
const outDir = resolve(process.env.ARGUS_OUT_DIR ?? 'screenshots')

// SPEC §6 is dark-first and the visual gauntlet reviews the dark theme, so
// the viewport and theme are fixed, not configurable.
const VIEWPORT = { width: 1440, height: 900 }

function log(...args) {
  console.log('[ui-capture]', ...args)
}

async function getJson(path) {
  const res = await fetch(`${baseUrl}${path}`)
  if (!res.ok) {
    throw new Error(`GET ${path} -> ${res.status} ${res.statusText}`)
  }
  return res.json()
}

/**
 * Picks the session the gauntlet should look at: the most tool calls among
 * those that actually have a subagent tree, so the detail screenshots show
 * a populated Timeline *and* a populated Subagents tab. Falls back to the
 * busiest session overall if the demo run produced no subagents at all,
 * rather than failing the capture — a thin screenshot beats none.
 */
async function pickDetailSession() {
  const { data } = await getJson('/api/v1/sessions?limit=500')
  if (!Array.isArray(data) || data.length === 0) {
    throw new Error('no sessions in the API response — did the sim seed run?')
  }
  const byTools = (a, b) => (b.tool_call_count ?? 0) - (a.tool_call_count ?? 0)
  const withSubagents = data.filter((s) => (s.subagent_count ?? 0) > 0).sort(byTools)
  const chosen = (withSubagents[0] ?? [...data].sort(byTools)[0])
  log(
    `detail session ${chosen.id} (project=${chosen.project} ` +
      `tool_calls=${chosen.tool_call_count} subagents=${chosen.subagent_count})`,
  )
  return chosen
}

/**
 * Screenshots one route. `waitFor` is a selector that must be present and
 * visible before the shutter fires; it is what stops the harness from
 * handing the gauntlet a picture of a skeleton. A miss is fatal: a blank
 * or still-loading screenshot that silently passes review is worse than a
 * failed capture run.
 */
async function capture(page, { name, path, waitFor }) {
  const url = `${baseUrl}${path}`
  log(`capturing ${name} <- ${url}`)
  await page.goto(url, { waitUntil: 'networkidle', timeout: 60_000 })
  await page.waitForSelector(waitFor, { state: 'visible', timeout: 30_000 })
  // Charts animate in and ECharts lays out on the next frame; two rAFs plus
  // a short settle beats an arbitrary long sleep.
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  )
  await page.waitForTimeout(750)
  const file = join(outDir, `${name}.png`)
  await mkdir(dirname(file), { recursive: true })
  await page.screenshot({ path: file, fullPage: false })
  return file
}

/**
 * `session-detail-inspector.png` (round-3 harness addition): the Timeline
 * tab with a meaningful row already selected, so the static capture shows
 * the inspector populated rather than its "no event selected" placeholder
 * — a screenshot of an empty inspector would prove nothing about whether
 * the structured-summary work (kind/tool/decision badge/duration/cost/
 * tokens — `EventDetailContent.vue`) actually renders.
 *
 * Picks a tool-call row that carries a decision when one exists (the
 * richest payload: decision + decision_source + duration all populate),
 * falling back to the highest-cost visible row (typically an LLM request)
 * so the capture is never empty even on a session with zero tool
 * decisions. 1440px is `EventInspector`'s wide breakpoint (>=1024px), so
 * this also exercises the persistent panel, not the overlay sheet.
 */
async function captureInspector(page, session) {
  const name = 'session-detail-inspector'
  const url = `${baseUrl}/sessions/${session.id}?tab=timeline`
  log(`capturing ${name} <- ${url}`)
  await page.goto(url, { waitUntil: 'networkidle', timeout: 60_000 })
  await page.waitForSelector('[data-capture-ready="true"]', { state: 'visible', timeout: 30_000 })

  const decisionRows = page.locator('[data-testid="event-row"]:has([data-testid="decision-badge"])')
  let target
  if ((await decisionRows.count()) > 0) {
    target = decisionRows.first()
  } else {
    const rows = page.locator('[data-testid="event-row"]')
    const count = await rows.count()
    if (count === 0) {
      throw new Error(`${name}: no event rows on the timeline to select — is the seeded session empty?`)
    }
    const costPattern = /\$([0-9]+(?:\.[0-9]+)?)/
    let bestIndex = 0
    let bestCost = -Infinity
    for (let i = 0; i < count; i += 1) {
      const text = await rows.nth(i).innerText()
      const match = text.match(costPattern)
      const cost = match ? Number.parseFloat(match[1]) : -Infinity
      if (cost > bestCost) {
        bestCost = cost
        bestIndex = i
      }
    }
    target = rows.nth(bestIndex)
  }

  await target.scrollIntoViewIfNeeded()
  await target.click()

  // The inspector fetches GET /events/{ref} on click — wait for the
  // structured summary itself (not just the panel/sheet chrome) so this
  // never photographs a loading skeleton.
  await page.waitForSelector('[data-testid="event-detail-summary"]', { state: 'visible', timeout: 30_000 })
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  )
  await page.waitForTimeout(750)

  const file = join(outDir, `${name}.png`)
  await mkdir(dirname(file), { recursive: true })
  await page.screenshot({ path: file, fullPage: false })
  return file
}

async function main() {
  await mkdir(outDir, { recursive: true })
  const session = await pickDetailSession()

  const browser = await chromium.launch({
    // WSL2 headless: no sandbox namespaces available in every configuration,
    // and this browser only ever loads localhost pages the harness started.
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--force-color-profile=srgb'],
  })
  const context = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: 1,
    // Belt and braces on the theme. `colorScheme` drives the
    // prefers-color-scheme branch of index.html's anti-flash script; the
    // localStorage seed drives the branch that wins over it (src/stores/ui.ts
    // precedence: stored > prefers-color-scheme > dark). Setting both means
    // the capture is dark whichever branch the app takes.
    colorScheme: 'dark',
    reducedMotion: 'reduce',
  })
  await context.addInitScript(() => {
    try {
      window.localStorage.setItem('argus-ui', JSON.stringify({ theme: 'dark' }))
    } catch {
      // Storage disabled in this context; colorScheme: 'dark' still applies.
    }
  })

  const page = await context.newPage()
  const consoleErrors = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') consoleErrors.push(msg.text())
  })
  page.on('pageerror', (err) => consoleErrors.push(`pageerror: ${err.message}`))

  // `data-capture-ready` is set by each view once its first fetch has
  // resolved (loading and skeletons gone). Views own that contract so the
  // harness never has to guess a per-page selector.
  const ready = '[data-capture-ready="true"]'
  const targets = [
    { name: 'sessions', path: '/sessions', waitFor: ready },
    { name: 'session-detail-timeline', path: `/sessions/${session.id}?tab=timeline`, waitFor: ready },
    {
      name: 'session-detail-subagents',
      path: `/sessions/${session.id}?tab=subagents`,
      waitFor: ready,
    },
    // Two analytics shots, on purpose. The view defaults to a 24h window,
    // but `sim --mode=demo` backfills event timestamps across 14 days, so
    // the default view honestly holds only a fraction of the seeded data
    // (measured: $4.24 of $39.78, with correspondingly thin charts). That
    // default is what an operator sees on a quiet day and is worth
    // reviewing; a design review also needs a populated dashboard, which
    // `?window=30d` gives by covering the whole backfill. Neither is more
    // correct than the other — they are two real states of one screen.
    { name: 'analytics', path: '/analytics', waitFor: ready },
    { name: 'analytics-30d', path: '/analytics?window=30d', waitFor: ready },
    { name: 'tools', path: '/tools', waitFor: ready },
    { name: 'data-quality', path: '/data-quality', waitFor: ready },
  ]

  const written = []
  try {
    for (const target of targets) {
      written.push(await capture(page, target))
    }
    written.push(await captureInspector(page, session))
  } finally {
    await context.close()
    await browser.close()
  }

  await writeFile(
    join(outDir, 'capture.json'),
    `${JSON.stringify(
      {
        base_url: baseUrl,
        viewport: VIEWPORT,
        theme: 'dark',
        detail_session_id: session.id,
        detail_session_tool_calls: session.tool_call_count,
        detail_session_subagents: session.subagent_count,
        screenshots: written.map((f) => f.replace(`${outDir}/`, '')),
        console_errors: consoleErrors,
        captured_at: new Date().toISOString(),
      },
      null,
      2,
    )}\n`,
    'utf8',
  )

  log(`wrote ${written.length} screenshots to ${outDir}`)
  if (consoleErrors.length > 0) {
    // Not fatal — a console error does not necessarily mean a blank page,
    // and the gauntlet still wants the images. Loud, and recorded in
    // capture.json, so it cannot be missed either.
    log(`WARNING: ${consoleErrors.length} browser console error(s):`)
    for (const e of consoleErrors.slice(0, 10)) log(`  ${e}`)
  }
}

main().catch((err) => {
  console.error('[ui-capture] FAILED:', err)
  process.exitCode = 1
})
