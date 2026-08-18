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
import { formatAbsoluteTime, formatCost, formatDuration, formatRelativeOffset, formatTokens } from '@/lib/format'
import { durationBarScale, rowDetail } from '@/lib/timelineDisplay'
import { Badge } from '@/components/ui/badge'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import DecisionBadge, { type Correlation } from './DecisionBadge.vue'

interface Props {
  item: TimelineItem
  /** ToolCall.correlation for this item's decision, when known — see EventRow's host for how it's derived. */
  correlation?: Correlation | null
  /** True when this row's event_ref is the one currently open in the inspector — the only visible selection cue (round-3 critic gap: "row selection state must be visible"). */
  selected?: boolean
  /** True for a tool-thread child (tool.decision/tool.permission_request/tool.result nested under its tool.pre call, see TimelineGroup's `buildToolThreads` usage) — renders slightly smaller/quieter than a top-level row, since the thread's own rail already shows the nesting. */
  nested?: boolean
  /**
   * The first event's `ts` in the currently loaded timeline (round-5: not
   * `session.started_at` — that anchor produced multi-day offsets whenever a
   * session's recorded start drifted from its earliest event; see
   * `Timeline.vue`'s `originTs`) — the origin `item.ts` is offset against
   * (round-4 critic gap: repeating the same absolute date down a whole
   * column of rows is unscannable; a relative offset from a shared origin
   * is). The absolute timestamp still lives in the row's tooltip and the
   * inspector, never discarded, just no longer the thing eating the row.
   */
  originTs?: string | null
  /** The session's largest observed `duration_ms`, for scaling this row's duration bar — see `durationBarScale`. `0`/absent renders no bar. */
  maxDurationMs?: number
}

const props = withDefaults(defineProps<Props>(), { correlation: null, selected: false, nested: false, originTs: null, maxDurationMs: 0 })

const emit = defineEmits<{
  /** The user wants the raw `attrs` for one event_ref — the primary event by default, or a specific source from the "N sources" list. */
  open: [eventRef: string]
}>()

const meta = computed(() => eventKindMeta(props.item.kind))
const hasMultipleSources = computed(() => props.item.events.length > 1)
const primaryEventRef = computed(() => props.item.events[0]!.event_ref)
/** The row's distinguishing detail (tool_name or model — see module doc on `rowDetail`) — `null` renders nothing, never a fake subject. */
const detail = computed(() => rowDetail(props.item))
const barScale = computed(() => durationBarScale(props.item.duration_ms, props.maxDurationMs))

function openPrimary() {
  emit('open', primaryEventRef.value)
}

function openEvent(eventRef: string) {
  emit('open', eventRef)
}
</script>

<template>
  <!--
    One dense line per row (round-5 critic gap: the old two-line layout —
    label/detail on one line, offset/duration/cost/tokens repeated below —
    ate ~60px/row for ~4 short fields; collapsing to a single row with the
    metrics right-aligned in fixed-width tabular-nums columns gets a row
    under 32px and 3-4x more of them on screen without dropping any field —
    everything from before is still here, just on one line).
  -->
  <div
    class="border-border/50 hover:bg-muted/40 flex min-w-0 cursor-pointer items-center gap-3 border-b text-sm"
    :class="[nested ? 'h-7 px-2' : 'h-8 px-3', selected ? 'bg-muted border-l-primary border-l-2' : '']"
    data-testid="event-row"
    :data-selected="selected"
    role="button"
    tabindex="0"
    :aria-selected="selected"
    @click="openPrimary"
    @keydown.enter="openPrimary"
  >
    <component
      :is="meta.icon"
      class="text-muted-foreground shrink-0"
      :class="nested ? 'size-3.5' : 'size-4'"
      aria-hidden="true"
    />

    <!-- Left cluster: identity — label, detail chip, decision pill, skew flag. Truncates before the right-hand metrics ever do. -->
    <div class="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
      <span class="text-foreground shrink-0 font-medium">{{ meta.label }}</span>
      <span
        v-if="detail"
        class="text-muted-foreground shrink-0 truncate font-mono text-xs"
        data-testid="event-row-detail"
      >{{ detail }}</span>
      <DecisionBadge
        v-if="item.decision !== null"
        class="shrink-0"
        :decision="item.decision"
        :decision-source="item.decision_source"
        :correlation="correlation"
      />
      <AlertTriangle
        v-if="item.clock_skewed"
        class="text-warn size-3.5 shrink-0"
        aria-hidden="true"
        title="This event's clock is skewed — its timestamp may be unreliable"
      />
      <span
        v-if="item.file_path"
        class="text-muted-foreground min-w-0 truncate font-mono text-xs"
      >{{ item.file_path }}</span>
    </div>

    <!--
      Right cluster: fixed-width, right-aligned, tabular-nums metric columns
      so offset/duration/cost/tokens line up down the whole list (round-5
      critic: "right-side metrics in fixed-width columns so they align down
      the list"). The offset leads with its varying digits (round-5: relative
      to the first loaded event, not a repeated absolute date — round-4's
      gap) with the absolute timestamp demoted to a hover/inspector detail.
    -->
    <div class="text-muted-foreground flex shrink-0 items-center gap-3 text-xs">
      <span
        class="w-16 text-right tabular-nums"
        data-testid="event-row-offset"
        :title="formatAbsoluteTime(item.ts)"
      >{{ formatRelativeOffset(item.ts, originTs) }}</span>

      <!--
        Duration bar folded into the single line: a fixed-width inline track
        beside its own text, scaled (log) against the session's max observed
        duration (round-4 critic ask, kept — round-5 just moves it onto the
        row instead of a line below it).
      -->
      <span class="flex w-16 shrink-0 items-center justify-end gap-1.5">
        <span
          v-if="item.duration_ms !== null && maxDurationMs > 0"
          class="bg-border/40 h-[3px] w-6 shrink-0 overflow-hidden rounded-full"
          data-testid="event-row-duration-bar"
          role="presentation"
        >
          <span
            class="bg-muted-foreground/60 block h-full rounded-full"
            :style="{ width: `${barScale}%` }"
          />
        </span>
        <span
          v-if="item.duration_ms !== null"
          class="tabular-nums"
        >{{ formatDuration(item.duration_ms) }}</span>
      </span>

      <span
        v-if="item.cost !== null"
        class="text-cost w-14 text-right tabular-nums"
      >{{ formatCost(item.cost) }}</span>
      <span
        v-if="item.tokens"
        class="w-14 text-right tabular-nums"
      >{{ formatTokens(item.tokens.input + item.tokens.output) }} tok</span>
    </div>

    <Popover v-if="hasMultipleSources">
      <PopoverTrigger
        as-child
        @click.stop
      >
        <Badge
          variant="secondary"
          class="shrink-0"
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
