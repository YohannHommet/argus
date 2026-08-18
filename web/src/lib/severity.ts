/**
 * Data-driven severity grading for the sessions table's Reject % and Cost
 * columns (round-4 UI gap: "the table encodes no severity in color"). Pure,
 * no Vue imports — usable from stores/components/tests alike, same spirit
 * as `format.ts`.
 */

export type Severity = 'neutral' | 'warn' | 'critical'

const REJECT_RATE_WARN_THRESHOLD = 0.05
const REJECT_RATE_CRITICAL_THRESHOLD = 0.15

/**
 * Fixed absolute thresholds, not relative to the visible page: a reject
 * rate is a rate regardless of which sessions happen to be loaded, so
 * "5% rejects" should read the same whether it's sitting next to a page of
 * mostly-0% sessions or a page of mostly-10% ones. `null`/`undefined`/`NaN`
 * (rate is undefined, e.g. zero tool calls — SPEC §6.1) grades neutral: an
 * unmeasured rate is not evidence of a problem.
 */
export function classifyRejectRateSeverity(rate: number | null | undefined): Severity {
  if (typeof rate !== 'number' || !Number.isFinite(rate)) return 'neutral'
  if (rate >= REJECT_RATE_CRITICAL_THRESHOLD) return 'critical'
  if (rate >= REJECT_RATE_WARN_THRESHOLD) return 'warn'
  return 'neutral'
}

export interface CostThresholds {
  warn: number
  critical: number
}

const NO_OUTLIERS: CostThresholds = { warn: Infinity, critical: Infinity }

/**
 * Cost has no natural absolute scale (a $6 session is unremarkable next to
 * $5-10 sessions, but the clear standout next to $0.10-$1 sessions), so
 * thresholds are derived from the visible set's own distribution: the 75th
 * and 90th percentiles of its non-null costs. When the visible set has no
 * spread (0 or 1 distinct cost value) there is nothing to call an outlier,
 * so both thresholds are `Infinity` — every row grades neutral rather than
 * everything lighting up "critical" together (data-driven, not christmas
 * lights).
 */
export function computeCostThresholds(costs: ReadonlyArray<number | null | undefined>): CostThresholds {
  const values = costs
    .filter((c): c is number => typeof c === 'number' && Number.isFinite(c))
    .sort((a, b) => a - b)

  if (values.length === 0 || values[0] === values[values.length - 1]) return NO_OUTLIERS

  const percentile = (p: number): number => {
    const idx = (values.length - 1) * p
    const lo = Math.floor(idx)
    const hi = Math.ceil(idx)
    if (lo === hi) return values[lo]
    return values[lo] + (values[hi] - values[lo]) * (idx - lo)
  }

  return { warn: percentile(0.75), critical: percentile(0.9) }
}

/** Grades a single cost against thresholds already computed for its visible set (`computeCostThresholds`). */
export function classifyCostSeverity(
  cost: number | null | undefined,
  thresholds: CostThresholds,
): Severity {
  if (typeof cost !== 'number' || !Number.isFinite(cost)) return 'neutral'
  if (cost >= thresholds.critical) return 'critical'
  if (cost >= thresholds.warn) return 'warn'
  return 'neutral'
}

/** Tailwind text-color utility for a severity grade — `--warn`/`--destructive` tokens, never a new color. */
export function severityTextClass(severity: Severity): string | undefined {
  if (severity === 'critical') return 'text-destructive'
  if (severity === 'warn') return 'text-warn'
  return undefined
}
