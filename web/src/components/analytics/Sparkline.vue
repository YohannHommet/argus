<script setup lang="ts">
/**
 * Inline per-tile trend line for the KPI strip (round-5 UI pass: tiles were
 * "flat, equal-weight numbers with no comparison or trend context"). Plain
 * SVG rather than a `vue-echarts` instance — twelve tiles is twelve chart
 * instances' worth of canvas/resize-observer overhead for a decoration a
 * few pixels tall, and a `<path>` is trivial to assert against in a mount
 * test without mocking a chart library.
 *
 * Colors still come from `useChartTheme` (SPEC §6.1: charts and the rest
 * of the UI must never drift), but a sparkline's hue is picked by
 * `metricColor(theme, metricKey)` (never `paletteColor`'s categorical
 * cycle) — round-3 UI pass gap: "sparklines ... are all rendered in one
 * undifferentiated blue/gray". `metricKey` says *which* KPI this tile
 * trends (cost/tokens/requests neutral-primary, errors/rejects
 * destructive, the rest a muted secondary hue — see `echartsTheme.ts`'s
 * `METRIC_SEMANTICS` table), so the same hue means the same thing in every
 * tile it appears in, unlike a multi-series chart's palette index, which
 * means a different model/project/vendor depending on which panel it's in.
 */
import { computed } from 'vue'

import { metricColor, useChartTheme, withAlpha, type MetricKey } from '@/lib/echartsTheme'

interface Props {
  /** One value per bucket, current window only. `null`/empty renders nothing. */
  values?: number[] | null
  /** Which KPI this sparkline trends — selects the semantic hue via `metricColor`. Defaults to `'cost'` (the neutral/primary hue) for callers that don't care to differentiate. */
  metricKey?: MetricKey
}

const props = withDefaults(defineProps<Props>(), { values: null, metricKey: 'cost' })

const theme = useChartTheme()
const color = computed(() => metricColor(theme.value, props.metricKey))

const viewBox = { width: 100, height: 28 }
const pad = 2

const points = computed<{ x: number; y: number }[]>(() => {
  const v = props.values
  if (!v || v.length === 0) return []
  if (v.length === 1) return [{ x: viewBox.width / 2, y: viewBox.height / 2 }]

  const min = Math.min(...v)
  const max = Math.max(...v)
  const range = max - min
  const usableWidth = viewBox.width - pad * 2
  const usableHeight = viewBox.height - pad * 2

  return v.map((value, index) => {
    const x = pad + (index / (v.length - 1)) * usableWidth
    // A flat (range === 0) series draws as a flat mid-height line — not
    // collapsed to the bottom, which would misread as "all zero".
    const normalized = range === 0 ? 0.5 : (value - min) / range
    const y = pad + (1 - normalized) * usableHeight
    return { x, y }
  })
})

const linePath = computed(() => {
  const pts = points.value
  if (pts.length === 0) return ''
  return pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(2)},${p.y.toFixed(2)}`).join(' ')
})

const areaPath = computed(() => {
  const pts = points.value
  if (pts.length === 0) return ''
  const first = pts[0]
  const last = pts[pts.length - 1]
  return `${linePath.value} L${last.x.toFixed(2)},${viewBox.height - pad} L${first.x.toFixed(2)},${viewBox.height - pad} Z`
})

const endpoint = computed(() => points.value[points.value.length - 1] ?? null)

const hasData = computed(() => points.value.length > 1)
</script>

<template>
  <svg
    v-if="hasData"
    :viewBox="`0 0 ${viewBox.width} ${viewBox.height}`"
    class="h-7 w-full"
    preserveAspectRatio="none"
    data-testid="sparkline"
    aria-hidden="true"
  >
    <path
      :d="areaPath"
      :fill="withAlpha(color, 15)"
      stroke="none"
    />
    <path
      :d="linePath"
      fill="none"
      :stroke="color"
      stroke-width="1.5"
      vector-effect="non-scaling-stroke"
    />
    <circle
      v-if="endpoint"
      :cx="endpoint.x"
      :cy="endpoint.y"
      r="2"
      :fill="color"
    />
  </svg>
</template>
