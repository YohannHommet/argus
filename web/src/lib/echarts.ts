/**
 * Single tree-shaken ECharts registration point (SPEC §6.1). Every chart
 * component imports `VChart`/`useChartResize` from here rather than
 * `vue-echarts`/`echarts/core` directly, so this module's side-effecting
 * `use()` call is guaranteed to run exactly once before any chart mounts,
 * and no second call with a different renderer/chart-type set can silently
 * expand the bundle (verified via `pnpm build`'s reported chunk size — see
 * ticket P4-07's report).
 *
 * Registers exactly: CanvasRenderer, LineChart, BarChart, PieChart,
 * HeatmapChart, GridComponent, TooltipComponent, LegendComponent,
 * DataZoomComponent, VisualMapComponent — the set the four analytics
 * components need (line/bar for TimeSeriesChart, bar/pie for
 * BreakdownChart, heatmap+visualMap for DecisionMatrix) and nothing else.
 */
import { useResizeObserver } from '@vueuse/core'
import type { MaybeComputedElementRef } from '@vueuse/core'
import { BarChart, HeatmapChart, LineChart, PieChart } from 'echarts/charts'
import {
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  VisualMapComponent,
} from 'echarts/components'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import type { Ref } from 'vue'
import VChart from 'vue-echarts'

import { formatCost, formatCount, formatDuration, formatTokens } from '@/lib/format'

use([
  CanvasRenderer,
  LineChart,
  BarChart,
  PieChart,
  HeatmapChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  VisualMapComponent,
])

export { VChart }
export type { EChartsCoreOption } from 'echarts/core'

/** Minimal surface `useChartResize` needs off a mounted `vue-echarts` instance. */
export interface ResizableChart {
  resize: () => void
}

/**
 * Wires a chart's `resize()` call to its container's `ResizeObserver`
 * rather than relying on vue-echarts's own `autoresize` prop, so the AC
 * ("charts resize with the container") is directly assertable in a mount
 * test: stub `global.ResizeObserver`, invoke the captured callback, assert
 * the exposed `resize` spy was called — no reliance on the library's
 * internal wiring, which a stubbed `VChart` component would bypass anyway.
 */
export function useChartResize(
  container: MaybeComputedElementRef,
  chart: Ref<ResizableChart | null | undefined>,
) {
  return useResizeObserver(container, () => {
    chart.value?.resize()
  })
}

/**
 * Selects which `lib/format.ts` formatter renders a chart's axis labels
 * and tooltip values. Shared between {@link TimeSeriesChart.vue} and
 * {@link BreakdownChart.vue} so the same `metric` name always formats the
 * same way in both — e.g. a `dimension=query_source` breakdown's `value`
 * is always a cost figure regardless of the `metric=` query param it was
 * fetched with (SPEC §4.3), so its host passes `metric="cost"` explicitly.
 */
export type ChartMetricKind = 'cost' | 'tokens' | 'count' | 'duration'

export function formatterForMetric(metric: ChartMetricKind): (value: number | null | undefined) => string {
  switch (metric) {
    case 'cost':
      return formatCost
    case 'tokens':
      return formatTokens
    case 'duration':
      return formatDuration
    case 'count':
      return formatCount
  }
}
