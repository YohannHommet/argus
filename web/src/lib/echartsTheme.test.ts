import { effectScope } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { useUiStore } from '@/stores/ui'
import { buildChartTheme, paletteColor, useChartTheme } from './echartsTheme'

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
    expect(theme.categoricalPalette).toEqual([
      'oklch(0.488 0.243 264.376)',
      'oklch(0.696 0.17 162.48)',
      'oklch(0.769 0.188 70.08)',
      'oklch(0.627 0.265 303.9)',
      'oklch(0.645 0.246 16.439)',
    ])
    expect(theme.accept).toBe('oklch(0.696 0.17 162.48)')
    expect(theme.reject).toBe('oklch(0.704 0.191 22.216)')
    expect(theme.cost).toBe('oklch(0.645 0.246 16.439)')
    expect(theme.warn).toBe('oklch(0.769 0.188 70.08)')
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
