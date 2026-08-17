<script setup lang="ts">
import { computed } from 'vue'

import { Badge } from '@/components/ui/badge'
import NullValue from '@/components/common/NullValue.vue'
import RawValue from '@/components/common/RawValue.vue'
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
}

const props = withDefaults(defineProps<Props>(), { layout: 'row' })

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
      class="p-2 align-middle"
    >
      <StatusDot :status="session.status" />
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="truncate p-2 align-middle"
      :title="session.project"
    >
      {{ session.project }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="p-2 align-middle"
    >
      <RawValue
        :value="session.vendor"
        kind="vendor"
      />
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="text-muted-foreground p-2 align-middle"
    >
      {{ formatRelativeTime(session.started_at) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="text-muted-foreground p-2 align-middle"
    >
      {{ formatRelativeTime(session.last_event_at) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="p-2 align-middle font-mono"
    >
      {{ formatDuration(session.duration_ms) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="p-2 align-middle font-mono"
    >
      {{ formatCount(session.turn_count) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="p-2 align-middle font-mono"
    >
      {{ formatCount(session.event_count) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="p-2 align-middle font-mono"
    >
      {{ formatCount(session.tool_call_count) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="p-2 align-middle"
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
          variant="outline"
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
      class="p-2 align-middle font-mono"
    >
      {{ formatTokens(totalTokens) }}
    </component>
    <component
      :is="cellTag"
      role="cell"
      class="text-cost p-2 align-middle font-mono"
    >
      {{ formatCost(session.cost.usd) }}
    </component>
  </component>
</template>
