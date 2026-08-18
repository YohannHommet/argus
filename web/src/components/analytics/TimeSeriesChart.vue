<script setup lang="ts">
/**
 * Renders `GET /analytics/timeseries` (SPEC §4.3) as a multi-series line
 * chart. Buckets are dense/zero-filled server-side — no gap handling here.
 * A series whose `key` is `""` (events with no model attributed) is
 * labelled "unattributed" for the legend/tooltip; `data.other` (series
 * beyond `limit_series`, folded by total desc) renders as its own
 * "Other" series rather than being dropped.
 */
import { computed, ref } from 'vue'
import type { ComposeOption } from 'echarts/core'
import type { LineSeriesOption } from 'echarts/charts'
import type {
  DataZoomComponentOption,
  GridComponentOption,
  LegendComponentOption,
  TooltipComponentOption,
} from 'echarts/components'

import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { formatterForMetric, useChartResize, VChart, type ChartMetricKind, type ResizableChart } from '@/lib/echarts'
import { chartLegend, paletteColor, slimDataZoom, useChartTheme } from '@/lib/echartsTheme'
import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { Skeleton } from '@/components/ui/skeleton'

type TimeSeriesOption = ComposeOption<
  LineSeriesOption | GridComponentOption | TooltipComponentOption | LegendComponentOption | DataZoomComponentOption
>

interface Props {
  data?: components['schemas']['Series'] | null
  loading?: boolean
  error?: ApiError | Error | null
  metric?: ChartMetricKind
}

const props = withDefaults(defineProps<Props>(), {
  data: null,
  loading: false,
  error: null,
  metric: 'count',
})

const emit = defineEmits<{ retry: [] }>()

const theme = useChartTheme()

const containerRef = ref<HTMLElement | null>(null)
const chartRef = ref<ResizableChart | null>(null)
useChartResize(containerRef, chartRef)

const valueFormatter = computed(() => formatterForMetric(props.metric))

/** `''` (unattributed) gets a visible label; every other key is the vendor's own name, verbatim. */
function seriesLabel(key: string): string {
  return key === '' ? 'unattributed' : key
}

const isEmpty = computed(() => {
  const d = props.data
  return !d || (d.series.length === 0 && !d.other)
})

function axisLabels(d: components['schemas']['Series']): string[] {
  const opts: Intl.DateTimeFormatOptions =
    d.bucket === 'hour' ? { hour: '2-digit', minute: '2-digit' } : { month: 'short', day: 'numeric' }
  return d.buckets.map((iso) => new Intl.DateTimeFormat('en-US', opts).format(new Date(iso)))
}

const option = computed<TimeSeriesOption>(() => {
  const d = props.data
  const t = theme.value
  if (!d) {
    return { backgroundColor: t.backgroundColor, textStyle: t.textStyle, series: [] }
  }

  const series: LineSeriesOption[] = d.series.map((point, index) => ({
    type: 'line',
    name: seriesLabel(point.key),
    data: point.values,
    showSymbol: false,
    smooth: 0.2,
    lineStyle: { color: paletteColor(t, index), width: 2 },
    itemStyle: { color: paletteColor(t, index) },
    emphasis: { focus: 'series', lineStyle: { width: 3 } },
  }))

  if (d.other) {
    series.push({
      type: 'line',
      name: 'Other',
      data: d.other.values,
      showSymbol: false,
      lineStyle: { color: t.mutedColor, type: 'dashed', width: 2 },
      itemStyle: { color: t.mutedColor },
      emphasis: { focus: 'series', lineStyle: { width: 3 } },
    })
  }

  return {
    backgroundColor: t.backgroundColor,
    textStyle: t.textStyle,
    grid: { left: 56, right: 16, top: 40, bottom: 40 },
    tooltip: {
      trigger: 'axis',
      valueFormatter: (value) => valueFormatter.value(typeof value === 'number' ? value : Number(value)),
    },
    legend: chartLegend(t),
    dataZoom: slimDataZoom(t),
    xAxis: {
      type: 'category',
      data: axisLabels(d),
      axisLine: { lineStyle: { color: t.borderColor } },
      axisTick: { show: false },
      axisLabel: { color: t.mutedColor, fontSize: 11 },
    },
    yAxis: {
      type: 'value',
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: t.mutedColor, fontSize: 11, formatter: (value: number) => valueFormatter.value(value) },
      splitLine: { lineStyle: { color: t.borderColor, type: 'dashed' } },
    },
    series,
  }
})
</script>

<template>
  <ErrorState
    v-if="error"
    :error="error"
    @retry="emit('retry')"
  />
  <Skeleton
    v-else-if="loading"
    class="h-64 w-full"
  />
  <EmptyState
    v-else-if="isEmpty"
    title="No data for this range"
  />
  <div
    v-else
    ref="containerRef"
    class="h-64 w-full"
  >
    <VChart
      ref="chartRef"
      class="h-full w-full"
      :option="option"
      :autoresize="false"
    />
  </div>
</template>
