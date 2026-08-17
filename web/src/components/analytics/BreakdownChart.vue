<script setup lang="ts">
/**
 * Renders `GET /analytics/breakdown` (SPEC §4.3) as a horizontal bar chart
 * (`variant="bar"`, default) or a pie (`variant="pie"`), rows sorted as
 * the API returns them (already ranked by value desc). A `""` key (e.g.
 * `dimension=query_source`'s "no source recorded" bucket) is labelled
 * "unattributed"; any other key — including a vocabulary Argus has never
 * seen, like `a_future_query_source` — renders verbatim, since dimensions
 * such as `decision_source`/`query_source` are unconstrained vendor
 * vocabularies (SPEC §4.4).
 */
import { computed, ref } from 'vue'
import type { ComposeOption } from 'echarts/core'
import type { BarSeriesOption, PieSeriesOption } from 'echarts/charts'
import type { GridComponentOption, LegendComponentOption, TooltipComponentOption } from 'echarts/components'

import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { formatterForMetric, useChartResize, VChart, type ChartMetricKind, type ResizableChart } from '@/lib/echarts'
import { paletteColor, useChartTheme } from '@/lib/echartsTheme'
import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { Skeleton } from '@/components/ui/skeleton'

type BreakdownOption = ComposeOption<
  BarSeriesOption | PieSeriesOption | GridComponentOption | TooltipComponentOption | LegendComponentOption
>

interface Props {
  data?: components['schemas']['Breakdown'] | null
  loading?: boolean
  error?: ApiError | Error | null
  /**
   * `dimension=query_source`'s `value` is always a cost figure regardless
   * of the `metric=` the caller fetched with (SPEC §4.3) — pass
   * `metric="cost"` for that dimension explicitly, this component does not
   * infer it from `data.dimension`.
   */
  metric?: ChartMetricKind
  variant?: 'bar' | 'pie'
}

const props = withDefaults(defineProps<Props>(), {
  data: null,
  loading: false,
  error: null,
  metric: 'count',
  variant: 'bar',
})

const emit = defineEmits<{ retry: [] }>()

const theme = useChartTheme()
const valueFormatter = computed(() => formatterForMetric(props.metric))

const containerRef = ref<HTMLElement | null>(null)
const chartRef = ref<ResizableChart | null>(null)
useChartResize(containerRef, chartRef)

const isEmpty = computed(() => !props.data || props.data.rows.length === 0)

/** `''` (unattributed) gets a visible label; every other key renders verbatim, per SPEC §4.4. */
function rowLabel(key: string): string {
  return key === '' ? 'unattributed' : key
}

const option = computed<BreakdownOption>(() => {
  const d = props.data
  const t = theme.value
  if (!d) {
    return { backgroundColor: t.backgroundColor, textStyle: t.textStyle, series: [] }
  }

  const labels = d.rows.map((row) => rowLabel(row.key))
  const base = {
    backgroundColor: t.backgroundColor,
    textStyle: t.textStyle,
    tooltip: {
      trigger: props.variant === 'pie' ? ('item' as const) : ('axis' as const),
      valueFormatter: (value: unknown) => valueFormatter.value(typeof value === 'number' ? value : Number(value)),
    },
  }

  if (props.variant === 'pie') {
    const series: PieSeriesOption = {
      type: 'pie',
      radius: '70%',
      data: d.rows.map((row, index) => ({
        name: labels[index],
        value: row.value,
        itemStyle: { color: paletteColor(t, index) },
      })),
      label: { color: t.textStyle.color },
    }
    return { ...base, legend: { top: 0, textStyle: { color: t.textStyle.color } }, series: [series] }
  }

  const series: BarSeriesOption = {
    type: 'bar',
    data: d.rows.map((row, index) => ({ value: row.value, itemStyle: { color: paletteColor(t, index) } })),
  }

  return {
    ...base,
    grid: { left: 120, right: 24, top: 16, bottom: 32 },
    xAxis: {
      type: 'value',
      axisLine: { lineStyle: { color: t.borderColor } },
      axisLabel: { color: t.mutedColor, formatter: (value: number) => valueFormatter.value(value) },
      splitLine: { lineStyle: { color: t.borderColor } },
    },
    yAxis: {
      type: 'category',
      data: labels,
      inverse: true,
      axisLine: { lineStyle: { color: t.borderColor } },
      axisLabel: { color: t.mutedColor },
    },
    series: [series],
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
