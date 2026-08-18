<script setup lang="ts">
import { computed } from 'vue'

import { Badge } from '@/components/ui/badge'
import NullValue from '@/components/common/NullValue.vue'
import RawValue from '@/components/common/RawValue.vue'
import LiveDot from './LiveDot.vue'
import StatusDot from './StatusDot.vue'
import { NO_HOOK_COVERAGE } from '@/lib/nullReasons'
import {
  formatCost,
  formatCount,
  formatDuration,
  formatRejectRate,
  formatRelativeTime,
  formatTokens,
} from '@/lib/format'
import {
  classifyCostSeverity,
  classifyRejectRateSeverity,
  severityTextClass,
} from '@/lib/severity'
import type { CostThresholds } from '@/lib/severity'
import { computeRejectRate } from '@/stores/sessions'
import type { SessionSummary } from '@/stores/sessions'
import { SESSION_ROW_GRID_COLS } from './sessionRowGrid'

interface Props {
  session: SessionSummary
  /**
   * 'row' renders a semantic `<tr>`/`<td>` — the default, real-`<table>` path used for the common
   * (≤200 row) case. 'grid' renders `<div role="row">`/`<div role="cell">` on a CSS grid instead:
   * `useVirtualList` positions each rendered item absolutely, which cannot be done to a `<tr>`
   * without breaking `<table>` layout (a `<tbody>` can't reflow around absolutely-positioned rows),
   * so `SessionTable.vue`'s >200-row virtualized path uses this mode. Documented deviation from a
   * "real table always" ideal — see SessionTable.vue's own note.
   */
  layout?: 'row' | 'grid'
  /**
   * Warn/critical cost cutoffs for the *visible* session set (round-4 UI gap: "give Reject % and
   * Cost graded warn/critical color"). Computed once per table render by `SessionTable.vue`
   * (`computeCostThresholds`) rather than per-row, so every row in a page grades against the same
   * distribution. Defaults to "never" so a row rendered without this prop (e.g. in isolation, in a
   * test) never fabricates an outlier out of a set of one.
   */
  costThresholds?: CostThresholds
}

const props = withDefaults(defineProps<Props>(), {
  layout: 'row',
  costThresholds: () => ({ warn: Infinity, critical: Infinity }),
})

const emit = defineEmits<{ activate: [] }>()

const rootTag = computed(() => (props.layout === 'row' ? 'tr' : 'div'))
const cellTag = computed(() => (props.layout === 'row' ? 'td' : 'div'))
const rootClass = computed(() => [
  'hover:bg-muted/50 focus-visible:ring-ring/50 border-border cursor-pointer border-b outline-none transition-colors focus-visible:ring-2 focus-visible:-outline-offset-2 last:border-0',
  props.layout === 'grid' ? ['grid items-center', SESSION_ROW_GRID_COLS] : [],
])

const totalTokens = computed(() => props.session.tokens.input + props.session.tokens.output)
const rejectRate = computed(() => computeRejectRate(props.session))
const rejectReason = computed(() =>
  props.session.tool_call_count === 0 ? 'No tool calls recorded for this session' : NO_HOOK_COVERAGE,
)

const rejectSeverity = computed(() => classifyRejectRateSeverity(rejectRate.value))
/** `outline` (neutral/warn) vs `destructive` (critical) — the latter is the shared Badge's own subtle bg-destructive/10 tint. */
const rejectBadgeVariant = computed(() => (rejectSeverity.value === 'critical' ? 'destructive' : 'outline'))
const rejectBadgeClass = computed(() =>
  rejectSeverity.value === 'warn' ? 'border-warn/40 bg-warn/10 text-warn' : undefined,
)

const costSeverity = computed(() => classifyCostSeverity(props.session.cost.usd, props.costThresholds))
const costClass = computed(() => ['px-3 py-2 align-middle font-mono text-right', severityTextClass(costSeverity.value) ?? 'text-cost'])

function activate(): void {
  emit('activate')
}
</script>

<template>
  <component
    :is="rootTag"
    role="row"
    tabindex="0"
    data-testid="session-row"
    :class="rootClass"
    @click="activate"
    @keydown.enter="activate"
  >
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 align-middle"
    >
      <span class="flex items-center gap-1.5">
        <StatusDot :status="session.status" />
        <!-- PLAN.md P5-06 / SPEC §6.2: a live badge on `active` sessions — renders nothing otherwise. -->
        <LiveDot :status="session.status" />
      </span>
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="truncate px-3 py-2 align-middle"
      :title="session.project"
    >
      {{ session.project }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 align-middle"
    >
      <RawValue
        :value="session.vendor"
        kind="vendor"
      />
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="text-muted-foreground px-3 py-2 align-middle"
    >
      {{ formatRelativeTime(session.started_at) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="text-muted-foreground px-3 py-2 align-middle"
    >
      {{ formatRelativeTime(session.last_event_at) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 text-right align-middle font-mono"
    >
      {{ formatDuration(session.duration_ms) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 text-right align-middle font-mono"
    >
      {{ formatCount(session.turn_count) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 text-right align-middle font-mono"
    >
      {{ formatCount(session.event_count) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 text-right align-middle font-mono"
    >
      {{ formatCount(session.tool_call_count) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 align-middle"
    >
      <!--
        The testid is on this wrapping span, not forwarded onto Badge/NullValue themselves: Vue's
        automatic attribute fallthrough doesn't reliably cross reka-ui's TooltipProvider/Tooltip/
        TooltipTrigger chain (NullValue's `reason` branch), so a consumer-side wrapper is the robust
        way to give both branches the same stable hook.
      -->
      <span data-testid="reject-rate-badge">
        <Badge
          v-if="rejectRate !== null"
          :variant="rejectBadgeVariant"
          :class="rejectBadgeClass"
          :data-severity="rejectSeverity"
        >
          {{ formatRejectRate(rejectRate) }}
        </Badge>
        <NullValue
          v-else
          :reason="rejectReason"
        />
      </span>
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="px-3 py-2 text-right align-middle font-mono"
    >
      {{ formatTokens(totalTokens) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      :class="costClass"
      :data-severity="costSeverity"
    >
      {{ formatCost(session.cost.usd) }}
    </component>
  </component>
</template>
