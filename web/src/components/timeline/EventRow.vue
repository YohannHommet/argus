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
import { EM_DASH, formatAbsoluteTime, formatCost, formatDuration, formatRelativeOffset, formatTokens, formatWallClockTime } from '@/lib/format'
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
   * The first event's `ts` in the currently loaded timeline, not `session.started_at` (see
   * `Timeline.vue`'s `originTs`) — `item.ts` is offset against this shared origin so the column reads
   * as a scannable relative offset instead of repeating the same absolute date down every row. The
   * absolute timestamp still lives in the row's tooltip and the inspector, never discarded.
   */
  originTs?: string | null
  /** The session's largest observed `duration_ms`, for scaling this row's duration bar — see `durationBarScale`. `0`/absent renders no bar. */
  maxDurationMs?: number
  /**
   * Compact "project · shortId" (or just the short id, when the project
   * isn't known yet) identifying which session this row belongs to.
   * `null`/absent renders no column at all — a single-session timeline
   * (`Timeline.vue`) already has its one session in the page header, so
   * repeating it on every row would be noise; only a multi-session view
   * (the live firehose, `LiveFeed.vue`) supplies this.
   */
  sessionLabel?: string | null
  /**
   * The offset column reads `formatRelativeOffset` against `originTs` — meaningful with a fixed
   * anchor (the first loaded event), meaningless on a live firehose with no such anchor (always
   * `EM_DASH`). `true` swaps that column to the row's own wall-clock time
   * (`formatWallClockTime(item.ts)`) instead. Default `false` so `Timeline.vue`/`TimelineGroup.vue`,
   * which never pass this prop, render byte-for-byte as before.
   */
  wallClockTime?: boolean
  /**
   * `true` gives the identity cluster (label/detail/decision/skew/file_path) a fixed content width
   * instead of a growing `flex-1`, so the metric cluster sits immediately after it instead of
   * stranding at the row's far edge on a wide row — at the cost of trailing whitespace, an accepted
   * left-weighted-table trade. It also lets the `tool_name`/model detail chip truncate under that
   * fixed width rather than overflow, since a vendor string is unbounded length. Default `false` so
   * `Timeline.vue`/`TimelineGroup.vue`, which never pass this prop, render byte-for-byte as before.
   */
  compactEventColumn?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  correlation: null,
  selected: false,
  nested: false,
  originTs: null,
  maxDurationMs: 0,
  sessionLabel: null,
  wallClockTime: false,
  compactEventColumn: false,
})

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
const totalTokens = computed(() => (props.item.tokens ? props.item.tokens.input + props.item.tokens.output : null))

/**
 * See `compactEventColumn`'s doc above: a fixed `w-96` instead of the growing `flex-1`
 * `Timeline.vue`/`TimelineGroup.vue` still get by default — sized off `eventKinds.ts`'s longest
 * labels plus a typical detail chip and decision badge, long enough that most real rows never truncate.
 */
const eventColumnClass = computed(() =>
  props.compactEventColumn ? 'flex w-96 shrink-0 items-center gap-2 overflow-hidden' : 'flex min-w-0 flex-1 items-center gap-2 overflow-hidden',
)

/**
 * The detail chip's `shrink-0` is fine under `flex-1` (the cluster just grows
 * to fit it) but would force `compactEventColumn`'s fixed-width cluster to
 * overflow past its own edge instead of truncating whenever a vendor
 * `tool_name`/model is long — so compact mode trades `shrink-0` for
 * `min-w-0`, letting `truncate` (already on this span either way) actually
 * engage. The kind label and `DecisionBadge` stay `shrink-0` unconditionally
 * either way — those are protected idioms, never squeezed.
 */
const detailClass = computed(() =>
  props.compactEventColumn ? 'text-muted-foreground min-w-0 truncate font-mono text-xs' : 'text-muted-foreground shrink-0 truncate font-mono text-xs',
)

function openPrimary() {
  emit('open', primaryEventRef.value)
}

function openEvent(eventRef: string) {
  emit('open', eventRef)
}
</script>

<template>
  <!--
    One dense line per row: metrics right-aligned in fixed-width tabular-nums columns keeps rows
    under 32px, fitting far more of them on screen without dropping any field.
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

    <!-- Session identity column — only a multi-session view (the live firehose) supplies this; a single-session timeline renders no column at all. -->
    <span
      v-if="sessionLabel"
      class="text-muted-foreground w-28 shrink-0 truncate font-mono text-xs"
      data-testid="event-row-session"
      :title="sessionLabel"
    >{{ sessionLabel }}</span>

    <!-- Left cluster: identity — label, detail chip, decision pill, skew flag. Truncates before the right-hand metrics ever do. -->
    <div :class="eventColumnClass">
      <span class="text-foreground shrink-0 font-medium">{{ meta.label }}</span>
      <span
        v-if="detail"
        :class="detailClass"
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
      Right cluster: fixed-width, right-aligned, tabular-nums metric columns so offset/duration/
      cost/tokens line up down the whole list. The offset leads with its varying digits, relative to
      the first loaded event rather than a repeated absolute date, which is demoted to a
      hover/inspector detail.

      Every column slot is unconditionally rendered — a `v-if` that drops a whole slot's width would
      shift the other columns to different x-offsets depending on which fields a given row carries.
      Each formatter already renders `EM_DASH` for a null/absent value (SPEC §6.1), so the fix is to
      let it render, not to remove the slot's reserved width.
    -->
    <div class="text-muted-foreground flex shrink-0 items-center gap-3 text-xs">
      <span
        class="w-16 text-right tabular-nums"
        :data-testid="wallClockTime ? 'event-row-time' : 'event-row-offset'"
        :title="formatAbsoluteTime(item.ts)"
      >{{ wallClockTime ? formatWallClockTime(item.ts) : formatRelativeOffset(item.ts, originTs) }}</span>

      <!--
        Duration bar folded into the single line: a fixed-width inline track beside its own text,
        scaled (log) against the session's max observed duration.
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
          class="tabular-nums"
          data-testid="event-row-duration"
        >{{ formatDuration(item.duration_ms) }}</span>
      </span>

      <!--
        `text-cost` (theme.css's `--foreground`, full-contrast) only when there is a real cost to
        show — applying it unconditionally would render a null cost's EM_DASH at emphasized
        brightness instead of muted, especially on the live feed where cost is null far more often.
      -->
      <span
        class="w-14 text-right tabular-nums"
        :class="item.cost !== null ? 'text-cost' : ''"
        data-testid="event-row-cost"
      >{{ formatCost(item.cost) }}</span>
      <span
        class="w-14 text-right tabular-nums"
        data-testid="event-row-tokens"
      >{{ totalTokens !== null ? `${formatTokens(totalTokens)} tok` : EM_DASH }}</span>
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
