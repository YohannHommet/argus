import { describe, expect, it } from 'vitest'

import { partialSessionSummary } from '@/test/fixtures.extra'
import {
  EM_DASH,
  formatAbsoluteTime,
  formatCost,
  formatCount,
  formatDuration,
  formatPercent,
  formatRelativeOffset,
  formatRelativeTime,
  formatTokens,
} from './format'

describe('formatCost', () => {
  it('renders sub-cent amounts to 4 decimals', () => {
    expect(formatCost(0.0004)).toBe('$0.0004')
  })

  it('renders normal amounts to 2 decimals', () => {
    expect(formatCost(12.34)).toBe('$12.34')
  })

  it('never rounds a cent-precision value to 1 decimal', () => {
    expect(formatCost(12.3)).toBe('$12.30')
  })

  it('groups thousands', () => {
    expect(formatCost(1234.56)).toBe('$1,234.56')
  })

  it('renders a measured zero as $0.00, not a dash', () => {
    expect(formatCost(0)).toBe('$0.00')
  })

  it('returns EM_DASH for null/undefined', () => {
    expect(formatCost(null)).toBe(EM_DASH)
    expect(formatCost(undefined)).toBe(EM_DASH)
  })

  it('returns EM_DASH for NaN', () => {
    expect(formatCost(Number.NaN)).toBe(EM_DASH)
  })
})

describe('formatTokens', () => {
  it('renders raw numbers below 1000 unchanged', () => {
    expect(formatTokens(999)).toBe('999')
  })

  it('renders K suffix', () => {
    expect(formatTokens(1234)).toBe('1.2K')
  })

  it('renders M suffix', () => {
    expect(formatTokens(1_200_000)).toBe('1.2M')
    expect(formatTokens(1_204_331)).toBe('1.2M')
  })

  it('trims a trailing .0', () => {
    expect(formatTokens(2_000_000)).toBe('2M')
  })

  it('returns EM_DASH for null/undefined', () => {
    expect(formatTokens(null)).toBe(EM_DASH)
    expect(formatTokens(undefined)).toBe(EM_DASH)
  })
})

describe('formatCount', () => {
  it('groups thousands', () => {
    expect(formatCount(12345)).toBe('12,345')
  })

  it('returns EM_DASH for null', () => {
    expect(formatCount(null)).toBe(EM_DASH)
  })
})

describe('formatDuration', () => {
  it('renders sub-second durations in ms', () => {
    expect(formatDuration(450)).toBe('450ms')
  })

  it('renders sub-minute durations with one decimal of seconds', () => {
    expect(formatDuration(4500)).toBe('4.5s')
  })

  it('renders sub-hour durations as Xm Ys', () => {
    expect(formatDuration(1_773_488)).toBe('29m 33s')
  })

  it('renders sub-day durations as Xh MMm with zero-padded minutes', () => {
    expect(formatDuration(2 * 3_600_000 + 4 * 60_000)).toBe('2h 04m')
  })

  it('renders multi-day durations as Xd HHh', () => {
    expect(formatDuration(3 * 86_400_000 + 4 * 3_600_000)).toBe('3d 04h')
  })

  it('returns EM_DASH for null/undefined/negative', () => {
    expect(formatDuration(null)).toBe(EM_DASH)
    expect(formatDuration(undefined)).toBe(EM_DASH)
    expect(formatDuration(-1)).toBe(EM_DASH)
  })
})

describe('formatRelativeTime', () => {
  const now = new Date('2026-08-17T12:00:00.000Z')

  it('renders "3 minutes ago"', () => {
    expect(formatRelativeTime('2026-08-17T11:57:00.000Z', now)).toBe('3 minutes ago')
  })

  it('renders EM_DASH for a null timestamp (partial session)', () => {
    expect(formatRelativeTime(null, now)).toBe(EM_DASH)
    expect(formatRelativeTime(undefined, now)).toBe(EM_DASH)
  })

  it('renders EM_DASH for a real partial session\'s started_at (SPEC §1.7), not Invalid Date', () => {
    expect(partialSessionSummary.started_at).toBeNull()
    expect(formatRelativeTime(partialSessionSummary.started_at, now)).toBe(EM_DASH)
  })

  it('returns EM_DASH instead of Invalid Date for an unparseable string', () => {
    expect(formatRelativeTime('not-a-date', now)).toBe(EM_DASH)
  })
})

describe('formatRelativeOffset', () => {
  // Round-5: the origin is the *first event in the loaded timeline*, not
  // `session.started_at` (round-4's anchor produced multi-day offsets — see
  // format.ts's module doc). The function itself is anchor-agnostic; this
  // var is just named for what the caller now passes.
  const firstEventTs = '2026-08-17T12:00:00.000Z'

  const cases: { name: string; iso: string | null | undefined; origin: string | null | undefined; expected: string }[] = [
    { name: 'zero offset for the first event itself (the exact critic example)', iso: firstEventTs, origin: firstEventTs, expected: '+0.0s' },
    { name: 'sub-10s offset (the exact critic example)', iso: '2026-08-17T12:00:03.200Z', origin: firstEventTs, expected: '+3.2s' },
    { name: 'sub-minute offset always shows one decimal', iso: '2026-08-17T12:00:59.900Z', origin: firstEventTs, expected: '+59.9s' },
    { name: 'minutes+seconds offset (the exact critic example)', iso: '2026-08-17T12:02:14.000Z', origin: firstEventTs, expected: '+2m 14s' },
    { name: 'hours+minutes offset drops seconds (the exact critic example)', iso: '2026-08-17T13:02:00.000Z', origin: firstEventTs, expected: '+1h 02m' },
    { name: 'hours+minutes offset with a non-zero seconds remainder still drops seconds', iso: '2026-08-17T14:04:33.000Z', origin: firstEventTs, expected: '+2h 04m' },
    { name: 'day-scale offset drops to hours, same coarsening as formatDuration', iso: '2026-08-28T13:06:48.000Z', origin: firstEventTs, expected: '+11d 01h' },
    { name: 'negative offset (event before the anchor, e.g. clock skew) gets a minus sign', iso: '2026-08-17T11:59:58.000Z', origin: firstEventTs, expected: '-2.0s' },
    { name: 'null event timestamp -> EM_DASH', iso: null, origin: firstEventTs, expected: EM_DASH },
    { name: 'undefined event timestamp -> EM_DASH', iso: undefined, origin: firstEventTs, expected: EM_DASH },
    { name: 'null origin (unknown anchor) -> EM_DASH, never a fabricated +0s', iso: '2026-08-17T12:00:04.500Z', origin: null, expected: EM_DASH },
    { name: 'unparseable event timestamp -> EM_DASH', iso: 'not-a-date', origin: firstEventTs, expected: EM_DASH },
    { name: 'unparseable origin -> EM_DASH', iso: '2026-08-17T12:00:04.500Z', origin: 'not-a-date', expected: EM_DASH },
  ]

  for (const { name, iso, origin, expected } of cases) {
    it(name, () => {
      expect(formatRelativeOffset(iso, origin)).toBe(expected)
    })
  }
})

describe('formatAbsoluteTime', () => {
  it('formats a valid ISO timestamp', () => {
    expect(formatAbsoluteTime('2026-08-17T12:00:00.000Z')).not.toBe(EM_DASH)
  })

  it('returns EM_DASH for null/invalid', () => {
    expect(formatAbsoluteTime(null)).toBe(EM_DASH)
    expect(formatAbsoluteTime('not-a-date')).toBe(EM_DASH)
  })
})

describe('formatPercent', () => {
  it('renders a fraction as a rounded percent', () => {
    expect(formatPercent(0.0412)).toBe('4.1%')
  })

  it('returns EM_DASH for null', () => {
    expect(formatPercent(null)).toBe(EM_DASH)
  })
})
