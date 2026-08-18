<script setup lang="ts">
/**
 * Renders `GET /analytics/decisions` (SPEC §1.9/§4.3) as a heatmap: tools
 * on the y-axis, `decision_source` on the x-axis. Both vocabularies are
 * discovered at runtime from the response — the x-axis is the **union**
 * of every row's `by_source` keys, in first-seen order, so a source
 * Argus has never hardcoded (e.g. `an_invented_decision_source`) still
 * gets its own column, and a tool that never uses one source still gets a
 * (zero-valued) cell for it rather than a ragged grid.
 *
 * This is the feature the product exists for: a cell click emits
 * `{ tool_name, decision_source }` so the host (`/analytics`, P4-08) can
 * link to `/tools?decision_source=…` — this component only emits, it does
 * not navigate.
 */
import { computed, ref } from 'vue'
import type { ComposeOption } from 'echarts/core'
import type { HeatmapSeriesOption } from 'echarts/charts'
import type { GridComponentOption, TooltipComponentOption, VisualMapComponentOption } from 'echarts/components'
import type { ECElementEvent } from 'echarts'

import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { useChartResize, VChart, type ResizableChart } from '@/lib/echarts'
import { useChartTheme } from '@/lib/echartsTheme'
import { formatCount, formatDuration, formatPercent } from '@/lib/format'
import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Skeleton } from '@/components/ui/skeleton'

type DecisionMatrixOption = ComposeOption<HeatmapSeriesOption | GridComponentOption | TooltipComponentOption | VisualMapComponentOption>

export interface DecisionMatrixFilter {
  tool_name: string
  decision_source: string
}

interface Props {
  data?: components['schemas']['DecisionMatrix'] | null
  loading?: boolean
  error?: ApiError | Error | null
}

const props = withDefaults(defineProps<Props>(), {
  data: null,
  loading: false,
  error: null,
})

const emit = defineEmits<{
  retry: []
  /** Emitted on a heatmap cell click; the host wires this to a `/tools` filter navigation. */
  filter: [payload: DecisionMatrixFilter]
}>()

const theme = useChartTheme()

const containerRef = ref<HTMLElement | null>(null)
const chartRef = ref<ResizableChart | null>(null)
useChartResize(containerRef, chartRef)

const rows = computed(() => props.data?.rows ?? [])

/** Union of every row's `by_source` keys, first-seen order — never a hardcoded list (SPEC §1.9). */
const columns = computed(() => {
  const seen = new Set<string>()
  for (const row of rows.value) {
    for (const key of Object.keys(row.by_source)) {
      seen.add(key)
    }
  }
  return Array.from(seen)
})

const isEmpty = computed(() => rows.value.length === 0)

const maxCellValue = computed(() => {
  let max = 0
  for (const row of rows.value) {
    for (const value of Object.values(row.by_source)) {
      if (value > max) max = value
    }
  }
  return max
})

const option = computed<DecisionMatrixOption>(() => {
  const t = theme.value
  const cols = columns.value
  const data: [number, number, number][] = []
  rows.value.forEach((row, rowIndex) => {
    cols.forEach((col, colIndex) => {
      data.push([colIndex, rowIndex, row.by_source[col] ?? 0])
    })
  })

  return {
    backgroundColor: t.backgroundColor,
    textStyle: t.textStyle,
    grid: { left: 140, right: 24, top: 16, bottom: 80 },
    tooltip: {
      trigger: 'item',
      formatter: (params) => {
        const p = Array.isArray(params) ? params[0] : params
        const value = p?.value
        if (!Array.isArray(value)) return ''
        const [colIndex, rowIndex, count] = value as [number, number, number]
        const row = rows.value[rowIndex]
        const col = cols[colIndex]
        if (!row || col === undefined) return ''
        return `${row.tool_name} × ${col === '' ? 'unattributed' : col}: ${formatCount(count)}`
      },
    },
    xAxis: {
      type: 'category',
      data: cols.map((c) => (c === '' ? 'unattributed' : c)),
      axisLine: { lineStyle: { color: t.borderColor } },
      axisTick: { show: false },
      axisLabel: { color: t.mutedColor, fontSize: 11, rotate: 45 },
      splitArea: { areaStyle: { color: [t.backgroundColor] } },
    },
    yAxis: {
      type: 'category',
      data: rows.value.map((r) => r.tool_name),
      axisLine: { lineStyle: { color: t.borderColor } },
      axisTick: { show: false },
      axisLabel: { color: t.mutedColor, fontSize: 11 },
      splitArea: { areaStyle: { color: [t.backgroundColor] } },
    },
    visualMap: {
      type: 'continuous',
      min: 0,
      max: Math.max(maxCellValue.value, 1),
      calculable: true,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      inRange: { color: [t.borderColor, t.primary] },
      textStyle: { color: t.mutedColor },
    },
    series: [
      {
        type: 'heatmap',
        data,
        itemStyle: { borderColor: t.backgroundColor, borderWidth: 2 },
        emphasis: { itemStyle: { borderColor: t.primary, borderWidth: 2 } },
        label: {
          show: true,
          color: t.textStyle.color,
          fontSize: 11,
          formatter: (p) => formatCount(Array.isArray(p.value) ? (p.value[2] as number) : null),
        },
      },
    ],
  }
})

function onClick(params: ECElementEvent) {
  const value = params.value
  if (!Array.isArray(value)) return
  const [colIndex, rowIndex] = value as [number, number, number]
  const row = rows.value[rowIndex]
  const col = columns.value[colIndex]
  if (!row || col === undefined) return
  emit('filter', { tool_name: row.tool_name, decision_source: col })
}
</script>

<template>
  <ErrorState
    v-if="error"
    :error="error"
    @retry="emit('retry')"
  />
  <Skeleton
    v-else-if="loading"
    class="h-80 w-full"
  />
  <EmptyState
    v-else-if="isEmpty"
    title="No decisions recorded for this range"
  />
  <div
    v-else
    class="flex flex-col gap-4"
  >
    <div
      ref="containerRef"
      class="h-80 w-full"
    >
      <VChart
        ref="chartRef"
        class="h-full w-full"
        :option="option"
        :autoresize="false"
        @click="onClick"
      />
    </div>

    <!--
      The heatmap's axis labels are canvas text (echarts can't host a Vue
      component there); this legible HTML table is where every
      decision_source actually goes through RawValue, per SPEC §4.4 — and
      it's also where the matrix states its own confidence (`exact_share`)
      and accept/reject totals per row, which the grid alone doesn't show.
    -->
    <table class="text-muted-foreground w-full text-left text-xs">
      <caption class="sr-only">
        Accept/reject totals and correlation confidence per tool
      </caption>
      <thead>
        <tr class="border-border border-b">
          <th
            scope="col"
            class="py-1 pr-4 font-medium"
          >
            Tool
          </th>
          <th
            scope="col"
            class="py-1 pr-4 font-medium"
          >
            Accept
          </th>
          <th
            scope="col"
            class="py-1 pr-4 font-medium"
          >
            Reject
          </th>
          <th
            scope="col"
            class="py-1 pr-4 font-medium"
          >
            Exact match
          </th>
          <th
            scope="col"
            class="py-1 pr-4 font-medium"
          >
            p50 wait
          </th>
          <th
            scope="col"
            class="py-1 font-medium"
          >
            p95 wait
          </th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="row in rows"
          :key="row.tool_name"
          class="border-border/50 border-b"
        >
          <td class="text-foreground py-1 pr-4 font-medium">
            {{ row.tool_name }}
          </td>
          <td class="text-accept py-1 pr-4 tabular-nums">
            {{ formatCount(row.accept) }}
          </td>
          <td class="text-reject py-1 pr-4 tabular-nums">
            {{ formatCount(row.reject) }}
          </td>
          <td class="py-1 pr-4 tabular-nums">
            {{ formatPercent(row.exact_share) }}
          </td>
          <td class="py-1 pr-4 tabular-nums">
            {{ formatDuration(row.p50_wait_ms) }}
          </td>
          <td class="py-1 tabular-nums">
            {{ formatDuration(row.p95_wait_ms) }}
          </td>
        </tr>
      </tbody>
    </table>

    <div
      class="flex flex-wrap gap-x-3 gap-y-1 text-xs"
      aria-label="decision sources"
    >
      <span
        v-for="col in columns"
        :key="col"
        class="inline-flex items-center gap-1"
      >
        <span class="text-muted-foreground">source:</span>
        <RawValue
          :value="col"
          kind="decision_source"
        />
      </span>
    </div>
  </div>
</template>
