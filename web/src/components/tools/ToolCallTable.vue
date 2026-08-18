<script lang="ts">
/**
 * Runtime exports (`<script setup>` can only export types, not values — the
 * component's script-setup block below shares this block's scope, so no
 * re-import is needed there).
 */
import type { components } from '@/api/schema'

export type ToolCall = components['schemas']['ToolCall']
export type Correlation = components['schemas']['Correlation']

/** The only two columns with a meaningful total order to sort by — both nullable durations. */
export const SORTABLE_KEYS = ['wait_ms', 'duration_ms'] as const
export type SortableKey = (typeof SORTABLE_KEYS)[number]

/** Pure — nulls sort last regardless of direction (SPEC §6.1: a null is "unknown", never treated as
 * the lowest real value), non-null values descending. Exported for direct unit testing. */
export function sortRows(rows: ToolCall[], key: SortableKey | null | undefined): ToolCall[] {
  if (!key) return rows
  return [...rows].sort((a, b) => {
    const av = a[key]
    const bv = b[key]
    if (av === null && bv === null) return 0
    if (av === null) return 1
    if (bv === null) return -1
    return bv - av
  })
}
</script>

<script setup lang="ts">
/**
 * The decision-provenance drill-down table (SPEC §6.2/§6.3, PLAN P4-06).
 * Purely presentational — rows/loading/error/sort come in as props, sort
 * requests and row clicks go out as emits, no store import — so the exact
 * same component renders both `/tools` (cross-session, `showSession: true`)
 * and the session detail Tools tab (one session's calls, `showSession:
 * false`, no session column).
 *
 * **No server-side sort exists for this data.** `GET /api/v1/tool-calls`
 * and `GET /api/v1/sessions/{id}/tool-calls` (schema.d.ts's
 * `operations['listToolCalls']`/`['listSessionToolCalls']`, generated from
 * `server/api/openapi.yaml`) have no `sort` query parameter — unlike
 * `GET /api/v1/sessions`, which does. Clicking `wait_ms`/`duration_ms`
 * therefore reorders whatever page is *already loaded*, client-side, via
 * `sortRows` below; it never triggers a refetch or invents a `sort` query
 * param the API can't serve (`tools.ts` never sends one). `sort-change` is
 * still emitted so a host can remember the chosen key across a `loadMore`
 * append, but the reordering itself happens here, driven by the `sort`
 * prop — the table stays a controlled component either way.
 */
import { computed } from 'vue'
import { CircleCheck, CircleHelp, Link2, Link2Off, ShieldQuestion, X } from '@lucide/vue'
import type { Component } from 'vue'
import { RouterLink } from 'vue-router'

import type { ApiError } from '@/api/errors'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import NullValue from '@/components/common/NullValue.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableHead, TableHeader, TableRow, TableCell } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { formatAbsoluteTime, formatDuration } from '@/lib/format'
import { NOT_MEASURED } from '@/lib/nullReasons'

interface Props {
  rows: ToolCall[]
  loading?: boolean
  loadingMore?: boolean
  error?: ApiError | Error | null
  hasMore?: boolean
  /** Client-side-only sort key — see file doc comment. `null` = server/keyset order, unmodified. */
  sort?: SortableKey | null
  /** Cross-session views (`/tools`) need a session column linking to `/sessions/:id`; the session
   * detail Tools tab (one session already) does not. */
  showSession?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  loadingMore: false,
  error: null,
  hasMore: false,
  sort: null,
  showSession: false,
})

const emit = defineEmits<{
  retry: []
  loadMore: []
  sortChange: [key: SortableKey]
  rowClick: [row: ToolCall]
}>()

const displayRows = computed(() => sortRows(props.rows, props.sort))

interface ColumnMeta {
  key: SortableKey
  label: string
}
const SORTABLE_COLUMNS: ColumnMeta[] = [
  { key: 'wait_ms', label: 'Wait' },
  { key: 'duration_ms', label: 'Duration' },
]

function onSortClick(key: SortableKey): void {
  emit('sortChange', key)
}

interface CorrelationMeta {
  label: string
  icon: Component
  class: string
  description: string
  /** Only `hook_only` gets an outlined badge treatment — the AC's "distinct visual, not just a
   * differently-coloured dot": it is the one case where the authoritative `tool_decision` fields
   * (decision/decision_source/permission_mode) are absent, so the row's provenance is weakest. */
  emphasize: boolean
}

/** Argus-computed, closed union (`components['schemas']['Correlation']`) — safe to switch on
 * exhaustively, unlike the vendor-free-form fields below, but a fallback branch stays anyway
 * (`correlationMeta`'s default) in case a future server ships a value this build predates. */
const CORRELATION_META: Record<Correlation, CorrelationMeta> = {
  exact: {
    label: 'Exact',
    icon: CircleCheck,
    class: 'text-accept',
    description: 'Hook and OTel data were joined on a shared tool_use_id — the strongest provenance.',
    emphasize: false,
  },
  otel_only: {
    label: 'OTel only',
    icon: Link2,
    class: 'text-muted-foreground',
    description: 'Seen only via OpenTelemetry; no hook event was available to correlate against.',
    emphasize: false,
  },
  heuristic: {
    label: 'Heuristic',
    icon: ShieldQuestion,
    class: 'text-warn',
    description: 'Correlated by best-effort matching (timing/order), not an exact tool_use_id join.',
    emphasize: false,
  },
  hook_only: {
    label: 'Hook only',
    icon: Link2Off,
    class: 'text-warn',
    description:
      'Seen only via the hook payload — no OTel tool.execution span matched. The authoritative tool_decision fields this row shows (decision, decision_source, permission_mode) are the hook’s own report, unconfirmed by OTel.',
    emphasize: true,
  },
}

const FALLBACK_CORRELATION_META: CorrelationMeta = {
  label: 'Unknown',
  icon: CircleHelp,
  class: 'text-unknown',
  description: 'Unrecognised correlation value — rendering it verbatim rather than guessing.',
  emphasize: true,
}

function correlationMeta(value: Correlation): CorrelationMeta {
  return CORRELATION_META[value] ?? FALLBACK_CORRELATION_META
}

function decisionClass(decision: string | null): string {
  if (decision === 'accept') return 'text-accept'
  if (decision === 'reject') return 'text-reject'
  return ''
}

function onRowClick(row: ToolCall): void {
  emit('rowClick', row)
}
</script>

<template>
  <div
    class="flex flex-col gap-3"
    data-testid="tool-call-table"
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
      v-else-if="rows.length === 0"
      title="No tool calls match these filters"
      description="Try widening the date range or clearing a filter."
    />

    <template v-else>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Tool</TableHead>
            <TableHead v-if="showSession">
              Session
            </TableHead>
            <TableHead>Decision</TableHead>
            <TableHead
              v-for="column in SORTABLE_COLUMNS"
              :key="column.key"
              class="cursor-pointer select-none"
              :data-testid="`sort-${column.key}`"
              @click="onSortClick(column.key)"
            >
              {{ column.label }}
              <span
                v-if="sort === column.key"
                aria-hidden="true"
              >&darr;</span>
            </TableHead>
            <TableHead>Success</TableHead>
            <TableHead>File</TableHead>
            <TableHead>Correlation</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow
            v-for="row in displayRows"
            :key="row.id"
            class="cursor-pointer"
            data-testid="tool-call-row"
            @click="onRowClick(row)"
          >
            <TableCell>
              <div class="flex flex-col">
                <span
                  class="text-foreground font-medium"
                  data-testid="cell-tool-name"
                >{{ row.tool_name }}</span>
                <span class="text-muted-foreground text-xs">
                  <RawValue
                    :value="row.tool_source"
                    kind="tool_source"
                  />
                </span>
              </div>
            </TableCell>

            <TableCell v-if="showSession">
              <RouterLink
                :to="{ name: 'session-detail', params: { id: row.session_id } }"
                class="text-primary hover:underline"
                data-testid="tool-call-session-link"
                @click.stop
              >
                {{ row.session_id.slice(0, 8) }}
              </RouterLink>
            </TableCell>

            <TableCell>
              <div class="flex flex-col gap-0.5">
                <span
                  class="font-medium"
                  :class="decisionClass(row.decision)"
                >
                  <RawValue
                    :value="row.decision"
                    kind="decision"
                  />
                </span>
                <span class="text-muted-foreground text-xs">
                  <RawValue
                    :value="row.decision_source"
                    kind="decision_source"
                  />
                </span>
              </div>
            </TableCell>

            <TableCell
              data-testid="cell-wait-ms"
              class="tabular-nums"
            >
              <NullValue
                v-if="row.wait_ms === null"
                :reason="NOT_MEASURED"
              />
              <span v-else>{{ formatDuration(row.wait_ms) }}</span>
            </TableCell>

            <TableCell
              data-testid="cell-duration-ms"
              class="tabular-nums"
            >
              <NullValue
                v-if="row.duration_ms === null"
                :reason="NOT_MEASURED"
              />
              <span
                v-else
                :title="formatAbsoluteTime(row.started_at)"
              >{{ formatDuration(row.duration_ms) }}</span>
            </TableCell>

            <TableCell>
              <NullValue
                v-if="row.success === null"
                :reason="NOT_MEASURED"
              />
              <Badge
                v-else-if="row.success"
                variant="outline"
                class="text-accept border-accept/40"
              >
                <CircleCheck class="size-3" />
                ok
              </Badge>
              <Badge
                v-else
                variant="outline"
                class="text-reject border-reject/40"
              >
                <X class="size-3" />
                <RawValue
                  :value="row.error_type"
                  kind="error_type"
                />
              </Badge>
            </TableCell>

            <TableCell class="max-w-56 truncate font-mono text-xs">
              <NullValue
                v-if="row.file_path === null"
                reason="Not applicable to this tool"
              />
              <span
                v-else
                :title="row.file_path"
              >{{ row.file_path }}</span>
            </TableCell>

            <TableCell>
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Badge
                      v-if="correlationMeta(row.correlation).emphasize"
                      variant="outline"
                      :class="[correlationMeta(row.correlation).class, 'border-current']"
                      :data-testid="`correlation-${row.correlation}`"
                      :aria-label="`correlation: ${row.correlation}`"
                    >
                      <component
                        :is="correlationMeta(row.correlation).icon"
                        class="size-3"
                      />
                      {{ correlationMeta(row.correlation).label }}
                    </Badge>
                    <span
                      v-else
                      class="inline-flex items-center gap-1 text-xs"
                      :class="correlationMeta(row.correlation).class"
                      :data-testid="`correlation-${row.correlation}`"
                      :aria-label="`correlation: ${row.correlation}`"
                    >
                      <component
                        :is="correlationMeta(row.correlation).icon"
                        class="size-3"
                      />
                      {{ correlationMeta(row.correlation).label }}
                    </span>
                  </TooltipTrigger>
                  <TooltipContent>{{ correlationMeta(row.correlation).description }}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
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
