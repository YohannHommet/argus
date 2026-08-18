<script setup lang="ts">
/**
 * One turn's worth of collapsed timeline items under a sticky header, or —
 * when `promptId` is `null` — the explicit "no turn" group for events with
 * `prompt_id === null` (real data has these, e.g. `hook.registered`;
 * PLAN.md P4-04 AC / Phase-4 exit criterion 3). The header shows per-turn
 * cost/tokens from `turns` (P4-04's `Turn.cost_usd`/token fields) when a
 * matching `Turn` is supplied; the no-turn group has no such aggregate, so
 * it never claims one.
 *
 * Renders as a span-tree node: its events sit indented in a left-bordered
 * rail under the header (the same visual language as `SubagentNode`'s
 * children), so a turn's tool calls, LLM requests and decisions read as
 * *owned by* that turn rather than as flat rows that happen to follow it.
 * Collapsing is purely local display state owned by `Timeline.vue`
 * (`v-model:collapsed` here) — never sent to the server, same rule as the
 * top-level collapse toggle.
 */
import { computed } from 'vue'
import { ChevronDown, ChevronRight, ListTree, MessageSquare } from '@lucide/vue'

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
  /** Local, display-only collapse state — owned by the host (`Timeline.vue`), never sent to the server. */
  collapsed?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  turn: null,
  correlationFor: () => null,
  collapsed: false,
})

const emit = defineEmits<{ open: [eventRef: string]; 'toggle-collapse': [] }>()

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
      class="bg-background/95 border-border sticky top-0 z-10 flex cursor-pointer items-center gap-2 border-b px-3 py-1.5 backdrop-blur"
      data-testid="timeline-group-header"
      role="button"
      tabindex="0"
      :aria-expanded="!collapsed"
      @click="emit('toggle-collapse')"
      @keydown.enter="emit('toggle-collapse')"
    >
      <button
        type="button"
        class="text-muted-foreground hover:text-foreground shrink-0"
        data-testid="timeline-group-toggle"
        :aria-label="collapsed ? 'Expand turn' : 'Collapse turn'"
        @click.stop="emit('toggle-collapse')"
      >
        <ChevronDown
          v-if="!collapsed"
          class="size-3.5"
        />
        <ChevronRight
          v-else
          class="size-3.5"
        />
      </button>
      <component
        :is="isNoTurn ? ListTree : MessageSquare"
        class="text-muted-foreground size-4 shrink-0"
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
        class="text-muted-foreground ml-auto flex items-center gap-3 text-xs"
      >
        <span
          v-if="collapsed"
          data-testid="timeline-group-collapsed-count"
        >{{ items.length }} event{{ items.length === 1 ? '' : 's' }}</span>
        <template v-if="turn">
          <span class="text-cost tabular-nums">{{ formatCost(turn.cost_usd) }}</span>
          <span>{{ formatTokens(totalTokens) }} tok</span>
        </template>
      </span>
    </header>

    <!--
      Span-tree rail: a turn's events sit inside a left-bordered, indented
      container so they read as owned children of the header above — the
      same connector-line idiom `SubagentNode` uses for its own children.
      The "No turn" group stays compact (SPEC/critic guidance: it's a
      leading catch-all, not a turn) so it gets a plainer, unindented list.
    -->
    <div
      v-if="!collapsed"
      :class="isNoTurn ? 'flex flex-col' : 'border-border/60 ml-[1.15rem] flex flex-col border-l pl-2'"
    >
      <EventRow
        v-for="item in items"
        :key="item.key"
        :item="item"
        :correlation="correlationFor(item)"
        @open="emit('open', $event)"
      />
    </div>
  </section>
</template>
