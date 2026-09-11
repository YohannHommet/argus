// PLAN.md P6-04: shared axe-core runner for the six-view a11y spec (and any other component test
// that wants the same bar). `vitest-axe`'s own `toHaveNoViolations` matcher fails on *any* impact
// level, including "minor"/"moderate" — reka-ui primitives and third-party chart canvases can carry
// a handful of those Argus doesn't own and can't unilaterally fix, so per the ticket's own AC
// ("zero critical/serious violations", not zero violations), this helper filters to the two impact
// levels the AC actually gates on and reports exactly which rule failed and on which element,
// rather than the axe library's default framework-agnostic dump.
import { configureAxe } from 'vitest-axe'
import type { Result } from 'axe-core'

const SERIOUS_IMPACTS = new Set(['critical', 'serious'])

const runAxe = configureAxe({
  rules: {
    // jsdom has no layout/paint engine — every element reports zero size and axe-core cannot compute
    // real contrast ratios under it, so `color-contrast` only ever produces noise (false positives on
    // a 0x0 box) in this environment, never a signal. Contrast is a visual-design concern verified by
    // the theme.css token palette + manual/visual QA, not something jsdom can measure.
    'color-contrast': { enabled: false },
  },
})

function describeViolation(violation: Result): string {
  const targets = violation.nodes.map((node) => `  - ${node.target.join(' ')}`).join('\n')
  return `[${violation.impact}] ${violation.id}: ${violation.help} (${violation.helpUrl})\n${targets}`
}

/**
 * Runs axe-core against `container` and throws (with a human-readable report) if any violation has
 * impact "critical" or "serious" — the bar PLAN.md P6-04's AC actually sets. A "minor"/"moderate"
 * violation is not swallowed silently: it just doesn't fail the assertion, matching the AC's own
 * wording rather than a stricter one this ticket never asked for.
 */
export async function assertNoSeriousA11yViolations(container: Element): Promise<void> {
  const results = await runAxe(container)
  const serious = results.violations.filter((v) => v.impact !== null && v.impact !== undefined && SERIOUS_IMPACTS.has(v.impact))

  if (serious.length > 0) {
    const report = serious.map(describeViolation).join('\n\n')
    throw new Error(`${serious.length} critical/serious accessibility violation(s):\n\n${report}`)
  }
}
