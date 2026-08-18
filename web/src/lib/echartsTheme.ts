/**
 * Builds the ECharts option fragment (`backgroundColor`, `textStyle`,
 * palette, semantic colors) from `theme.css`'s resolved CSS custom
 * properties, so charts and the rest of the UI can never drift apart
 * (SPEC §6.1). `useChartTheme` re-derives it whenever `uiStore.theme`
 * changes; charts spread the result into their `option` computed so the
 * AC ("toggling the theme changes backgroundColor/textStyle.color in the
 * regenerated option") is literally what a mount test reads back.
 */
import { computed, type ComputedRef } from 'vue'

import { useUiStore } from '@/stores/ui'

export interface ChartTheme {
  backgroundColor: string
  textStyle: { color: string }
  /** Border/axis-line color — theme.css's `--border`. */
  borderColor: string
  /** Secondary text (axis labels, legend) — theme.css's `--muted-foreground`. */
  mutedColor: string
  /** The product's one accent hue — theme.css's `--primary`. Used for heatmap intensity, emphasis states, anything that isn't a multi-series categorical palette. */
  primary: string
  /** Categorical series palette, `--chart-1`..`--chart-5` in order. Cycle it — see {@link paletteColor}. */
  categoricalPalette: [string, string, string, string, string]
  accept: string
  reject: string
  cost: string
  warn: string
  unknown: string
}

/**
 * jsdom's `getComputedStyle` returns `''` for a custom property it never
 * resolved (no stylesheet cascade applied) — and a real browser returns
 * `''` too for a token that's simply missing from `theme.css`. Either way,
 * a silent empty string would make the whole theme black-on-black rather
 * than fail loudly, so every token falls back to its documented `:root`
 * (dark) value from `theme.css` when unresolved. These are fallbacks, not
 * a second source of truth — `theme.css` is still the only place the
 * palette is *decided*.
 */
const FALLBACKS: Record<string, string> = {
  '--background': 'oklch(0.145 0 0)',
  '--foreground': 'oklch(0.985 0 0)',
  '--muted-foreground': 'oklch(0.708 0 0)',
  '--border': 'oklch(1 0 0 / 10%)',
  '--primary': 'oklch(0.65 0.19 258)',
  '--chart-1': 'oklch(0.65 0.19 258)',
  '--chart-2': 'oklch(0.75 0.14 200)',
  '--chart-3': 'oklch(0.78 0.15 85)',
  '--chart-4': 'oklch(0.68 0.18 305)',
  '--chart-5': 'oklch(0.7 0.17 350)',
  '--accept': 'oklch(0.72 0.17 155)',
  '--reject': 'oklch(0.704 0.191 22.216)',
  /** --cost's documented fallback is --foreground's own fallback: cost is neutral text, never a second hue (gap #1). */
  '--cost': 'oklch(0.985 0 0)',
  '--warn': 'oklch(0.78 0.15 85)',
  '--unknown': 'oklch(0.708 0 0)',
}

/** Reads one resolved custom property off `<html>`, falling back per {@link FALLBACKS}. */
function readToken(name: string): string {
  const resolved = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  if (resolved) return resolved
  const fallback = FALLBACKS[name]
  if (fallback === undefined) {
    throw new Error(`echartsTheme: unresolved CSS custom property "${name}" has no documented fallback`)
  }
  return fallback
}

/** Builds a {@link ChartTheme} from the CSS custom properties currently resolved on `<html>`. */
export function buildChartTheme(): ChartTheme {
  return {
    backgroundColor: readToken('--background'),
    textStyle: { color: readToken('--foreground') },
    borderColor: readToken('--border'),
    mutedColor: readToken('--muted-foreground'),
    primary: readToken('--primary'),
    categoricalPalette: [
      readToken('--chart-1'),
      readToken('--chart-2'),
      readToken('--chart-3'),
      readToken('--chart-4'),
      readToken('--chart-5'),
    ],
    accept: readToken('--accept'),
    reject: readToken('--reject'),
    cost: readToken('--cost'),
    warn: readToken('--warn'),
    unknown: readToken('--unknown'),
  }
}

/**
 * Reactive theme: recomputed whenever `uiStore.theme` changes. Safe to call
 * synchronously right after `uiStore.setTheme(...)` — the store's watcher
 * applies `<html class="dark|light">` with `flush: 'sync'`, so
 * `getComputedStyle` already sees the new values by the time this
 * `computed` re-evaluates.
 */
export function useChartTheme(): ComputedRef<ChartTheme> {
  const ui = useUiStore()
  return computed(() => {
    // Read (not just call) ui.theme so this computed actually depends on
    // it — the real palette comes from getComputedStyle, but that call
    // alone isn't reactive, so without this the theme would never refresh.
    void ui.theme
    return buildChartTheme()
  })
}

/**
 * Cycles the 5-color categorical palette by index. `limit_series` (SPEC
 * §4.3) allows up to 8 named series plus an `other` bucket, well past the
 * 5-color palette, so series 6+ intentionally repeat colors 1-5 rather
 * than inventing new hex values.
 */
export function paletteColor(theme: ChartTheme, index: number): string {
  return theme.categoricalPalette[index % theme.categoricalPalette.length]
}

/**
 * Appends an alpha channel to a token's resolved `oklch(L C H)` string —
 * `theme.css` never stores alpha on `--primary`/`--chart-*` (only `--border`
 * and `--input` bake one in), so this is how chart chrome gets a translucent
 * fill (dataZoom's selected range, hover halos) without a second token per
 * color. No-ops (returns the input unchanged) on anything not already
 * `oklch(...)`, so passing an already-alpha'd token like `--border` through
 * here can never double up the alpha component.
 */
export function withAlpha(oklchColor: string, percent: number): string {
  if (!/^oklch\([^)]*\)$/.test(oklchColor)) return oklchColor
  return oklchColor.replace(/\)$/, ` / ${percent}%)`)
}

/**
 * Shared legend chrome (gap #3, "chart chrome"): a small flat swatch and
 * muted, theme-matched label instead of vue-echarts/ECharts's own default
 * legend styling, which otherwise clashes with the rest of the UI's type
 * scale. Every chart with a legend (`TimeSeriesChart`, `BreakdownChart`'s
 * pie variant) spreads this in rather than hand-rolling its own.
 */
export function chartLegend(t: ChartTheme) {
  return {
    top: 0,
    icon: 'roundRect' as const,
    itemWidth: 10,
    itemHeight: 10,
    itemGap: 20,
    textStyle: { color: t.mutedColor, fontSize: 12 },
  }
}

/**
 * Shared dataZoom chrome (gap #3): a slim, low-contrast slider rather than
 * ECharts's default heavy gray scrollbar-with-handles-and-shadow, which
 * reads as a stray UI widget rather than part of the chart. The inside
 * (scroll/drag) zoom is kept for interaction; only the slider's paint is
 * restyled.
 */
export function slimDataZoom(t: ChartTheme) {
  return [
    { type: 'inside' as const },
    {
      type: 'slider' as const,
      height: 6,
      bottom: 2,
      brushSelect: false,
      showDetail: false,
      showDataShadow: false,
      borderColor: 'transparent',
      backgroundColor: 'transparent',
      fillerColor: withAlpha(t.primary, 18),
      handleSize: 10,
      handleStyle: { color: t.primary, borderColor: t.primary, opacity: 0.9 },
      moveHandleStyle: { color: t.primary, opacity: 0.6 },
      dataBackground: { lineStyle: { color: t.borderColor }, areaStyle: { color: t.borderColor } },
      selectedDataBackground: { lineStyle: { color: t.primary }, areaStyle: { color: withAlpha(t.primary, 12) } },
    },
  ]
}
