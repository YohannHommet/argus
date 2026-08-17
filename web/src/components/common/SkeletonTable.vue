<script setup lang="ts">
/**
 * A pending-table placeholder: `rows` shimmer bars stacked the same way
 * every data table on this codebase already renders its own loading state
 * (`SessionTable.vue`, `ToolCallTable.vue`) — pulled out here so a hosting
 * view that owns its own loading branch (rather than delegating to one of
 * those tables) has the same look for free instead of re-inventing it.
 */
import { Skeleton } from '@/components/ui/skeleton'

interface Props {
  rows?: number
}

withDefaults(defineProps<Props>(), { rows: 8 })
</script>

<template>
  <div
    class="flex flex-col gap-2"
    data-testid="skeleton-table"
  >
    <Skeleton
      v-for="i in rows"
      :key="i"
      class="h-10 w-full"
    />
  </div>
</template>
