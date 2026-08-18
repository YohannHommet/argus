<script setup lang="ts">
/**
 * One turn's worth of collapsed timeline items under a sticky header, or —
 * when `promptId` is `null` — the explicit "no turn" group for events with
 * `prompt_id === null` (real data has these, e.g. `hook.registered`;
 * PLAN.md P4-04 AC / Phase-4 exit criterion 3). The header shows per-turn
 * cost/tokens from `turns` (P4-04's `Turn.cost_usd`/token fields) when a
 * matching `Turn` is supplied; the no-turn group has no such aggregate, so
 * it never claims one.
 */
import { computed } from 'vue'
import { ListTree, MessageSquare } from '@lucide/vue'

import type { TimelineItem } from '@/lib/collapseEvents'
import { formatCost, formatTokens } from '@/lib/format'
import type { Turn } from '@/stores/sessionDetail'
import type { Correlation } from './DecisionBadge.vue'
import EventRow from './EventRow.vue'

interface Props {
  promptId: string | null
  turn?: Turn | null
  items: TimelineItem[]
  /** Correlation lookup by tool_use_id, for items whose decision needs a caveat. See Timeline.vue for how it's derived. */
  correlationFor?: (item: TimelineItem) => Correlation | null
}

const props = withDefaults(defineProps<Props>(), {
  turn: null,
  correlationFor: () => null,
})

const emit = defineEmits<{ open: [eventRef: string] }>()

const isNoTurn = computed(() => props.promptId === null)

const totalTokens = computed(() => {
  const t = props.turn
  if (!t) return null
  return t.input_tokens + t.output_tokens
})
</script>

<template>
  <section data-testid="timeline-group">
    <header
      class="bg-background/95 border-border sticky top-0 z-10 flex items-center gap-3 border-b px-3 py-2 backdrop-blur"
      data-testid="timeline-group-header"
    >
      <component
        :is="isNoTurn ? ListTree : MessageSquare"
        class="text-muted-foreground size-4"
        aria-hidden="true"
      />
      <span class="text-foreground text-sm font-semibold">
        {{ isNoTurn ? 'No turn' : `Turn ${turn?.turn_index ?? promptId}` }}
      </span>
      <span
        v-if="isNoTurn"
        class="text-muted-foreground text-xs"
      >Events not attributed to any prompt</span>
      <span
        v-if="turn"
        class="text-muted-foreground ml-auto flex items-center gap-3 text-xs"
      >
        <span class="text-cost tabular-nums">{{ formatCost(turn.cost_usd) }}</span>
        <span>{{ formatTokens(totalTokens) }} tok</span>
      </span>
    </header>

    <EventRow
      v-for="item in items"
      :key="item.key"
      :item="item"
      :correlation="correlationFor(item)"
      @open="emit('open', $event)"
    />
  </section>
</template>
