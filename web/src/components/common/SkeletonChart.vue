<script setup lang="ts">
/**
 * A pending chart placeholder: one block sized to the `h-64` every chart
 * component on this codebase already reserves for itself
 * (`TimeSeriesChart.vue`, `BreakdownChart.vue`, `DecisionMatrix.vue`), so a
 * hosting view that wants its own loading branch above a chart-shaped slot
 * gets the same footprint without duplicating the raw `Skeleton` + class.
 */
import { Skeleton } from '@/components/ui/skeleton'

interface Props {
  heightClass?: string
}

withDefaults(defineProps<Props>(), { heightClass: 'h-64' })
</script>

<template>
  <Skeleton
    data-testid="skeleton-chart"
    class="w-full"
    :class="heightClass"
  />
</template>
