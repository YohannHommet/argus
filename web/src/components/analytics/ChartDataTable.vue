<script setup lang="ts">
/**
 * An accessible fallback for an ECharts canvas, which has no DOM a screen reader (or
 * axe) can read — a `<details>` disclosure hides a real `<table>` of the same series behind a toggle
 * so the chart's data isn't chart-only, without permanently doubling every panel's vertical space.
 * Shared by `BreakdownChart.vue` and `TimeSeriesChart.vue`: both already compute a formatted
 * label/value per row for the chart itself, so this component takes already-formatted cells rather
 * than raw numbers — it renders exactly what the chart shows, never a second, possibly-diverging
 * formatting pass.
 */
interface Props {
  /** Screen-reader-only table caption — what this table is a data view of. */
  caption: string
  /** The disclosure's visible toggle label. */
  summary: string
  /** Column headers, in order. */
  columns: string[]
  /** One row per data point, cells already formatted for display (same formatter the chart itself used). */
  rows: (string | number)[][]
}

defineProps<Props>()
</script>

<template>
  <details
    class="mt-2 text-xs"
    data-testid="chart-data-table-toggle"
  >
    <summary class="text-muted-foreground hover:text-foreground focus-visible:ring-ring cursor-pointer rounded outline-none select-none focus-visible:ring-2">
      {{ summary }}
    </summary>
    <div class="mt-2 overflow-x-auto">
      <table
        class="w-full text-left text-xs"
        data-testid="chart-data-table"
      >
        <caption class="sr-only">
          {{ caption }}
        </caption>
        <thead>
          <tr class="border-border border-b">
            <th
              v-for="(column, index) in columns"
              :key="index"
              scope="col"
              class="text-muted-foreground py-1 pr-4 font-medium"
            >
              {{ column }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, rowIndex) in rows"
            :key="rowIndex"
            class="border-border/50 border-b last:border-0"
          >
            <td
              v-for="(cell, cellIndex) in row"
              :key="cellIndex"
              class="text-foreground py-1 pr-4 tabular-nums"
            >
              {{ cell }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </details>
</template>
