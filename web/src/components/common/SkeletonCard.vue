<script setup lang="ts">
/**
 * A pending KPI-tile/card placeholder: a title-width bar over a
 * value-width bar, inside the same `Card` shell `StatTile.vue` renders
 * once its data arrives — so a grid of these matches the grid it is about
 * to become instead of jumping size on load.
 */
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
</script>

<template>
  <Card data-testid="skeleton-card">
    <CardHeader>
      <Skeleton class="h-3 w-20" />
    </CardHeader>
    <CardContent>
      <Skeleton class="h-8 w-16" />
    </CardContent>
  </Card>
</template>
