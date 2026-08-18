<script setup lang="ts">
/**
 * SPEC §6.2/§6.3's firehose feed: streaming rows reusing `EventRow`, a kind
 * filter, pause/resume with a buffered-count badge, auto-scroll with a
 * "jump to latest" affordance, and row-click → `EventDetailSheet`.
 *
 * Fully props-in/events-out (no store read of its own) — the ticket calls
 * this out explicitly as the easier-to-test shape for "100 fake frames
 * render correctly", and it is: every one of this file's tests mounts with
 * a plain `TimelineEvent[]` fixture array, no Pinia store, no fake
 * `EventSource`.
 */
import { computed, nextTick, ref, watch } from 'vue'
import { ArrowUp, Pause, Play } from '@lucide/vue'

import type { TimelineEvent } from '@/stores/live'
import { collapseEvents, type TimelineItem } from '@/lib/collapseEvents'
import { ALL_KINDS, eventKindMeta, type Kind } from '@/lib/eventKinds'
import { formatCount } from '@/lib/format'
import { maxDuration } from '@/lib/timelineDisplay'
import EmptyState from '@/components/common/EmptyState.vue'
import EventDetailSheet from '@/components/timeline/EventDetailSheet.vue'
import EventRow from '@/components/timeline/EventRow.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

interface Props {
  /** Chronological (oldest-first) events to render — same convention as `liveStore.events`; never mutated here. */
  events: TimelineEvent[]
  /** `liveStore.paused` — see the "freeze" doc below for why this component enforces the freeze itself rather than trusting the host not to update `events` while paused. */
  paused: boolean
  /** `liveStore.bufferedWhilePaused` — shown on the resume badge. */
  bufferedCount: number
}

const props = defineProps<Props>()

const emit = defineEmits<{
  /** The user clicked Pause/Resume — the host owns the actual store call (`liveStore.pause()`/`resume()`), since pausing is a store-wide effect (it also freezes `ActiveSessionCards`' "current tool" derivation, which reads the same ring buffer), not something this component can do to itself. */
  pause: []
  resume: []
}>()

/**
 * `props.events` freezes here while `paused`, rather than this component
 * simply rendering `props.events` directly and trusting the host
 * (`liveStore`) to stop pushing into it. Two independent reasons: (1) it
 * makes "pause freezes the list" a verifiable contract of this component's
 * own props, testable by mounting once and calling `setProps` with more
 * events while `paused: true` — without needing to wire a real store/
 * EventSource just to prove the freeze; (2) it is not actually redundant
 * with the store's own pause — a future caller could feed this component
 * from something other than `liveStore` and would still get the documented
 * behaviour "for free".
 */
const frozenEvents = ref<TimelineEvent[]>(props.events)
watch(
  () => props.events,
  (next) => {
    if (!props.paused) frozenEvents.value = next
  },
)
watch(
  () => props.paused,
  (paused) => {
    if (!paused) frozenEvents.value = props.events
  },
)

/**
 * Multi-select over the full closed `Kind` vocabulary (`ALL_KINDS`), not
 * just kinds seen so far on the feed — so a `Kind` Argus defines but that
 * happens not to have arrived yet this session (e.g. `mcp.elicitation`) is
 * still selectable, and `'unknown'` (Argus's own closed-vocabulary escape
 * hatch for an `event_name` it doesn't recognise, SPEC §1.5.1) is always
 * selectable/renderable too — the one member of `Kind` that plays the same
 * "must never be rejected" role SPEC §0's vendor-vocabulary rule plays for
 * `decision`/`tool_source`/etc. `Kind` itself is otherwise the
 * Argus-normalised *closed* set (`eventKinds.ts`'s own doc comment), so
 * there is no arbitrary out-of-schema string to defend against here — that
 * defence belongs to `RawValue`'s fields, not this one.
 * Empty selection = no filter (every kind shown), matching
 * `AnalyticsView.vue`'s existing multi-select `Select` filters.
 */
const selectedKinds = ref<Kind[]>([])

function onKindFilterChange(value: unknown): void {
  selectedKinds.value = Array.isArray(value) ? value.filter((v): v is Kind => typeof v === 'string') : []
}

const kindFilteredEvents = computed<TimelineEvent[]>(() => {
  if (selectedKinds.value.length === 0) return frozenEvents.value
  const selected = new Set<string>(selectedKinds.value)
  return frozenEvents.value.filter((event) => selected.has(event.kind))
})

/**
 * `collapse: false` (PLAN.md P5-05's own prescribed choice): `collapseEvents`'s
 * default 2s-window grouping would make an already-rendered row mutate —
 * gain a "N sources" badge, swap its merged fields — while a reader is
 * actively looking at it. That is acceptable on a static, already-complete
 * historical timeline; it is not acceptable on a feed whose entire point is
 * being watched live. `collapse: false` is the order-preserving, one-row-
 * per-event identity mapping `EventRow` needs (`TimelineEvent` ->
 * `TimelineItem`) — see `collapseEvents.ts`'s own doc for why this is the
 * one sanctioned adapter rather than a hand-built `TimelineItem`.
 */
const timelineItems = computed<TimelineItem[]>(() => collapseEvents(kindFilteredEvents.value, { collapse: false }))

/**
 * The store's `events` (and everything derived from it above) stays
 * chronological oldest-first — the store's own doc comment is explicit that
 * this is deliberate and presentation order is the view's job. The firehose
 * itself reads newest-on-top (SPEC §6.2), so the reversal happens exactly
 * once, right here, immediately before render.
 */
const displayItems = computed<TimelineItem[]>(() => [...timelineItems.value].reverse())

const maxDurationMs = computed(() => maxDuration(displayItems.value))

const isFiltered = computed(() => selectedKinds.value.length > 0)

// --- Auto-scroll -----------------------------------------------------
//
// Inverted from the usual bottom-anchored chat-log pattern because this
// feed renders newest-first: "the latest" lives at scrollTop 0, not at the
// bottom, so "following" means pinned to the TOP. SPEC's own "stops when
// the user scrolls up" (written with a bottom-anchored log in mind)
// becomes, here, "stops when the user scrolls away from the top". New
// frames are prepended above whatever is currently rendered; neither a
// real browser nor jsdom compensates scroll position for content inserted
// above the current viewport on its own, so without the explicit
// `scrollTop = 0` reset below, a user who is following would see the list
// silently scroll away under them on every incoming frame.
const FOLLOW_THRESHOLD_PX = 4

const scrollContainer = ref<HTMLDivElement | null>(null)
const following = ref(true)

function onScroll(): void {
  const el = scrollContainer.value
  if (!el) return
  following.value = el.scrollTop <= FOLLOW_THRESHOLD_PX
}

function jumpToLatest(): void {
  following.value = true
  const el = scrollContainer.value
  if (el) el.scrollTop = 0
}

watch(displayItems, () => {
  if (!following.value) return
  void nextTick(() => {
    const el = scrollContainer.value
    if (el) el.scrollTop = 0
  })
})

// --- Row click -> EventDetailSheet ------------------------------------

const selectedEventRef = ref<string | null>(null)
const sheetOpen = ref(false)

function onRowOpen(eventRef: string): void {
  selectedEventRef.value = eventRef
  sheetOpen.value = true
}
</script>

<template>
  <div
    data-testid="live-feed"
    class="flex flex-col gap-3"
  >
    <div class="flex flex-wrap items-end justify-between gap-3">
      <div class="flex flex-col gap-1">
        <label
          id="live-feed-kind-filter-label"
          class="text-muted-foreground text-xs"
        >Kind</label>
        <Select
          multiple
          :model-value="selectedKinds"
          data-testid="live-feed-kind-filter"
          @update:model-value="onKindFilterChange"
        >
          <SelectTrigger
            aria-labelledby="live-feed-kind-filter-label"
            class="w-52"
          >
            <SelectValue placeholder="All kinds" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="kind in ALL_KINDS"
              :key="kind"
              :value="kind"
            >
              {{ eventKindMeta(kind).label }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="flex items-center gap-2">
        <Button
          v-if="!following"
          type="button"
          size="sm"
          variant="secondary"
          data-testid="live-feed-jump-latest"
          @click="jumpToLatest"
        >
          <ArrowUp
            class="size-3.5"
            aria-hidden="true"
          />
          Jump to latest
        </Button>

        <Badge
          v-if="paused && bufferedCount > 0"
          role="status"
          variant="secondary"
          data-testid="live-feed-buffered-badge"
        >
          {{ formatCount(bufferedCount) }} buffered
        </Badge>

        <Button
          type="button"
          size="sm"
          variant="outline"
          data-testid="live-feed-pause-toggle"
          @click="paused ? emit('resume') : emit('pause')"
        >
          <component
            :is="paused ? Play : Pause"
            class="size-3.5"
            aria-hidden="true"
          />
          {{ paused ? 'Resume' : 'Pause' }}
        </Button>
      </div>
    </div>

    <EmptyState
      v-if="displayItems.length === 0"
      title="No events yet"
      :description="isFiltered ? 'No buffered event matches the selected kinds.' : 'Events appear here as soon as they arrive on the live feed.'"
    />
    <!--
      Layout thrash (PLAN.md P5-05's explicit AC) — what this markup does
      about it:
        1. `:key="item.key"` (the anchor event_ref, stable per row) lets
           Vue's keyed list-patch reuse existing row DOM nodes across a
           push: a new frame only ever *prepends* one new item, so every
           other row keeps the same key at a shifted index and Vue moves
           its existing element rather than destroying/recreating it.
           Without a stable key, every push would tear down and rebuild the
           entire visible list.
        2. `EventRow` itself (reused, not edited) already fixes each row's
           height (`h-8`/`h-7`) and right-aligns every numeric column in a
           fixed-width `tabular-nums` cell, so neither a new row's height
           nor an existing row's digit count ever reflows a neighbour.
        3. `scrollTop` is only written from the `watch(displayItems, ...)`
           below when `following` is true — a paused/scrolled-up reader
           never has their scroll position fought over by an incoming
           frame.
    -->
    <div
      v-else
      ref="scrollContainer"
      class="border-border max-h-[32rem] overflow-y-auto rounded-lg border"
      data-testid="live-feed-scroll"
      @scroll="onScroll"
    >
      <EventRow
        v-for="item in displayItems"
        :key="item.key"
        :item="item"
        :selected="item.key === selectedEventRef"
        :max-duration-ms="maxDurationMs"
        @open="onRowOpen"
      />
    </div>

    <EventDetailSheet
      :event-ref="selectedEventRef"
      :open="sheetOpen"
      @update:open="sheetOpen = $event"
    />
  </div>
</template>
