<script setup lang="ts">
import { computed } from 'vue'
import { useVirtualList } from '@vueuse/core'

import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { ApiError } from '@/api/errors'
import { computeCostThresholds } from '@/lib/severity'
import type { SessionSummary, SortKey } from '@/stores/sessions'
import SessionRow from './SessionRow.vue'
import { SESSION_ROW_GRID_COLS, VIRTUALIZATION_THRESHOLD } from './sessionRowGrid'

const VIRTUAL_ROW_HEIGHT = 44

interface Column {
  key: string
  label: string
  /** Present only on the 4 columns the API can actually sort by (SPEC §4.1) — clicking any other
   * header does nothing, by design (there is no client-side sort to fall back to). */
  sort?: SortKey
  class?: string
}

// Magnitude columns (SPEC gap: "numeric columns should be right-aligned with tabular figures so
// magnitudes compare down a column") get `text-right` on both header and cell so the digits stack.
// Reject % is deliberately excluded — it renders as a badge/chip, not a bare number.
const NUMERIC_COLUMN_CLASS = 'text-right'

const COLUMNS: Column[] = [
  { key: 'status', label: 'Status' },
  { key: 'project', label: 'Project' },
  { key: 'vendor', label: 'Vendor' },
  { key: 'started', label: 'Started', sort: 'started_at' },
  { key: 'last_event', label: 'Last event', sort: 'last_event_at' },
  { key: 'duration', label: 'Duration', class: NUMERIC_COLUMN_CLASS },
  { key: 'turns', label: 'Turns', class: NUMERIC_COLUMN_CLASS },
  { key: 'events', label: 'Events', sort: 'event_count', class: NUMERIC_COLUMN_CLASS },
  { key: 'tools', label: 'Tools', class: NUMERIC_COLUMN_CLASS },
  { key: 'reject_rate', label: 'Reject %' },
  { key: 'tokens', label: 'Tokens', class: NUMERIC_COLUMN_CLASS },
  { key: 'cost', label: 'Cost', sort: 'cost_usd', class: NUMERIC_COLUMN_CLASS },
]

const costThresholds = computed(() => computeCostThresholds(props.sessions.map((s) => s.cost.usd)))

interface Props {
  sessions: SessionSummary[]
  sort: SortKey
  loading?: boolean
  loadingMore?: boolean
  error?: ApiError | Error | null
  hasMore?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  loadingMore: false,
  error: null,
  hasMore: false,
})

const emit = defineEmits<{
  retry: []
  loadMore: []
  selectSession: [id: string]
  sort: [key: SortKey]
}>()

const isVirtualized = computed(() => props.sessions.length > VIRTUALIZATION_THRESHOLD)

const { list, containerProps, wrapperProps } = useVirtualList(
  computed(() => props.sessions),
  { itemHeight: VIRTUAL_ROW_HEIGHT },
)

function onHeaderClick(column: Column): void {
  if (column.sort) emit('sort', column.sort)
}
</script>

<template>
  <div
    class="flex flex-col gap-3"
    data-testid="session-table"
  >
    <ErrorState
      v-if="error"
      :error="error"
      @retry="emit('retry')"
    />

    <template v-else-if="loading">
      <div class="flex flex-col gap-2">
        <Skeleton
          v-for="i in 8"
          :key="i"
          class="h-10 w-full"
        />
      </div>
    </template>

    <EmptyState
      v-else-if="sessions.length === 0"
      title="No sessions match these filters"
      description="Try widening the date range or clearing a filter."
    />

    <template v-else-if="!isVirtualized">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead
              v-for="column in COLUMNS"
              :key="column.key"
              :class="[column.sort ? 'cursor-pointer select-none' : undefined, column.class]"
              @click="onHeaderClick(column)"
            >
              {{ column.label }}
              <span
                v-if="column.sort && sort === column.sort"
                aria-hidden="true"
              >&darr;</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <SessionRow
            v-for="session in sessions"
            :key="session.id"
            :session="session"
            :cost-thresholds="costThresholds"
            @activate="emit('selectSession', session.id)"
          />
        </TableBody>
      </Table>
    </template>

    <!--
      Virtualized path (>200 rows, PLAN P4-02's AC): `useVirtualList` absolutely-positions each
      rendered item inside its wrapper, which a real `<table>`/`<tbody>` cannot host without breaking
      row layout — so this is a deliberate, documented deviation from "always a real table": a
      div-based CSS grid that mirrors the table header's columns 1:1 via SessionRow's own
      SESSION_ROW_GRID_COLS constant. The ≤200-row path above stays a real semantic `<table>`.
    -->
    <template v-else>
      <div
        role="table"
        data-testid="session-virtual-table"
      >
        <div
          role="row"
          class="border-border grid items-center border-b"
          :class="SESSION_ROW_GRID_COLS"
        >
          <div
            v-for="column in COLUMNS"
            :key="column.key"
            role="columnheader"
            class="text-foreground h-10 px-2 text-left align-middle text-sm font-medium"
            :class="[column.sort ? 'cursor-pointer select-none' : undefined, column.class]"
            @click="onHeaderClick(column)"
          >
            {{ column.label }}
            <span
              v-if="column.sort && sort === column.sort"
              aria-hidden="true"
            >&darr;</span>
          </div>
        </div>
        <div
          v-bind="containerProps"
          class="h-[600px] overflow-y-auto"
        >
          <div v-bind="wrapperProps">
            <SessionRow
              v-for="row in list"
              :key="row.data.id"
              :session="row.data"
              layout="grid"
              :cost-thresholds="costThresholds"
              @activate="emit('selectSession', row.data.id)"
            />
          </div>
        </div>
      </div>
    </template>

    <div
      v-if="!loading && !error && hasMore"
      class="flex justify-center py-2"
    >
      <Button
        variant="outline"
        :disabled="loadingMore"
        data-testid="load-more"
        @click="emit('loadMore')"
      >
        {{ loadingMore ? 'Loading…' : 'Load more' }}
      </Button>
    </div>
  </div>
</template>
