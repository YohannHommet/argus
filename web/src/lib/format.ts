/**
 * Pure formatting helpers (no Vue imports — usable from stores, tests, and
 * components alike). Every function returns `EM_DASH` for `null`/
 * `undefined`/unparseable input rather than `0`, `''`, or `Invalid Date`:
 * SPEC §6.1 treats "we don't know" and "we measured zero" as different
 * facts, and a formatter is the last line of defence against collapsing
 * them back together.
 */

export const EM_DASH = '—'

type Numeric = number | null | undefined

function isFiniteNumber(n: Numeric): n is number {
  return typeof n === 'number' && Number.isFinite(n)
}

function trimTrailingZero(s: string): string {
  return s.endsWith('.0') ? s.slice(0, -2) : s
}

/**
 * `$0.0004` below one cent (a sub-cent per-token cost would otherwise
 * round to `$0.00` and look free), `$12.34` / `$1,234.56` at normal
 * magnitudes. A measured `0` renders `$0.00` — it's a real reading, not a
 * missing one — so only `null`/`undefined`/`NaN` fall back to `EM_DASH`.
 */
export function formatCost(usd: Numeric): string {
  if (!isFiniteNumber(usd)) return EM_DASH
  const decimals = usd !== 0 && Math.abs(usd) < 0.01 ? 4 : 2
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  }).format(usd)
}

/**
 * Full-precision cost for tooltips/detail rows — up to 6 decimals, only as
 * many as the value actually needs (Intl drops digits beyond the value's
 * own precision as long as they're above `minimumFractionDigits`).
 */
export function formatCostPrecise(usd: Numeric): string {
  if (!isFiniteNumber(usd)) return EM_DASH
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  }).format(usd)
}

/** Grouped integer (`1,234`). Rounds — this is for counts, not measurements. */
export function formatCount(n: Numeric): string {
  if (!isFiniteNumber(n)) return EM_DASH
  return new Intl.NumberFormat('en-US').format(Math.round(n))
}

const TOKEN_UNITS: { value: number; suffix: string }[] = [
  { value: 1e12, suffix: 'T' },
  { value: 1e9, suffix: 'B' },
  { value: 1e6, suffix: 'M' },
  { value: 1e3, suffix: 'K' },
]

/**
 * SI-suffixed token counts: `1.2K`, `1.2M`, but a plain `999` below 1000 —
 * a single decimal, trimmed when it's `.0` (`2000000` -> `2M`, not `2.0M`).
 */
export function formatTokens(n: Numeric): string {
  if (!isFiniteNumber(n)) return EM_DASH
  const abs = Math.abs(n)
  for (const unit of TOKEN_UNITS) {
    if (abs >= unit.value) {
      return `${trimTrailingZero((n / unit.value).toFixed(1))}${unit.suffix}`
    }
  }
  return formatCount(n)
}

/**
 * `450ms` below a second, `4.5s` below a minute (one decimal — sub-second
 * precision matters at that scale, e.g. hook latency), then `29m 33s`,
 * `2h 04m` (zero-padded minutes), `3d 04h` (zero-padded hours) at coarser
 * scales where seconds/minutes stop being useful and alignment in a
 * column of durations does.
 */
export function formatDuration(ms: Numeric): string {
  if (!isFiniteNumber(ms) || ms < 0) return EM_DASH
  if (ms < 1000) return `${Math.round(ms)}ms`

  const totalSeconds = ms / 1000
  if (totalSeconds < 60) return `${trimTrailingZero(totalSeconds.toFixed(1))}s`

  const totalSecondsInt = Math.floor(totalSeconds)
  const totalMinutes = Math.floor(totalSecondsInt / 60)
  if (totalMinutes < 60) {
    const seconds = totalSecondsInt % 60
    return `${totalMinutes}m ${String(seconds).padStart(2, '0')}s`
  }

  const totalHours = Math.floor(totalMinutes / 60)
  if (totalHours < 24) {
    const minutes = totalMinutes % 60
    return `${totalHours}h ${String(minutes).padStart(2, '0')}m`
  }

  const days = Math.floor(totalHours / 24)
  const hours = totalHours % 24
  return `${days}d ${String(hours).padStart(2, '0')}h`
}

type RelativeDivision = { amount: number; unit: Intl.RelativeTimeFormatUnit }

// Standard "pick the coarsest unit that still rounds to >= 1" ladder.
const RELATIVE_DIVISIONS: RelativeDivision[] = [
  { amount: 60, unit: 'second' },
  { amount: 60, unit: 'minute' },
  { amount: 24, unit: 'hour' },
  { amount: 7, unit: 'day' },
  { amount: 4.34524, unit: 'week' },
  { amount: 12, unit: 'month' },
  { amount: Number.POSITIVE_INFINITY, unit: 'year' },
]

const relativeFormatter = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })

/**
 * `"3 minutes ago"` via `Intl.RelativeTimeFormat`. Returns `EM_DASH` for a
 * null/unparseable timestamp rather than `Invalid Date` — the `partial:
 * true` session case (`started_at: null`, SPEC §1.7) must never format an
 * un-formattable date.
 */
export function formatRelativeTime(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return EM_DASH
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return EM_DASH

  let duration = (now.getTime() - date.getTime()) / 1000
  for (const division of RELATIVE_DIVISIONS) {
    if (Math.abs(duration) < division.amount) {
      return relativeFormatter.format(Math.round(-duration), division.unit)
    }
    duration /= division.amount
  }
  return relativeFormatter.format(Math.round(-duration), 'year')
}

/** Absolute timestamp for tooltips/detail rows, via `Intl.DateTimeFormat`. */
export function formatAbsoluteTime(
  iso: string | null | undefined,
  opts: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'medium' },
): string {
  if (!iso) return EM_DASH
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return EM_DASH
  return new Intl.DateTimeFormat('en-US', opts).format(date)
}

/** `0.0412` -> `4.1%`. */
export function formatPercent(fraction: Numeric, digits = 1): string {
  if (!isFiniteNumber(fraction)) return EM_DASH
  return new Intl.NumberFormat('en-US', {
    style: 'percent',
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(fraction)
}

/** Alias for a reject/error rate — same shape as `formatPercent`, named for callsite clarity. */
export function formatRejectRate(fraction: Numeric, digits = 1): string {
  return formatPercent(fraction, digits)
}
