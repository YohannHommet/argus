import { describe, expect, it } from 'vitest'

import { durationBarScale, maxDuration, rowDetail } from './timelineDisplay'

describe('rowDetail', () => {
  const cases: { name: string; item: { tool_name: string | null; model: string | null }; expected: string | null }[] = [
    { name: 'tool_name only (tool rows)', item: { tool_name: 'Edit', model: null }, expected: 'Edit' },
    { name: 'model only (LLM rows)', item: { tool_name: null, model: 'claude-sonnet-4-5-20250929' }, expected: 'claude-sonnet-4-5-20250929' },
    { name: 'both present — tool_name wins', item: { tool_name: 'Read', model: 'gpt-4o' }, expected: 'Read' },
    { name: 'neither present (e.g. assistant.message — content is opt-in, never faked)', item: { tool_name: null, model: null }, expected: null },
    { name: 'empty-string tool_name treated as absent', item: { tool_name: '', model: 'claude-haiku-4-5' }, expected: 'claude-haiku-4-5' },
  ]

  for (const { name, item, expected } of cases) {
    it(name, () => {
      expect(rowDetail(item)).toBe(expected)
    })
  }
})

describe('durationBarScale', () => {
  const cases: { name: string; durationMs: number | null | undefined; maxDurationMs: number; expected: number }[] = [
    { name: 'null duration -> 0', durationMs: null, maxDurationMs: 10_000, expected: 0 },
    { name: 'undefined duration -> 0', durationMs: undefined, maxDurationMs: 10_000, expected: 0 },
    { name: 'zero duration -> 0', durationMs: 0, maxDurationMs: 10_000, expected: 0 },
    { name: 'negative duration -> 0 (never a negative bar)', durationMs: -50, maxDurationMs: 10_000, expected: 0 },
    { name: 'non-finite duration -> 0', durationMs: Number.POSITIVE_INFINITY, maxDurationMs: 10_000, expected: 0 },
    { name: 'zero max (nothing to compare against) -> 0', durationMs: 500, maxDurationMs: 0, expected: 0 },
    { name: 'negative max -> 0', durationMs: 500, maxDurationMs: -1, expected: 0 },
    { name: 'duration equal to max -> 100 (log(x)/log(x) done as an exact clamp, not a float artefact)', durationMs: 5000, maxDurationMs: 5000, expected: 100 },
    { name: 'duration above max (stale max) -> clamped to 100', durationMs: 6000, maxDurationMs: 5000, expected: 100 },
    { name: 'sole event: its own duration is the max -> 100', durationMs: 842, maxDurationMs: 842, expected: 100 },
  ]

  for (const { name, durationMs, maxDurationMs, expected } of cases) {
    it(name, () => {
      expect(durationBarScale(durationMs, maxDurationMs)).toBe(expected)
    })
  }

  it('is monotonically increasing in durationMs for a fixed max', () => {
    const max = 60_000
    const small = durationBarScale(100, max)
    const medium = durationBarScale(2_000, max)
    const large = durationBarScale(30_000, max)
    expect(small).toBeLessThan(medium)
    expect(medium).toBeLessThan(large)
  })

  it('compresses large durations relative to a linear scale (log-scale intent)', () => {
    const max = 60_000
    // Linearly, 6000ms would be 10% of max; on a log scale it must read
    // higher than that, since log1p compresses the far end of the range.
    expect(durationBarScale(6_000, max)).toBeGreaterThan(10)
  })

  it('never returns a value outside [0, 100]', () => {
    for (const d of [1, 10, 100, 1_000, 10_000, 1_000_000]) {
      const scale = durationBarScale(d, 5_000)
      expect(scale).toBeGreaterThanOrEqual(0)
      expect(scale).toBeLessThanOrEqual(100)
    }
  })
})

describe('maxDuration', () => {
  it('returns 0 for an empty list', () => {
    expect(maxDuration([])).toBe(0)
  })

  it('returns 0 when no item has a duration', () => {
    expect(maxDuration([{ duration_ms: null }, { duration_ms: null }])).toBe(0)
  })

  it('returns the largest duration across items, ignoring nulls', () => {
    expect(maxDuration([{ duration_ms: 120 }, { duration_ms: null }, { duration_ms: 5_800 }, { duration_ms: 300 }])).toBe(5_800)
  })
})
