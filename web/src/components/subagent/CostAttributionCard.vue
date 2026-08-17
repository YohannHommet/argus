<script setup lang="ts">
/**
 * Renders `SubagentTree.cost_attribution` (PLAN P4-05). SPEC §1.9 is
 * explicit that `by_query_source`'s vocabulary is real but uninterpreted —
 * Argus does not map it onto a "subagent vs main" semantic — so this card
 * shows the raw keys as a sorted table rather than inventing labels for
 * them, and leads with the "other query sources: $X of $Y" framing SPEC
 * §1.9 calls "the honest form of the claim" instead of a per-agent cost
 * number the telemetry cannot support.
 */
import { computed } from 'vue'

import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { formatCost } from '@/lib/format'

type SubagentCostAttribution = components['schemas']['SubagentCostAttribution']

interface Props {
  data?: SubagentCostAttribution | null
  loading?: boolean
  error?: ApiError | Error | null
}

const props = withDefaults(defineProps<Props>(), {
  data: null,
  loading: false,
  error: null,
})

const emit = defineEmits<{ retry: [] }>()

const isEmpty = computed(() => !props.loading && !props.error && props.data === null)

/** Raw `by_query_source` keys, sorted by cost descending — no special-casing of any particular key (SPEC §1.9/§4.4). */
const rows = computed(() => {
  if (!props.data) return []
  return Object.entries(props.data.by_query_source).sort(([, a], [, b]) => b - a)
})

const totalCostUsd = computed(() => rows.value.reduce((sum, [, value]) => sum + value, 0))
</script>

<template>
  <Card data-testid="cost-attribution-card">
    <CardHeader>
      <CardTitle>Cost attribution by query source</CardTitle>
      <CardDescription>
        Whole-session-lifetime figures (not windowed) — the real
        <code>query_source</code> vocabulary observed on this deployment, passed
        through verbatim.
      </CardDescription>
    </CardHeader>
    <CardContent class="flex flex-col gap-4">
      <ErrorState
        v-if="error"
        :error="error"
        title="Couldn't load cost attribution"
        @retry="emit('retry')"
      />

      <div
        v-else-if="loading"
        class="flex flex-col gap-2"
        data-testid="cost-attribution-loading"
      >
        <Skeleton class="h-6 w-full" />
        <Skeleton class="h-6 w-full" />
        <Skeleton class="h-6 w-full" />
      </div>

      <EmptyState
        v-else-if="isEmpty"
        title="No cost attribution available"
        description="This session recorded no api_request cost."
      />

      <template v-else-if="data">
        <p
          class="text-muted-foreground text-sm"
          data-testid="cost-attribution-other-share"
        >
          Dominant query source
          <RawValue
            :value="data.dominant_query_source"
            kind="query_source"
          /> — other query sources: <span class="text-cost font-medium">{{ formatCost(data.other_query_source_usd) }}</span> of
          <span class="text-cost font-medium">{{ formatCost(totalCostUsd) }}</span>.
        </p>

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Query source</TableHead>
              <TableHead class="text-right">
                Cost
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow
              v-for="[key, value] in rows"
              :key="key"
              data-testid="cost-attribution-row"
            >
              <TableCell>
                <RawValue
                  :value="key"
                  kind="query_source"
                />
              </TableCell>
              <TableCell class="text-cost text-right">
                {{ formatCost(value) }}
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>

        <p
          v-if="data.note"
          class="text-muted-foreground text-xs italic"
          data-testid="cost-attribution-note"
        >
          {{ data.note }}
        </p>

        <p
          v-if="!data.per_node_available"
          class="text-muted-foreground text-xs"
        >
          Per-node cost is not available for this session — costs above are attributed by query source only, not by individual subagent.
        </p>
      </template>
    </CardContent>
  </Card>
</template>
