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
  /**
   * `theme.css`'s `--card` — every analytics chart lives inside a `Card`
   * (`bg-card`), one step lighter than the page's own `--background`
   * (round-5 UI pass, gap: "the inner plot panel sits on a darker box
   * than its card creating an unintended seam"). A chart's own
   * `backgroundColor` should read this, not `backgroundColor` above,
   * whenever it's painting the inside of a `Card` — which is every chart
   * this product has today.
   */
  cardBackgroundColor: string
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
  '--card': 'oklch(0.205 0 0)',
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
    cardBackgroundColor: readToken('--card'),
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
 * Sets a token's resolved `oklch(L C H [/ A%])` string to a new alpha
 * channel — `theme.css` never stores alpha on `--primary`/`--chart-*` (only
 * `--border` and `--input` bake one in), so this is how chart chrome gets a
 * translucent fill (dataZoom's selected range, hover halos, the round-3
 * lighter grid lines below) without a second token per color. No-ops
 * (returns the input unchanged) on anything not `oklch(...)`. Replaces
 * (rather than appends) any alpha component already present, so passing an
 * already-alpha'd token like `--border` through here can never double up
 * the alpha into an invalid `oklch(... / 10% / 6%)`.
 */
export function withAlpha(oklchColor: string, percent: number): string {
  const match = /^oklch\(([^)]*)\)$/.exec(oklchColor)
  if (!match) return oklchColor
  const withoutExistingAlpha = match[1].replace(/\s*\/\s*[\d.]+%\s*$/, '')
  return `oklch(${withoutExistingAlpha} / ${percent}%)`
}

/**
 * Semantic identity of a KPI/series metric, independent of `ChartMetricKind`
 * (`lib/echarts.ts`'s `cost`/`tokens`/`count`/`duration`, which only selects
 * a *formatter* and says nothing about whether more is good or bad). Every
 * metric the Analytics screen shows a sparkline/delta/series for maps to
 * exactly one entry in {@link METRIC_SEMANTICS} — the single table that
 * decides hue + polarity (round-3 UI pass gap: "sparklines, deltas, and
 * chart series are all rendered in one undifferentiated blue/gray").
 */
export type MetricKey =
  | 'cost'
  | 'tokens'
  | 'api_requests'
  | 'api_errors'
  | 'tool_rejects'
  | 'tool_calls'
  | 'sessions'
  | 'turns'
  | 'loc_added'
  | 'loc_removed'
  | 'active_time'
  | 'reject_rate'

/** Which of the theme's three semantic colors a metric's sparkline/series uses. */
type MetricHue = 'neutral' | 'destructive' | 'secondary'

/**
 * Whether a *rising* value is bad news for this metric. `'destructive'`
 * (errors, rejects, reject rate) means up reads as a warning and down as an
 * improvement. `'neutral'` (cost, tokens, requests, and the other count-ish
 * KPIs) means more isn't failure — spending more or running more
 * sessions/turns/tool-calls is simply more, so its delta is tinted with the
 * accent color going up and muted going down, never red/green.
 */
export type MetricPolarity = 'neutral' | 'destructive'

const METRIC_SEMANTICS: Record<MetricKey, { hue: MetricHue; polarity: MetricPolarity }> = {
  cost: { hue: 'neutral', polarity: 'neutral' },
  tokens: { hue: 'neutral', polarity: 'neutral' },
  api_requests: { hue: 'neutral', polarity: 'neutral' },
  api_errors: { hue: 'destructive', polarity: 'destructive' },
  tool_rejects: { hue: 'destructive', polarity: 'destructive' },
  tool_calls: { hue: 'secondary', polarity: 'neutral' },
  sessions: { hue: 'secondary', polarity: 'neutral' },
  turns: { hue: 'secondary', polarity: 'neutral' },
  loc_added: { hue: 'secondary', polarity: 'neutral' },
  loc_removed: { hue: 'secondary', polarity: 'neutral' },
  active_time: { hue: 'secondary', polarity: 'neutral' },
  reject_rate: { hue: 'destructive', polarity: 'destructive' },
}

/** This metric's polarity — see {@link MetricPolarity}. Exposed standalone so hosts can direction-tint a delta without reaching into the hue mapping. */
export function metricPolarity(key: MetricKey): MetricPolarity {
  return METRIC_SEMANTICS[key].polarity
}

/**
 * Resolves a metric's semantic hue to an actual theme color: `'neutral'` ->
 * `t.primary` (the same hue cost/tokens/requests use everywhere else on the
 * screen), `'destructive'` -> `t.reject`, `'secondary'` -> the categorical
 * palette's second color (`--chart-2`) — a fixed, muted hue distinct from
 * both, never cycled per-series the way `paletteColor` cycles a
 * multi-series chart's palette.
 */
export function metricColor(t: ChartTheme, key: MetricKey): string {
  const { hue } = METRIC_SEMANTICS[key]
  if (hue === 'neutral') return t.primary
  if (hue === 'destructive') return t.reject
  return t.categoricalPalette[1]
}

/**
 * Tints a delta by direction *and* the metric's own polarity (gap:
 * "deltas ... rendered in one undifferentiated blue/gray" +
 * "encode polarity, don't moralize neutral metrics"). A zero delta is
 * always muted — there's no direction to tint. For a `'destructive'` metric
 * rising is bad (`t.reject`) and falling is good (`t.accept`); for a
 * `'neutral'` metric rising gets the accent color and falling is simply
 * muted, never red/green.
 */
export function deltaColor(t: ChartTheme, key: MetricKey, delta: number | null | undefined): string {
  if (delta === null || delta === undefined || delta === 0) return t.mutedColor
  const { polarity } = METRIC_SEMANTICS[key]
  if (polarity === 'destructive') return delta > 0 ? t.reject : t.accept
  return delta > 0 ? t.primary : t.mutedColor
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
