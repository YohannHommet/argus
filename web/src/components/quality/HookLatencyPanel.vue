<script setup lang="ts">
/**
 * `/api/v1/quality/hook-latency`: per-`hook_event` execution counts and
 * p50/p95/p99 latency. This measures the overhead Argus's OWN hook
 * scripts add to a Claude Code session on every tool call/turn — not a
 * property of Claude Code itself — which is why the copy below says so
 * rather than presenting it as a generic "hook stats" table.
 *
 * A real Phase-3 bug (docs/review/phase-3-deviations.md) conflated this
 * endpoint's `executions` (a count) with the *session-detail* endpoint's
 * `hook_latency.by_hook_event`, which maps each hook event to its own
 * p50 latency, not a count. This panel only ever reads this endpoint's
 * own `executions`/`p50_ms`/`p95_ms`/`p99_ms` fields — never mirrors that
 * other shape's semantics.
 */
import { ApiError } from '@/api/errors'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatCount, formatDuration } from '@/lib/format'
import type { HookLatencyRow } from '@/stores/quality'

interface Props {
  rows: HookLatencyRow[]
  loading?: boolean
  error?: ApiError | Error | null
  /** `/meta`'s `hooks_seen`, via `useMetaStore()` — only used to make the empty state's explanation accurate for an OTLP-only deployment. */
  hooksSeen?: boolean
}

withDefaults(defineProps<Props>(), {
  loading: false,
  error: null,
  hooksSeen: false,
})

const emit = defineEmits<{ retry: [] }>()
</script>

<template>
  <div
    class="flex flex-col gap-3"
    data-testid="hook-latency-panel"
  >
    <p
      class="text-muted-foreground text-xs"
      data-testid="hook-latency-panel-explanation"
    >
      Latency Argus's own hook scripts add to each Claude Code tool call/turn, per
      <code>hook_event</code> — not a property of Claude Code itself. A p95/p99 that runs well
      above p50 means occasional slow hook executions; check the hook script for slow network
      calls or disk I/O if these numbers creep up.
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
      title="No hooks have reported yet"
      :description="
        hooksSeen
          ? 'Argus has seen hook payloads before, but none carry a hook.execution_end in this window.'
          : 'Normal for an OTLP-only deployment (/meta reports hooks_seen: false) — this panel fills in once the Claude Code hook script starts firing.'
      "
    />

    <Table v-else>
      <TableHeader>
        <TableRow>
          <TableHead>hook_event</TableHead>
          <TableHead>executions</TableHead>
          <TableHead>p50</TableHead>
          <TableHead>p95</TableHead>
          <TableHead>p99</TableHead>
          <TableHead>errors</TableHead>
          <TableHead>cancelled</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow
          v-for="row in rows"
          :key="row.hook_event"
        >
          <TableCell class="font-mono text-xs">
            <RawValue
              :value="row.hook_event"
              kind="hook_event"
            />
          </TableCell>
          <TableCell data-testid="hook-latency-executions">
            {{ formatCount(row.executions) }}
          </TableCell>
          <TableCell>{{ formatDuration(row.p50_ms) }}</TableCell>
          <TableCell>{{ formatDuration(row.p95_ms) }}</TableCell>
          <TableCell>{{ formatDuration(row.p99_ms) }}</TableCell>
          <TableCell>{{ formatCount(row.errors) }}</TableCell>
          <TableCell>{{ formatCount(row.cancelled) }}</TableCell>
        </TableRow>
      </TableBody>
    </Table>
  </div>
</template>
