/**
 * Pure period-over-period math for the KPI strip's deltas/sparklines
 * (round-5 UI pass: "flat, equal-weight numbers with no comparison or
 * trend context"). Deliberately has no Vue/store imports — it operates on
 * the raw `Series` shape `GET /analytics/timeseries` already returns, so
 * a delta is always "current window's actual buckets minus the preceding
 * window's actual buckets", never a client-side guess.
 *
 * Two null cases stay distinct, matching SPEC §6.1's null-vs-zero thesis:
 *  - `series` itself `null` (the resource was skipped — not attributable
 *    under a `?model=` filter, or simply never fetched) -> both
 *    `seriesTotal` and `computeDelta` return `null`, never `0`.
 *  - an empty/all-zero *preceding* window (a real `Series` with zero-value
 *    buckets, e.g. a brand-new project) -> `seriesTotal` returns `0` (a
 *    real measurement), so `computeDelta` returns a real number (typically
 *    the full current total, a "+100%"-style jump from zero) rather than
 *    `null`.
 */
import type { components } from '@/api/schema'

export type Series = components['schemas']['Series']

/**
 * Sums every bucket across every named series plus `other` — the same
 * "sum(series)+other == sum(all rows) per bucket" invariant the backend
 * builds `Series` under (see `read_analytics.go`'s `buildSeries`), so this
 * total is correct regardless of `group_by` (per-series values just
 * partition the same underlying rows differently).
 */
export function seriesTotal(series: Series | null | undefined): number | null {
  if (!series) return null
  let total = 0
  for (const point of series.series) {
    for (const value of point.values) total += value
  }
  if (series.other) {
    for (const value of series.other.values) total += value
  }
  return total
}

/**
 * `current - previous`, in the metric's own unit. `null` whenever either
 * side is `null` — a delta against an unknown baseline (or an unknown
 * current value) is meaningless, matching `StatTile`'s own "a null value
 * never renders a delta" rule.
 */
export function computeDelta(current: number | null, previous: number | null): number | null {
  if (current === null || previous === null) return null
  return current - previous
}

/**
 * `current - previous` computed directly off two `Series` (or `null`s) in
 * one call — the shape every KPI tile actually has on hand.
 */
export function seriesDelta(current: Series | null | undefined, previous: Series | null | undefined): number | null {
  return computeDelta(seriesTotal(current), seriesTotal(previous))
}

/**
 * One value per bucket (summed across every named series + `other`) for
 * the inline sparkline — the current window only; a sparkline never plots
 * the preceding window (that's what the delta is for). `null` (never an
 * empty/zero-filled array) when there is no series to plot, so a caller
 * can tell "no data" apart from "one zero-valued bucket".
 */
export function sparklineValues(series: Series | null | undefined): number[] | null {
  if (!series || series.buckets.length === 0) return null
  const totals = new Array<number>(series.buckets.length).fill(0)
  for (const point of series.series) {
    point.values.forEach((value, index) => {
      totals[index] += value
    })
  }
  if (series.other) {
    series.other.values.forEach((value, index) => {
      totals[index] += value
    })
  }
  return totals
}
