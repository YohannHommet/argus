import { defineConfig, devices } from '@playwright/test'

/**
 * Assertion-based browser E2E for the embedded Argus SPA. The stack is owned by
 * scripts/e2e.sh (compose up on a non-default port, seed, teardown) — this file
 * owns only the browser. Run it against an already-up stack with:
 *
 *   ARGUS_E2E_BASE_URL=http://localhost:18090 pnpm exec playwright test
 *
 * Chromium only, pinned to the repo's `playwright` version. Two phases the
 * runner drives separately (see e2e.sh), because the live suite needs a load
 * sim streaming *while it runs* and that sim adds sessions the count/total
 * assertions in the other specs must not see:
 *   - main:  `playwright test --grep-invert @live`
 *   - live:  `ARGUS_E2E_LIVE=1 playwright test --grep @live`
 * A bare local `playwright test` is still safe: the @live spec self-skips
 * unless ARGUS_E2E_LIVE is set (see live.spec.ts), so it never runs the live
 * assertions without the load-sim orchestration they depend on. Config-level
 * `grep`/`grepInvert` is intentionally NOT set — it ANDs with the CLI's, which
 * would make `--grep @live` match nothing.
 */
const baseURL = process.env.ARGUS_E2E_BASE_URL ?? 'http://localhost:18090'
const isCI = !!process.env.CI

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: isCI,
  // WSL2 headless chromium is flaky under Docker load; a single retry absorbs a
  // one-off crash without masking a real, reproducible failure. CI (clean
  // runner) is the source of truth and gets one more.
  retries: isCI ? 2 : 1,
  // Serial: the browser and the whole stack share one box. Parallel browser
  // contexts on WSL2 under Docker load are exactly what makes headless chromium
  // crash here — throughput is not the goal, a trustworthy gate is.
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: isCI
    ? [['github'], ['list'], ['html', { open: 'never' }]]
    : [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL,
    // Wide viewport: the event inspector renders its persistent side panel at
    // >=1024px (EventInspector.vue's useMediaQuery) rather than the overlay
    // sheet, which is the path detail.spec asserts against.
    viewport: { width: 1440, height: 900 },
    colorScheme: 'dark',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 900 },
        // WSL2/CI containers: no usable sandbox namespaces, and this browser
        // only ever loads the localhost stack e2e.sh started. Mirrors
        // scripts/ui-capture.mjs's launch args.
        launchOptions: { args: ['--no-sandbox', '--disable-dev-shm-usage'] },
      },
    },
  ],
})
