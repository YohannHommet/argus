import { effectScope } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useUiStore } from '@/stores/ui'
import { buildChartTheme, metricColor, metricPolarity, paletteColor, useChartTheme, withAlpha, type MetricKey } from './echartsTheme'

describe('buildChartTheme', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('falls back to the documented dark values when getComputedStyle resolves nothing (jsdom has no CSS cascade for custom properties)', () => {
    // jsdom never applies theme.css's stylesheet, so every custom property
    // read here returns '' regardless of the <html> class — this is the
    // exact path the AC calls out as needing an explicit test, not just a
    // happy path that only ever sees real values.
    const theme = buildChartTheme()
    expect(theme.backgroundColor).toBe('oklch(0.145 0 0)')
    expect(theme.textStyle.color).toBe('oklch(0.985 0 0)')
    expect(theme.primary).toBe('oklch(0.65 0.19 258)')
    expect(theme.categoricalPalette).toEqual([
      'oklch(0.65 0.19 258)',
      'oklch(0.75 0.14 200)',
      'oklch(0.78 0.15 85)',
      'oklch(0.68 0.18 305)',
      'oklch(0.7 0.17 350)',
    ])
    expect(theme.accept).toBe('oklch(0.72 0.17 155)')
    expect(theme.reject).toBe('oklch(0.704 0.191 22.216)')
    // cost is neutral foreground text, never a second (red) hue — gap #1.
    expect(theme.cost).toBe('oklch(0.985 0 0)')
    expect(theme.warn).toBe('oklch(0.78 0.15 85)')
    expect(theme.unknown).toBe('oklch(0.708 0 0)')
  })

  it('reads a resolved custom property instead of the fallback when one is actually set', () => {
    document.documentElement.style.setProperty('--background', 'oklch(0.5 0 0)')
    const theme = buildChartTheme()
    expect(theme.backgroundColor).toBe('oklch(0.5 0 0)')
    document.documentElement.style.removeProperty('--background')
  })
})

describe('paletteColor', () => {
  it('cycles the 5-color palette for indices beyond 5', () => {
    const theme = buildChartTheme()
    expect(paletteColor(theme, 0)).toBe(theme.categoricalPalette[0])
    expect(paletteColor(theme, 5)).toBe(theme.categoricalPalette[0])
    expect(paletteColor(theme, 6)).toBe(theme.categoricalPalette[1])
  })
})

describe('withAlpha', () => {
  it('appends an alpha channel to a token with none', () => {
    const theme = buildChartTheme()
    expect(withAlpha(theme.primary, 18)).toBe(`${theme.primary.slice(0, -1)} / 18%)`)
  })

  it('replaces (never doubles up) an alpha channel already present on the token', () => {
    // --border's own fallback already bakes in "/ 10%" — round-3 fix: a
    // naive append would produce the invalid "oklch(1 0 0 / 10% / 6%)".
    const replaced = withAlpha('oklch(1 0 0 / 10%)', 6)
    expect(replaced).toBe('oklch(1 0 0 / 6%)')
    expect(replaced.match(/%/g)).toHaveLength(1)
  })

  it('no-ops on a value that is not an oklch(...) string', () => {
    expect(withAlpha('transparent', 50)).toBe('transparent')
  })
})

/**
 * Round-3 UI pass gap: "sparklines, deltas, and chart series are all
 * rendered in one undifferentiated blue/gray ... assign semantic color".
 * Table-driven per the fix's own instruction — every {@link MetricKey} the
 * Analytics screen shows gets one row asserting its hue class and polarity
 * together, so a future metric added to `METRIC_SEMANTICS` without an entry
 * here is undocumented, not silently defaulted.
 */
const METRIC_TABLE: { key: MetricKey; expectHue: 'neutral' | 'destructive' | 'secondary' }[] = [
  { key: 'cost', expectHue: 'neutral' },
  { key: 'tokens', expectHue: 'neutral' },
  { key: 'api_requests', expectHue: 'neutral' },
  { key: 'api_errors', expectHue: 'destructive' },
  { key: 'tool_rejects', expectHue: 'destructive' },
  { key: 'reject_rate', expectHue: 'destructive' },
  { key: 'tool_calls', expectHue: 'secondary' },
  { key: 'sessions', expectHue: 'secondary' },
  { key: 'turns', expectHue: 'secondary' },
  { key: 'loc_added', expectHue: 'secondary' },
  { key: 'loc_removed', expectHue: 'secondary' },
  { key: 'active_time', expectHue: 'secondary' },
]

describe('metricColor', () => {
  const theme = buildChartTheme()

  it.each(METRIC_TABLE)('resolves $key to its $expectHue hue', ({ key, expectHue }) => {
    const color = metricColor(theme, key)
    if (expectHue === 'neutral') expect(color).toBe(theme.primary)
    if (expectHue === 'destructive') expect(color).toBe(theme.reject)
    if (expectHue === 'secondary') expect(color).toBe(theme.categoricalPalette[1])
  })
})

describe('metricPolarity', () => {
  it.each([
    { key: 'cost' as const, expected: 'neutral' as const },
    { key: 'tokens' as const, expected: 'neutral' as const },
    { key: 'api_requests' as const, expected: 'neutral' as const },
    { key: 'tool_calls' as const, expected: 'neutral' as const },
    { key: 'sessions' as const, expected: 'neutral' as const },
    { key: 'turns' as const, expected: 'neutral' as const },
    { key: 'loc_added' as const, expected: 'neutral' as const },
    { key: 'loc_removed' as const, expected: 'neutral' as const },
    { key: 'active_time' as const, expected: 'neutral' as const },
    { key: 'api_errors' as const, expected: 'destructive' as const },
    { key: 'tool_rejects' as const, expected: 'destructive' as const },
    { key: 'reject_rate' as const, expected: 'destructive' as const },
  ])('$key is $expected', ({ key, expected }) => {
    expect(metricPolarity(key)).toBe(expected)
  })
})

describe('useChartTheme', () => {
  it('regenerates backgroundColor/textStyle.color when uiStore.theme changes', () => {
    const scope = effectScope()
    scope.run(() => {
      const ui = useUiStore()
      const theme = useChartTheme()

      // document.documentElement.style overrides simulate what theme.css's
      // .light block would resolve to in a real browser — jsdom itself
      // never applies the stylesheet (see buildChartTheme's fallback test).
      document.documentElement.style.setProperty('--background', 'oklch(0.145 0 0)')
      document.documentElement.style.setProperty('--foreground', 'oklch(0.985 0 0)')
      const dark = { backgroundColor: theme.value.backgroundColor, textColor: theme.value.textStyle.color }

      ui.setTheme('light')
      document.documentElement.style.setProperty('--background', 'oklch(1 0 0)')
      document.documentElement.style.setProperty('--foreground', 'oklch(0.145 0 0)')
      const light = { backgroundColor: theme.value.backgroundColor, textColor: theme.value.textStyle.color }

      expect(light.backgroundColor).not.toBe(dark.backgroundColor)
      expect(light.textColor).not.toBe(dark.textColor)
      expect(light.backgroundColor).toBe('oklch(1 0 0)')
      expect(light.textColor).toBe('oklch(0.145 0 0)')

      document.documentElement.style.removeProperty('--background')
      document.documentElement.style.removeProperty('--foreground')
    })
    scope.stop()
  })
})
