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
  '--chart-1': 'oklch(0.488 0.243 264.376)',
  '--chart-2': 'oklch(0.696 0.17 162.48)',
  '--chart-3': 'oklch(0.769 0.188 70.08)',
  '--chart-4': 'oklch(0.627 0.265 303.9)',
  '--chart-5': 'oklch(0.645 0.246 16.439)',
  '--accept': 'oklch(0.696 0.17 162.48)',
  '--reject': 'oklch(0.704 0.191 22.216)',
  '--cost': 'oklch(0.645 0.246 16.439)',
  '--warn': 'oklch(0.769 0.188 70.08)',
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
