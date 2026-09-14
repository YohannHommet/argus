// Shared axe-core runner: vitest-axe's toHaveNoViolations fails on any impact, but reka-ui primitives and third-party chart canvases carry minor/moderate violations Argus can't fix — this filters to critical/serious only and reports exactly which rule failed and where.
import { configureAxe } from 'vitest-axe'
import type { Result } from 'axe-core'

const SERIOUS_IMPACTS = new Set(['critical', 'serious'])

const runAxe = configureAxe({
  rules: {
    // jsdom has no layout/paint engine, so color-contrast only ever produces false-positive noise here — contrast is verified visually, not via jsdom.
    'color-contrast': { enabled: false },
  },
})

function describeViolation(violation: Result): string {
  const targets = violation.nodes.map((node) => `  - ${node.target.join(' ')}`).join('\n')
  return `[${violation.impact}] ${violation.id}: ${violation.help} (${violation.helpUrl})\n${targets}`
}

/**
 * Runs axe-core against `container` and throws (with a human-readable report) if any violation has
 * impact "critical" or "serious". A "minor"/"moderate" violation is not swallowed silently: it just
 * doesn't fail the assertion, matching the bar this project actually gates on.
 */
export async function assertNoSeriousA11yViolations(container: Element): Promise<void> {
  const results = await runAxe(container)
  const serious = results.violations.filter((v) => v.impact !== null && v.impact !== undefined && SERIOUS_IMPACTS.has(v.impact))

  if (serious.length > 0) {
    const report = serious.map(describeViolation).join('\n\n')
    throw new Error(`${serious.length} critical/serious accessibility violation(s):\n\n${report}`)
  }
}
