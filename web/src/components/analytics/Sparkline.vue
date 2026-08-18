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
 * of the UI must never drift), but a sparkline always uses `t.primary`
 * (never `paletteColor`'s categorical cycle) — its hue means one fixed
 * thing everywhere it appears ("this tile's own trend"), unlike a
 * multi-series chart's palette index, which means a different model/
 * project/vendor depending on which panel it's in.
 */
import { computed } from 'vue'

import { useChartTheme, withAlpha } from '@/lib/echartsTheme'

interface Props {
  /** One value per bucket, current window only. `null`/empty renders nothing. */
  values?: number[] | null
}

const props = withDefaults(defineProps<Props>(), { values: null })

const theme = useChartTheme()

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
      :fill="withAlpha(theme.primary, 15)"
      stroke="none"
    />
    <path
      :d="linePath"
      fill="none"
      :stroke="theme.primary"
      stroke-width="1.5"
      vector-effect="non-scaling-stroke"
    />
    <circle
      v-if="endpoint"
      :cx="endpoint.x"
      :cy="endpoint.y"
      r="2"
      :fill="theme.primary"
    />
  </svg>
</template>
