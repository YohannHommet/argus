import { describe, expect, it } from 'vitest'

import { computeDelta, seriesDelta, seriesTotal, sparklineValues, type Series } from './analyticsDelta'

function series(buckets: string[], points: { key: string; values: number[] }[], other?: number[]): Series {
  return { bucket: 'hour', buckets, series: points, ...(other ? { other: { values: other } } : {}) }
}

describe('seriesTotal', () => {
  const cases: { name: string; input: Series | null | undefined; expected: number | null }[] = [
    { name: 'null series -> null (never 0)', input: null, expected: null },
    { name: 'undefined series -> null', input: undefined, expected: null },
    { name: 'single series, single bucket', input: series(['t0'], [{ key: '', values: [5] }]), expected: 5 },
    {
      name: 'multiple series summed per bucket, then across buckets',
      input: series(['t0', 't1'], [
        { key: 'a', values: [1, 2] },
        { key: 'b', values: [3, 4] },
      ]),
      expected: 10,
    },
    {
      name: '`other` bucket included in the total',
      input: series(['t0', 't1'], [{ key: 'a', values: [1, 1] }], [10, 10]),
      expected: 22,
    },
    {
      name: 'all-zero buckets -> real 0, not null (empty preceding window)',
      input: series(['t0', 't1', 't2'], [{ key: '', values: [0, 0, 0] }]),
      expected: 0,
    },
    { name: 'zero series, no buckets', input: series([], []), expected: 0 },
  ]

  it.each(cases)('$name', ({ input, expected }) => {
    expect(seriesTotal(input)).toBe(expected)
  })
})

describe('computeDelta', () => {
  const cases: { name: string; current: number | null; previous: number | null; expected: number | null }[] = [
    { name: 'both known -> current minus previous', current: 42, previous: 30, expected: 12 },
    { name: 'negative delta (a real decrease)', current: 10, previous: 25, expected: -15 },
    { name: 'current null (not-attributable) -> null', current: null, previous: 30, expected: null },
    { name: 'previous null (preceding window never fetched/skipped) -> null', current: 42, previous: null, expected: null },
    { name: 'both null -> null', current: null, previous: null, expected: null },
    { name: 'empty preceding window (real 0) -> full current value as the delta', current: 42, previous: 0, expected: 42 },
    { name: 'no change -> 0, a real delta, not null', current: 10, previous: 10, expected: 0 },
  ]

  it.each(cases)('$name', ({ current, previous, expected }) => {
    expect(computeDelta(current, previous)).toBe(expected)
  })
})

describe('seriesDelta', () => {
  it('combines seriesTotal on both sides', () => {
    const current = series(['t0', 't1'], [{ key: '', values: [5, 5] }])
    const previous = series(['t0', 't1'], [{ key: '', values: [2, 2] }])
    expect(seriesDelta(current, previous)).toBe(6)
  })

  it('null current (skipped under a model filter) -> null, even with a real previous window', () => {
    const previous = series(['t0'], [{ key: '', values: [4] }])
    expect(seriesDelta(null, previous)).toBeNull()
  })

  it('null previous (preceding window not fetched) -> null, even with a real current window', () => {
    const current = series(['t0'], [{ key: '', values: [4] }])
    expect(seriesDelta(current, null)).toBeNull()
  })

  it('empty preceding window -> delta equals the current total, not null', () => {
    const current = series(['t0', 't1'], [{ key: '', values: [3, 4] }])
    const previous = series(['t0', 't1'], [{ key: '', values: [0, 0] }])
    expect(seriesDelta(current, previous)).toBe(7)
  })
})

describe('sparklineValues', () => {
  const cases: { name: string; input: Series | null | undefined; expected: number[] | null }[] = [
    { name: 'null series -> null', input: null, expected: null },
    { name: 'undefined series -> null', input: undefined, expected: null },
    { name: 'no buckets -> null', input: series([], []), expected: null },
    {
      name: 'sums every named series per bucket index',
      input: series(['t0', 't1', 't2'], [
        { key: 'a', values: [1, 2, 3] },
        { key: 'b', values: [10, 20, 30] },
      ]),
      expected: [11, 22, 33],
    },
    {
      name: '`other` folded into the same per-bucket totals',
      input: series(['t0', 't1'], [{ key: 'a', values: [1, 2] }], [100, 200]),
      expected: [101, 202],
    },
    {
      name: 'all-zero buckets still produce a real (zero) array, not null',
      input: series(['t0', 't1'], [{ key: '', values: [0, 0] }]),
      expected: [0, 0],
    },
  ]

  it.each(cases)('$name', ({ input, expected }) => {
    expect(sparklineValues(input)).toEqual(expected)
  })
})
