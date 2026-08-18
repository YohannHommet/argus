import { describe, expect, it } from 'vitest'

import {
  classifyCostSeverity,
  classifyRejectRateSeverity,
  computeCostThresholds,
  severityTextClass,
} from './severity'

describe('classifyRejectRateSeverity', () => {
  it.each([
    [null, 'neutral'],
    [undefined, 'neutral'],
    [NaN, 'neutral'],
    [0, 'neutral'],
    [0.049, 'neutral'],
    [0.05, 'warn'],
    [0.1, 'warn'],
    [0.149, 'warn'],
    [0.15, 'critical'],
    [0.5, 'critical'],
    [1, 'critical'],
  ] as const)('classifies %p as %s', (rate, expected) => {
    expect(classifyRejectRateSeverity(rate)).toBe(expected)
  })
})

describe('computeCostThresholds', () => {
  it('returns Infinity/Infinity for an empty set', () => {
    expect(computeCostThresholds([])).toEqual({ warn: Infinity, critical: Infinity })
  })

  it('returns Infinity/Infinity when every visible cost is identical (no spread to grade)', () => {
    expect(computeCostThresholds([1.2, 1.2, 1.2])).toEqual({ warn: Infinity, critical: Infinity })
  })

  it('returns Infinity/Infinity for a single value', () => {
    expect(computeCostThresholds([3.5])).toEqual({ warn: Infinity, critical: Infinity })
  })

  it('ignores null/undefined/non-finite entries when computing percentiles', () => {
    const withNulls = computeCostThresholds([0.1, null, 0.2, undefined, 0.3, 0.4, NaN])
    const withoutNulls = computeCostThresholds([0.1, 0.2, 0.3, 0.4])
    expect(withNulls).toEqual(withoutNulls)
  })

  it('derives warn at p75 and critical at p90 of the visible non-null costs', () => {
    const costs = [0.14, 0.31, 0.37, 0.55, 0.95, 1.07, 1.17, 1.23, 1.27, 1.71]
    const thresholds = computeCostThresholds(costs)
    expect(thresholds.warn).toBeCloseTo(1.215, 4)
    expect(thresholds.critical).toBeCloseTo(1.314, 4)
  })
})

describe('classifyCostSeverity', () => {
  const thresholds = { warn: 2, critical: 5 }

  it.each([
    [null, 'neutral'],
    [undefined, 'neutral'],
    [NaN, 'neutral'],
    [0, 'neutral'],
    [1.99, 'neutral'],
    [2, 'warn'],
    [3, 'warn'],
    [4.99, 'warn'],
    [5, 'critical'],
    [10, 'critical'],
  ] as const)('classifies %p against {warn:2, critical:5} as %s', (cost, expected) => {
    expect(classifyCostSeverity(cost, thresholds)).toBe(expected)
  })

  it('grades neutral for every value when thresholds are Infinity/Infinity', () => {
    const noSpread = { warn: Infinity, critical: Infinity }
    expect(classifyCostSeverity(1_000_000, noSpread)).toBe('neutral')
  })
})

describe('severityTextClass', () => {
  it.each([
    ['neutral', undefined],
    ['warn', 'text-warn'],
    ['critical', 'text-destructive'],
  ] as const)('maps %s to %s', (severity, expected) => {
    expect(severityTextClass(severity)).toBe(expected)
  })
})
