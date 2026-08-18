<script setup lang="ts">
/**
 * SPEC §6.2's unmapped-`event_name` inspector: the raw-sample viewer is
 * the whole reason this table exists, so any group's `sample` opens in
 * `JsonViewer` (an AC) — Argus stores it verbatim and this is where an
 * operator sees exactly what a new vendor event looks like before Argus
 * has a mapping for it.
 *
 * On clean demo data this endpoint returns `rows: []` (the sim only emits
 * unmapped events with `--chaos-unknown`), so the empty state is the
 * *default* path — it says something useful rather than a bare "No data".
 */
import { ref } from 'vue'

import { ApiError } from '@/api/errors'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import JsonViewer from '@/components/common/JsonViewer.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatAbsoluteTime, formatCount } from '@/lib/format'
import type { UnknownKindGroup } from '@/stores/quality'

interface Props {
  rows: UnknownKindGroup[]
  loading?: boolean
  error?: ApiError | Error | null
}

withDefaults(defineProps<Props>(), {
  loading: false,
  error: null,
})

const emit = defineEmits<{ retry: [] }>()

// A single controlled dialog reused for whichever row's "View sample"
// button was last clicked, rather than one Dialog per row — mirrors
// EventDetailSheet's controlled-open pattern, scaled down since the
// sample is already in hand (no per-row fetch needed).
const selected = ref<UnknownKindGroup | null>(null)

function openSample(row: UnknownKindGroup): void {
  selected.value = row
}

function onOpenChange(value: boolean): void {
  if (!value) selected.value = null
}
</script>

<template>
  <div
    class="flex flex-col gap-3"
    data-testid="unknown-kind-table"
  >
    <p
      class="text-muted-foreground text-xs"
      data-testid="unknown-kind-table-explanation"
    >
      Event names Argus received in the last 24h that it doesn't recognise — the earliest signal
      that a vendor shipped a new event type. A non-empty table right after a Claude Code upgrade
      is expected and not itself a problem; open a sample below and add a mapping for the new
      <code>event_name</code> so it stops appearing here.
    </p>

    <ErrorState
      v-if="error"
      :error="error"
      @retry="emit('retry')"
    />

    <template v-else-if="loading">
      <Skeleton
        v-for="i in 3"
        :key="i"
        class="h-10 w-full"
      />
    </template>

    <EmptyState
      v-else-if="rows.length === 0"
      title="No unmapped event names in this window"
      description="Argus recognises every event Claude Code has sent in the last 24 hours."
    />

    <Table v-else>
      <TableHeader>
        <TableRow>
          <TableHead>event_name</TableHead>
          <TableHead>source</TableHead>
          <TableHead>count</TableHead>
          <TableHead>first seen</TableHead>
          <TableHead>last seen</TableHead>
          <TableHead>sample</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow
          v-for="row in rows"
          :key="`${row.source}-${row.event_name}`"
        >
          <TableCell class="font-mono text-xs">
            <RawValue
              :value="row.event_name"
              kind="event_name"
            />
          </TableCell>
          <TableCell>
            <RawValue
              :value="row.source"
              kind="source"
            />
          </TableCell>
          <TableCell data-testid="unknown-kind-count">
            {{ formatCount(row.count) }}
          </TableCell>
          <TableCell class="text-muted-foreground text-xs">
            {{ formatAbsoluteTime(row.first_seen) }}
          </TableCell>
          <TableCell class="text-muted-foreground text-xs">
            {{ formatAbsoluteTime(row.last_seen) }}
          </TableCell>
          <TableCell>
            <Button
              variant="outline"
              size="sm"
              data-testid="unknown-kind-view-sample"
              @click="openSample(row)"
            >
              View sample
            </Button>
          </TableCell>
        </TableRow>
      </TableBody>
    </Table>

    <Dialog
      :open="selected !== null"
      @update:open="onOpenChange"
    >
      <DialogContent
        data-testid="unknown-kind-sample-dialog"
        class="sm:max-w-lg"
      >
        <DialogHeader>
          <DialogTitle>
            Raw sample
            <RawValue
              v-if="selected"
              :value="selected.event_name"
              kind="event_name"
            />
          </DialogTitle>
        </DialogHeader>
        <JsonViewer
          v-if="selected"
          :value="selected.sample"
          label="Raw sample"
        />
      </DialogContent>
    </Dialog>
  </div>
</template>
