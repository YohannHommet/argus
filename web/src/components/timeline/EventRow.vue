<script setup lang="ts">
/**
 * One collapsed timeline row (a `TimelineItem` from `collapseEvents`).
 * Clicking the row opens the detail drawer on its primary (first) raw
 * event; when the item collapsed more than one source, a "N sources"
 * affordance lists each raw member individually — clicking one opens the
 * drawer on that specific `event_ref` (SPEC §1.5.3(b): collapsing must stay
 * reversible/inspectable, not just togglable at the top level).
 */
import { computed } from 'vue'
import { AlertTriangle } from '@lucide/vue'

import type { TimelineItem } from '@/lib/collapseEvents'
import { eventKindMeta } from '@/lib/eventKinds'
import { formatAbsoluteTime, formatCost, formatDuration, formatTokens } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import DecisionBadge, { type Correlation } from './DecisionBadge.vue'

interface Props {
  item: TimelineItem
  /** ToolCall.correlation for this item's decision, when known — see EventRow's host for how it's derived. */
  correlation?: Correlation | null
}

const props = withDefaults(defineProps<Props>(), { correlation: null })

const emit = defineEmits<{
  /** The user wants the raw `attrs` for one event_ref — the primary event by default, or a specific source from the "N sources" list. */
  open: [eventRef: string]
}>()

const meta = computed(() => eventKindMeta(props.item.kind))
const hasMultipleSources = computed(() => props.item.events.length > 1)
const primaryEventRef = computed(() => props.item.events[0]!.event_ref)

function openPrimary() {
  emit('open', primaryEventRef.value)
}

function openEvent(eventRef: string) {
  emit('open', eventRef)
}
</script>

<template>
  <div
    class="border-border/50 hover:bg-muted/40 flex cursor-pointer items-start gap-3 border-b px-3 py-2 text-sm"
    data-testid="event-row"
    role="button"
    tabindex="0"
    @click="openPrimary"
    @keydown.enter="openPrimary"
  >
    <component
      :is="meta.icon"
      class="text-muted-foreground mt-0.5 size-4 shrink-0"
      aria-hidden="true"
    />

    <div class="min-w-0 flex-1">
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-foreground font-medium">{{ meta.label }}</span>
        <span
          v-if="item.tool_name"
          class="text-muted-foreground font-mono text-xs"
        >{{ item.tool_name }}</span>
        <DecisionBadge
          v-if="item.decision !== null"
          :decision="item.decision"
          :decision-source="item.decision_source"
          :correlation="correlation"
        />
        <AlertTriangle
          v-if="item.clock_skewed"
          class="text-warn size-3.5"
          aria-hidden="true"
          title="This event's clock is skewed — its timestamp may be unreliable"
        />
      </div>
      <div class="text-muted-foreground mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs">
        <span>{{ formatAbsoluteTime(item.ts) }}</span>
        <span v-if="item.duration_ms !== null">{{ formatDuration(item.duration_ms) }}</span>
        <span
          v-if="item.cost !== null"
          class="text-cost"
        >{{ formatCost(item.cost) }}</span>
        <span v-if="item.tokens">{{ formatTokens(item.tokens.input + item.tokens.output) }} tok</span>
        <span v-if="item.file_path">{{ item.file_path }}</span>
      </div>
    </div>

    <Popover v-if="hasMultipleSources">
      <PopoverTrigger
        as-child
        @click.stop
      >
        <Badge
          variant="secondary"
          data-testid="event-row-sources"
        >
          {{ item.sources.length }} sources
        </Badge>
      </PopoverTrigger>
      <PopoverContent
        class="w-64 p-2"
        @click.stop
      >
        <p class="text-muted-foreground mb-2 text-xs font-medium">
          Collapsed from {{ item.events.length }} raw events
        </p>
        <ul class="flex flex-col gap-1">
          <li
            v-for="event in item.events"
            :key="event.event_ref"
          >
            <button
              type="button"
              class="hover:bg-muted flex w-full items-center justify-between gap-2 rounded px-2 py-1 text-left text-xs"
              @click="openEvent(event.event_ref)"
            >
              <span class="font-mono">{{ event.source }}</span>
              <span class="text-muted-foreground">{{ formatAbsoluteTime(event.ts) }}</span>
            </button>
          </li>
        </ul>
      </PopoverContent>
    </Popover>
  </div>
</template>
