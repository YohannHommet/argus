<script setup lang="ts">
/**
 * Renders `SubagentTree.cost_attribution` (PLAN P4-05). SPEC §1.9 is
 * explicit that `by_query_source`'s vocabulary is real but uninterpreted —
 * Argus does not map it onto a "subagent vs main" semantic — so this card
 * shows the raw keys as a sorted table rather than inventing labels for
 * them, and leads with the "other query sources: $X of $Y" framing SPEC
 * §1.9 calls "the honest form of the claim" instead of a per-agent cost
 * number the telemetry cannot support.
 *
 * Round-6 critic gap: this card was rendering three separate disclaimer
 * sentences (a CardDescription paragraph, the server's `note`, and a
 * hand-written "per-node cost is not available" sentence) plus an
 * always-expanded table, together dwarfing the Subagents tree — the tab's
 * actual primary content. It is now collapsed by default (the table is
 * reference material, not the headline) and the disclaimers collapse into
 * one muted summary line: the honest "$X of $Y" framing stays visible
 * (it's the one number-bearing fact worth a glance without expanding
 * anything), the CardDescription's explanation moves into the (?) tooltip
 * next to the title, and the note/per-node-unavailable sentences fold into
 * a single trailing note element with an info-icon tooltip for the fixed
 * per-node caveat.
 */
import { computed, ref } from 'vue'
import { ChevronDown, ChevronRight, Info } from '@lucide/vue'

import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import type { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { formatCost, formatPercent } from '@/lib/format'

type SubagentCostAttribution = components['schemas']['SubagentCostAttribution']

interface Props {
  data?: SubagentCostAttribution | null
  loading?: boolean
  error?: ApiError | Error | null
  /**
   * D-30 (docs/review/phase-4-gauntlet.md, owner-ratified 2026-08-18): the
   * session's `cost.estimated_usd`/`estimated_share` (SPEC §4.3) —
   * deliberately NOT read off `data`, because `by_query_source` is
   * reported-cost-only (SPEC §2.1: "summed reported cost. Uninterpreted"),
   * so an all-estimated session has nothing in it to derive an estimate
   * from. Both default to 0, which reproduces today's rendering exactly:
   * this card's one current caller (SessionDetailView.vue) does not yet
   * pass them through (flagged to the lead as a follow-up wiring gap, since
   * that file is outside this ticket's scope).
   */
  estimatedUsd?: number
  estimatedShare?: number
}

const props = withDefaults(defineProps<Props>(), {
  data: null,
  loading: false,
  error: null,
  estimatedUsd: 0,
  estimatedShare: 0,
})

const emit = defineEmits<{ retry: [] }>()

const isEmpty = computed(() => !props.loading && !props.error && props.data === null)

/** Collapsed by default (round-6 critic gap) — this table is secondary reference material next to the Subagents tree, not the tab's headline. */
const expanded = ref(false)

/** Raw `by_query_source` keys, sorted by cost descending — no special-casing of any particular key (SPEC §1.9/§4.4). */
const rows = computed(() => {
  if (!props.data) return []
  return Object.entries(props.data.by_query_source).sort(([, a], [, b]) => b - a)
})

const totalCostUsd = computed(() => rows.value.reduce((sum, [, value]) => sum + value, 0))

/**
 * D-30: true only when this session's vendor-reported query-source split
 * has NOTHING in it (`rows` empty, i.e. `by_query_source` summed to zero)
 * yet the session actually burned a real, non-zero estimated cost — the
 * exact "$0.00 of $0.00 rendered as though it were measured" defect this
 * ticket fixes. A session with ANY reported cost keeps today's summary/
 * table untouched (SPEC §2.1: by_query_source is reported-only by design,
 * so a partially-estimated session's reported slice is still a true, if
 * incomplete, answer — not a lie worth this card's own fix).
 */
const allEstimatedNoReportedSplit = computed(() => rows.value.length === 0 && props.estimatedUsd > 0)

const estimatedNoticeText = computed(() => {
  if (props.estimatedShare >= 1) {
    return `This session's entire cost (${formatCost(props.estimatedUsd)}) is estimated from Argus's own model_prices table — no api_request event reported a vendor cost.`
  }
  return `This session's cost is estimated from Argus's own model_prices table (${formatCost(props.estimatedUsd)}, ${formatPercent(props.estimatedShare)} of total) — no vendor-reported query_source cost to split.`
})

const PER_QUERY_SOURCE_ESTIMATED_CAVEAT_HINT =
  'The per-query-source split above only ever covers vendor-reported cost (SPEC §2.1) — it has no estimated figures to attribute.'

const PER_NODE_UNAVAILABLE_HINT =
  'Per-node cost is not available for this session — costs above are attributed by query source only, not by individual subagent.'

const DESCRIPTION_HINT =
  'Whole-session-lifetime figures (not windowed) — the real query_source vocabulary observed on this deployment, passed through verbatim.'
</script>

<template>
  <Card data-testid="cost-attribution-card">
    <CardHeader>
      <button
        type="button"
        class="flex w-full items-center justify-between gap-2 text-left"
        data-testid="cost-attribution-toggle"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <span class="flex items-center gap-1.5">
          <CardTitle class="text-sm">
            Cost attribution by query source
          </CardTitle>
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger
                as-child
                @click.stop
              >
                <span
                  class="text-muted-foreground cursor-help"
                  :title="DESCRIPTION_HINT"
                  :aria-label="DESCRIPTION_HINT"
                >
                  <Info class="size-3.5" />
                </span>
              </TooltipTrigger>
              <TooltipContent>{{ DESCRIPTION_HINT }}</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </span>
        <ChevronDown
          v-if="expanded"
          class="text-muted-foreground size-4 shrink-0"
        />
        <ChevronRight
          v-else
          class="text-muted-foreground size-4 shrink-0"
        />
      </button>

      <!--
        This one line is the always-visible summary (round-6 critic gap:
        "collapse the disclaimers into at most one short muted line +
        tooltips") — the honest "$X of $Y" framing plus the server's own
        `note`, with the fixed per-node-unavailable caveat behind an info
        icon rather than its own sentence.
      -->
      <p
        v-if="data && allEstimatedNoReportedSplit"
        class="text-muted-foreground flex flex-wrap items-center gap-1 text-xs"
        data-testid="cost-attribution-estimated-notice"
      >
        <span
          class="border-warn/40 bg-warn/10 text-warn rounded px-1 py-0.5 text-[0.625rem] font-medium tracking-wide uppercase"
          data-testid="cost-attribution-estimated-badge"
        >Estimated</span>
        <span data-testid="cost-attribution-estimated-text">{{ estimatedNoticeText }}</span>
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger as-child>
              <span
                class="cursor-help"
                :title="PER_QUERY_SOURCE_ESTIMATED_CAVEAT_HINT"
                :aria-label="PER_QUERY_SOURCE_ESTIMATED_CAVEAT_HINT"
                data-testid="cost-attribution-estimated-caveat-hint"
              >
                <Info class="size-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent>{{ PER_QUERY_SOURCE_ESTIMATED_CAVEAT_HINT }}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </p>
      <p
        v-else-if="data"
        class="text-muted-foreground flex flex-wrap items-center gap-1 text-xs"
        data-testid="cost-attribution-other-share"
      >
        Dominant query source
        <RawValue
          :value="data.dominant_query_source"
          kind="query_source"
        /> — other query sources: <span class="text-cost font-medium tabular-nums">{{ formatCost(data.other_query_source_usd) }}</span> of
        <span class="text-cost font-medium tabular-nums">{{ formatCost(totalCostUsd) }}</span>.
        <span
          v-if="data.note"
          data-testid="cost-attribution-note"
          class="italic"
        >{{ data.note }}</span>
        <TooltipProvider v-if="!data.per_node_available">
          <Tooltip>
            <TooltipTrigger as-child>
              <span
                class="cursor-help"
                :title="PER_NODE_UNAVAILABLE_HINT"
                :aria-label="PER_NODE_UNAVAILABLE_HINT"
                data-testid="cost-attribution-per-node-hint"
              >
                <Info class="size-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent>{{ PER_NODE_UNAVAILABLE_HINT }}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </p>
    </CardHeader>
    <CardContent
      v-if="error || loading || isEmpty || data"
      class="flex flex-col gap-4"
    >
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

      <!--
        Secondary reference material next to the tree (this tab's primary
        content) — capped and scrollable so a long `by_query_source`
        vocabulary can't push the tree out of view, and collapsed (`v-show`,
        not `v-if`: stays in the DOM so this remains directly testable
        without simulating a click on every test) by default so it never
        dwarfs the tree — see `expanded` above.
      -->
      <div
        v-else-if="data"
        v-show="expanded"
        class="max-h-48 overflow-auto"
        data-testid="cost-attribution-table-wrap"
      >
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
              <TableCell class="text-cost text-right tabular-nums">
                {{ formatCost(value) }}
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </CardContent>
  </Card>
</template>
