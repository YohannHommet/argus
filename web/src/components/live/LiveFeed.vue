<script setup lang="ts">
/**
 * SPEC §6.2/§6.3's firehose feed: streaming rows reusing `EventRow`, a kind
 * filter, pause/resume with a buffered-count badge, auto-scroll with a
 * "jump to latest" affordance, row-click → `EventDetailSheet`, and (round-6
 * critic gap) a sticky labeled header naming its own columns — `EventRow`'s
 * right-hand metrics are otherwise four unlabeled numeric tracks, mostly
 * `EM_DASH` on any one row, with no way to tell which is which.
 *
 * Round-8 critic gap: the "Time" column rendered each row's own
 * vendor-emitted `ts` (SPEC's `ts`, not arrival order) down a list ordered
 * by *arrival* (newest-received-first) — out-of-order event clocks are real
 * (clock skew, multi-source fan-in) and this is the one view whose whole
 * point is showing stream truth, so the fix is not to re-sort the feed, it
 * is to stop mislabeling the column. The column is now "Received": each
 * row's client wall-clock the instant this tab's `EventSource` handler saw
 * the frame (`liveStore`'s `receivedAt`, stamped once as frames land —
 * monotonic by construction), which reads monotonically top-to-bottom by
 * the same construction that made the row order itself monotonic. The
 * event's own `ts` is not discarded — it rides along on the row's own hover
 * title (see the `title` fallthrough on `EventRow` below) and stays exactly
 * what `EventDetailSheet` shows in the inspector, since that fetches by
 * `event_ref` independently of anything this column displays.
 *
 * Round-9 critic gap: `EventRow`'s identity cluster (label/detail/decision/
 * skew/file_path) rendered `flex-1`, so on a row whose own content was short
 * it stretched to soak up every pixel between it and the right-hand metric
 * cluster — a 460–630px dead void, up to half the table, versus ~95px on
 * `SessionTable.vue`'s tightest inter-column gap. The row below now passes
 * `compact-event-column` (see `EventRow.vue`'s own doc on that prop): the
 * identity cluster gets a fixed `w-96` instead of a growing one, so the
 * metric cluster sits immediately after it rather than floating to the
 * table's far edge. On a wide viewport that leaves trailing whitespace after
 * the row's content — an accepted trade for a left-weighted, Sessions-style
 * table rather than a full-bleed one.
 *
 * Fully props-in/events-out (no store read of its own) — the ticket calls
 * this out explicitly as the easier-to-test shape for "100 fake frames
 * render correctly", and it is: every one of this file's tests mounts with
 * a plain `TimelineEvent[]` fixture array, no Pinia store, no fake
 * `EventSource`. `receivedAt` is optional for exactly that reason — a
 * fixture that never went through `liveStore` still renders, falling back
 * to the event's own `ts`.
 */
import { computed, nextTick, ref, watch } from 'vue'
import { ArrowUp, Pause, Play } from '@lucide/vue'

import type { LiveTimelineEvent, SessionSummary } from '@/stores/live'
import { collapseEvents, type TimelineItem } from '@/lib/collapseEvents'
import { ALL_KINDS, eventKindMeta, type Kind } from '@/lib/eventKinds'
import { formatAbsoluteTime, formatCount, formatDuration } from '@/lib/format'
import { maxDuration } from '@/lib/timelineDisplay'
import EmptyState from '@/components/common/EmptyState.vue'
import EventDetailSheet from '@/components/timeline/EventDetailSheet.vue'
import EventRow from '@/components/timeline/EventRow.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

interface Props {
  /** Chronological (oldest-first) events to render — same convention as `liveStore.events`; never mutated here. */
  events: LiveTimelineEvent[]
  /**
   * `liveStore.sessions.values()` — read only to resolve each row's compact
   * "project · shortId" identity column (round-5 critic gap: "it's a
   * fleet-wide firehose", every row needs to say whose session it is).
   * Defaults to empty so a caller that doesn't track sessions (e.g. a plain
   * fixture-driven test) still gets a valid feed, just falling back to
   * short-id-only labels.
   */
  sessions?: SessionSummary[]
  /** `liveStore.paused` — see the "freeze" doc below for why this component enforces the freeze itself rather than trusting the host not to update `events` while paused. */
  paused: boolean
  /** `liveStore.bufferedWhilePaused` — shown on the resume badge. */
  bufferedCount: number
}

const props = withDefaults(defineProps<Props>(), { sessions: () => [] })

/**
 * Keyed by `session_id` so each row can resolve "project · shortId" without
 * a linear scan per row. `project` can legitimately be empty (a session
 * still waiting on its hook `session.start`/`workspace.cwd_changed` event —
 * see `lib/nullReasons.ts`'s `NO_PROJECT_SIGNAL_YET`), in which case the
 * label falls back to the short id alone rather than rendering a bare "·".
 * A session this tab has never received a `session` frame for (e.g. it
 * ended and aged out before this tab connected) gets the same short-id-only
 * fallback — never a fabricated project name.
 */
const sessionsById = computed(() => new Map(props.sessions.map((s) => [s.id, s])))

function sessionLabel(item: TimelineItem): string {
  const shortId = item.session_id.slice(0, 8)
  const project = sessionsById.value.get(item.session_id)?.project
  return project ? `${project} · ${shortId}` : shortId
}

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
const frozenEvents = ref<LiveTimelineEvent[]>(props.events)
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

const kindFilteredEvents = computed<LiveTimelineEvent[]>(() => {
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

/**
 * `event_ref -> receivedAt`, built straight from `props.events` rather than
 * from `timelineItems`/`displayItems`: `collapseEvents` types its output
 * `TimelineItem`s (and their nested `events: TimelineEvent[]`) against the
 * wire schema, which has no `receivedAt` field, so that store-only value
 * doesn't survive the trip through it. `item.key` is exactly the anchor
 * event's `event_ref` (`collapseEvents.ts`'s own doc — `collapse: false`
 * gives a 1:1 mapping), so it is the right join key back to this map.
 */
const receivedAtByRef = computed(() => {
  const map = new Map<string, string>()
  for (const event of props.events) {
    if (event.receivedAt) map.set(event.event_ref, event.receivedAt)
  }
  return map
})

/**
 * `EventRow`'s `wallClockTime` mode reads a single field, `item.ts`, for
 * both the "Received" column's value and that same cell's own hover title
 * (`EventRow.vue`: `formatWallClockTime(item.ts)` / `formatAbsoluteTime(item.ts)`).
 * Rather than adding a second time field to `EventRow`'s contract, this
 * feeds it a shallow-copied item whose `ts` *is* the receive stamp — the
 * one and only value that column now means. Falls back to the event's own
 * `ts` when no `receivedAt` is known (a plain-fixture test that never went
 * through `liveStore`), so every prop-driven test not about this column
 * keeps rendering exactly as before.
 */
function receivedDisplayItem(item: TimelineItem): TimelineItem {
  const receivedAt = receivedAtByRef.value.get(item.key)
  return receivedAt ? { ...item, ts: receivedAt } : item
}

/**
 * The event's own `ts` — no longer this row's headline value, but not
 * discarded either (round-8 decision: "one honest label, one monotonic
 * read", not "throw the vendor timestamp away"). Surfaced as a plain HTML
 * `title` passed straight through to `<EventRow>`: it isn't one of
 * `EventRow`'s declared props, so Vue's default attribute fallthrough lands
 * it on the row's own root element as a native hover tooltip, with no
 * change to `EventRow.vue` itself.
 */
function eventTimeTitle(item: TimelineItem): string {
  return `Event time: ${formatAbsoluteTime(item.ts)}`
}

const maxDurationMs = computed(() => maxDuration(displayItems.value))

const isFiltered = computed(() => selectedKinds.value.length > 0)

/**
 * Round-6 critic ask: "a stream count near the Pause control" — the total
 * frames this tab has seen on the firehose, unfiltered by the kind select
 * (that's what "this tab" is honestly counting: what arrived, not what the
 * reader currently has selected to look at). Reads `props.events`, not
 * `frozenEvents`/`kindFilteredEvents`, deliberately: even while paused, the
 * count should keep telling the truth about what this tab has received —
 * only the *rendered rows* freeze on pause, per the doc comment above.
 */
const totalEventCount = computed(() => props.events.length)

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

      <div class="flex items-center gap-3">
        <span
          class="text-muted-foreground text-xs"
          data-testid="live-feed-event-count"
        >{{ formatCount(totalEventCount) }} events this tab</span>

        <!--
          Labels the per-row duration bar's scale, same convention as
          `Timeline.vue`'s own `timeline-duration-scale-note` — only shown
          once something on the visible feed has a measured duration to
          scale against.
        -->
        <span
          v-if="maxDurationMs > 0"
          class="text-muted-foreground text-xs"
          data-testid="live-feed-duration-scale-note"
        >Duration bar: log scale, max {{ formatDuration(maxDurationMs) }}</span>

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
        2. `EventRow` itself already fixes each row's height (`h-8`/`h-7`)
           and right-aligns every numeric column in a fixed-width
           `tabular-nums` cell, so neither a new row's height nor an
           existing row's digit count ever reflows a neighbour.
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
      <!--
        Round-6 critic gap: the feed's four right-hand numeric columns had no
        header at all, so a reader could not tell what "—" meant in any of
        them. `sticky top-0` (the scroll container above is this row's own
        scrolling ancestor) keeps it pinned while the newest-first list
        scrolls underneath it — same shaded-row idiom `SessionTable.vue`'s
        `TableHeader` uses (`bg-muted/40`, a bottom border), just as a plain
        `div` rather than a `<table>` since this feed already isn't one
        (fixed-width flex columns, not a `<table>`, per `EventRow` itself).
        Column widths/gaps below are kept in lockstep with `EventRow`'s own
        markup by hand — there is no single source of truth for them to
        drift from without one, but the two are right next to each other in
        every diff that touches either.

        Round-9 critic gap: the "Event" header span below used to be
        `min-w-0 flex-1`, growing to match `EventRow`'s own then-`flex-1`
        identity cluster — which is exactly what stranded the metric
        columns at the row's far edge with a 460–630px void in between (one
        row reading as two disconnected halves). It's now the fixed `w-96`
        `EventRow` renders under `compact-event-column` below, so the
        "Received" header (and the rest of the metric cluster) sits right
        after it — same tight rhythm as `SessionTable.vue`'s columns —
        rather than floating off to the table's edge.
      -->
      <div
        class="border-border bg-muted/40 text-muted-foreground sticky top-0 z-10 flex min-w-0 items-center gap-3 border-b px-3 text-xs font-medium"
        data-testid="live-feed-header"
      >
        <span
          class="size-4 shrink-0"
          aria-hidden="true"
        />
        <span
          class="w-28 shrink-0"
          data-testid="live-feed-header-session"
        >Session</span>
        <span
          class="w-96 shrink-0"
          data-testid="live-feed-header-event"
        >Event</span>
        <div class="flex shrink-0 items-center gap-3">
          <span
            class="w-16 text-right"
            data-testid="live-feed-header-received"
          >Received</span>
          <span
            class="w-16 text-right"
            data-testid="live-feed-header-duration"
          >Duration</span>
          <span
            class="w-14 text-right"
            data-testid="live-feed-header-cost"
          >Cost</span>
          <span
            class="w-14 text-right"
            data-testid="live-feed-header-tokens"
          >Tokens</span>
        </div>
      </div>

      <EventRow
        v-for="item in displayItems"
        :key="item.key"
        :item="receivedDisplayItem(item)"
        :selected="item.key === selectedEventRef"
        :max-duration-ms="maxDurationMs"
        :session-label="sessionLabel(item)"
        :title="eventTimeTitle(item)"
        wall-clock-time
        compact-event-column
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
