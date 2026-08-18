<script setup lang="ts">
/**
 * The Timeline tab (PLAN.md P4-04 / Phase-4 exit criterion 3): turn-grouped,
 * collapsible, filterable, infinitely-scrollable events for the current
 * session (`useSessionDetailStore().currentId`). Mounted by
 * `SessionDetailView.vue`, which this component does not touch or assume
 * anything about beyond "a session is already current".
 *
 * Design decisions this component makes (documented here so the phase lead
 * doesn't have to read the source to wire it in):
 *
 * - **Kind filter chips are server-side**, via the store's own
 *   `kinds`/`setTimelineFilters` + `loadTimeline({reset:true})` — not a
 *   client-side `.filter()` over already-fetched pages. The timeline is
 *   cursor-paginated; filtering after the fact would leave partial, wrong
 *   page sizes and require re-fetching to fill the viewport, which is
 *   exactly what the server-side `kinds` query param exists to avoid.
 * - **Collapsing is purely client-side and local** (`collapseEnabled` ref),
 *   per SPEC §1.5.3(b) / D-24: `?collapse=` is never sent to the server —
 *   `collapseEvents` runs over whatever page(s) are already loaded.
 * - **Chips are built from the kinds actually present in the loaded raw
 *   events**, not the full 43-value `Kind` union — a session realistically
 *   emits a dozen or so kinds, and 43 chips would be an unusable wall (same
 *   reasoning as `DecisionMatrix`'s dynamic columns).
 * - **Grouping is by contiguous run of `prompt_id`** (including `null`,
 *   rendered as an explicit "No turn" header) — NOT a global bucket keyed by
 *   `prompt_id` across the whole session. A global bucket was tried first
 *   and was a real bug: `store.timelineItems` is chronological, but bucketing
 *   by "every item with this prompt_id, wherever it occurs" pulled a
 *   no-turn event that happened *after* a turn started into the leading
 *   no-turn block anyway, so that turn's header rendered below events later
 *   than its own — SPEC's whole point of a turn-grouped timeline is a
 *   readable top-to-bottom chronology, which a global bucket silently broke.
 *   Splitting on contiguous runs instead means groups render in exactly the
 *   input order; the (rare) session whose events for one turn are truly
 *   non-contiguous renders that turn as more than one block instead of
 *   reordering the timeline to hide it — an honest reflection of what
 *   happened, per SPEC's raw-data-first stance.
 * - **`correlationFor` is a local proxy, not `ToolCall.correlation`.**
 *   Fetching the tool-calls list to join by `tool_use_id` is P4-06's
 *   endpoint, out of this ticket's scope. Here, an item's decision is
 *   treated as `'exact'` when one of its raw members is the authoritative
 *   `otel_log`/`tool.decision` event, and `'heuristic'` otherwise (e.g. the
 *   decision was inferred from a hook or `tool.result` alone) — good enough
 *   to drive the badge's caveat without pretending to be the server value.
 * - **Infinite scroll**: an `IntersectionObserver` sentinel triggers
 *   `loadMoreTimeline()` where the browser supports it; a "Load more"
 *   button is always rendered too (visible whenever more pages remain) as
 *   the guaranteed-reliable, directly-testable trigger — jsdom has no
 *   `IntersectionObserver`, so the button is what the test suite drives.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { X } from '@lucide/vue'

import { type TimelineItem, collapseEvents } from '@/lib/collapseEvents'
import { eventKindMeta } from '@/lib/eventKinds'
import type { Kind } from '@/lib/eventKinds'
import { formatDuration } from '@/lib/format'
import { maxDuration } from '@/lib/timelineDisplay'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import type { Correlation } from './DecisionBadge.vue'
import EventInspector from './EventInspector.vue'
import TimelineGroup from './TimelineGroup.vue'

const store = useSessionDetailStore()

/**
 * Round-6 critic gap: an agent filter applied from the Subagents tree
 * (`?tab=timeline&agent_id=…`) had no visible chip and no way to clear it
 * short of editing the URL. Per the module doc above, `agentId` is the one
 * timeline filter this component does not own the source of — so clearing
 * it is an emit, not a direct `store.setTimelineFilters` call, exactly like
 * routing that filter in is SessionDetailView's job via the route watcher.
 */
const emit = defineEmits<{ 'clear-agent-filter': [] }>()

const collapseEnabled = ref(true)

const items = computed<TimelineItem[]>(() => collapseEvents(store.timelineItems, { collapse: collapseEnabled.value }))

/**
 * The shared duration-bar scale (round-4 critic ask) — computed over the
 * currently loaded/collapsed items, not the whole session's history the
 * server may hold: the bar is meant to make *this view* scannable, and a
 * scale drawn from pages not yet loaded would silently shrink as more pages
 * arrive, which is worse than a scale that's honest about what's on screen.
 */
const maxDurationMs = computed(() => maxDuration(items.value))

/**
 * The offset column's origin: the first event in the *currently loaded*
 * timeline, not `session.started_at` (round-5 critic gap — that anchor
 * produced multi-day offsets whenever a session's recorded start drifted
 * from its earliest event, e.g. a real capture with `started_at` ~11 days
 * after its own earliest events; every row read "+11d 0Xh ..." and the
 * offset column stopped being useful). `store.timelineItems` is
 * chronological (see module doc above), so the first collapsed item is
 * always the earliest one on screen — stable across `loadMoreTimeline`
 * (appends later items, never earlier ones) and naturally re-derived to a
 * new origin whenever a filter change resets the loaded set.
 */
const originTs = computed<string | null>(() => items.value[0]?.ts ?? null)

interface Group {
  /** Stable across re-renders: the group's anchor (first) item's key — unique even when two runs share the same `promptId` (see module doc). */
  id: string
  promptId: string | null
  items: TimelineItem[]
  /** True when an earlier group in this same list already used this (non-null) promptId — see below. */
  isContinuation: boolean
}

/**
 * Contiguous runs of the same `prompt_id` — see the module doc for why this
 * is a run split, not a global bucket keyed by prompt_id. A second (or
 * later) run of the same non-null `prompt_id` is flagged `isContinuation`
 * so `TimelineGroup` can label it "Turn N · continued" instead of a bare
 * repeated "Turn N" header, which unlabelled reads as a bug (round-3
 * critic gap: "'Turn 0' appearing twice ... reads as broken").
 */
const groups = computed<Group[]>(() => {
  const result: Group[] = []
  const seenPromptIds = new Set<string>()
  for (const item of items.value) {
    const key = item.prompt_id
    const current = result[result.length - 1]
    if (current && current.promptId === key) {
      current.items.push(item)
    } else {
      const isContinuation = key !== null && seenPromptIds.has(key)
      if (key !== null) seenPromptIds.add(key)
      result.push({ id: item.key, promptId: key, items: [item], isContinuation })
    }
  }
  return result
})

// Per-turn collapse (default expanded), keyed by Group.id — local/display-only,
// same as `collapseEnabled` above, never sent to the server.
const collapsedGroups = ref<Set<string>>(new Set())

function isGroupCollapsed(groupId: string): boolean {
  return collapsedGroups.value.has(groupId)
}

function toggleGroup(groupId: string) {
  const next = new Set(collapsedGroups.value)
  if (next.has(groupId)) {
    next.delete(groupId)
  } else {
    next.add(groupId)
  }
  collapsedGroups.value = next
}

function turnFor(promptId: string | null) {
  if (promptId === null) return null
  return store.turns.find((t) => t.prompt_id === promptId) ?? null
}

/** See the module doc: a local proxy for ToolCall.correlation, not the server value. */
function correlationFor(item: TimelineItem): Correlation | null {
  if (item.decision === null) return null
  const isAuthoritative = item.events.some((e) => e.source === 'otel_log' && e.kind === 'tool.decision')
  return isAuthoritative ? 'exact' : 'heuristic'
}

const availableKinds = computed<Kind[]>(() => {
  const seen = new Set<Kind>()
  for (const event of store.timelineItems) seen.add(event.kind)
  return Array.from(seen).sort()
})

function isKindActive(kind: Kind): boolean {
  return store.kinds.length === 0 || store.kinds.includes(kind)
}

function toggleKind(kind: Kind) {
  // An empty filter means "all" (server semantics — see store.loadTimeline).
  // Clicking an inactive chip while "all" is selected narrows to just that
  // kind rather than toggling it into a one-item exclusion list.
  const active = store.kinds.length === 0 ? new Set<Kind>() : new Set(store.kinds)
  if (active.has(kind)) {
    active.delete(kind)
  } else {
    active.add(kind)
  }
  store.setTimelineFilters({ kinds: Array.from(active) })
  void store.loadTimeline({ reset: true })
}

function clearKindFilter() {
  store.setTimelineFilters({ kinds: [] })
  void store.loadTimeline({ reset: true })
}

function retry() {
  void store.loadTimeline({ reset: true })
}

function loadMore() {
  void store.loadMoreTimeline()
}

// --- detail sheet ---------------------------------------------------------

const selectedEventRef = ref<string | null>(null)
const sheetOpen = ref(false)

function openDetail(eventRef: string) {
  selectedEventRef.value = eventRef
  sheetOpen.value = true
}

function onSheetOpenChange(value: boolean) {
  sheetOpen.value = value
}

// --- infinite scroll --------------------------------------------------------

const sentinelRef = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | null = null

function setupObserver() {
  if (typeof IntersectionObserver === 'undefined' || !sentinelRef.value) return
  observer = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting) && store.timelineHasMore && !store.timelineLoading) {
      void store.loadMoreTimeline()
    }
  })
  observer.observe(sentinelRef.value)
}

onMounted(() => {
  void store.loadTurns()
  void store.loadTimeline()
  setupObserver()
})

onBeforeUnmount(() => {
  observer?.disconnect()
})

watch(sentinelRef, (el, prev) => {
  if (prev) observer?.disconnect()
  if (el) setupObserver()
})

/**
 * `agentId` is the one timeline filter this component does not itself own:
 * P4-05's subagent tree sets it (a node click routes to
 * `?tab=timeline&agent_id=…`, which SessionDetailView's query watcher applies
 * to the store). `setTimelineFilters` is a pure state setter and the kind
 * chips above pair every call with their own `loadTimeline`, so without this
 * watcher an agent filter would land in the store while the rendered events
 * silently stayed unfiltered. The store's `loadTimeline` does send
 * `agent_id`, so each layer was individually correct and only the seam
 * between them was missing. `reset: true` because a cursor obtained from the
 * unfiltered query cannot be continued against a filtered one.
 */
watch(
  () => store.agentId,
  () => {
    void store.loadTimeline({ reset: true })
  },
)
</script>

<template>
  <div
    class="flex h-full flex-col gap-3"
    data-testid="timeline"
  >
    <div class="flex flex-wrap items-center gap-2 px-3 pt-2">
      <Badge
        variant="secondary"
        class="cursor-pointer"
        data-testid="timeline-kind-chip-all"
        :class="{ 'ring-primary ring-2': store.kinds.length === 0 }"
        @click="clearKindFilter"
      >
        All kinds
      </Badge>
      <Badge
        v-for="kind in availableKinds"
        :key="kind"
        variant="outline"
        class="cursor-pointer"
        :class="{ 'ring-primary ring-2': isKindActive(kind) && store.kinds.length > 0 }"
        :data-testid="`timeline-kind-chip-${kind}`"
        @click="toggleKind(kind)"
      >
        {{ eventKindMeta(kind).label }}
      </Badge>

      <Badge
        v-if="store.agentId"
        variant="outline"
        class="gap-1 pr-1"
        data-testid="timeline-agent-filter-chip"
      >
        Agent:
        <span
          class="max-w-32 truncate font-mono"
          :title="store.agentId"
        >{{ store.agentId }}</span>
        <button
          type="button"
          class="hover:bg-muted shrink-0 rounded-full p-0.5"
          data-testid="timeline-agent-filter-clear"
          aria-label="Clear agent filter"
          @click="emit('clear-agent-filter')"
        >
          <X class="size-3" />
        </button>
      </Badge>

      <!--
        Labels the per-row duration bar's scale (round-4 critic ask: "label
        the scale") — only rendered once something has a measured duration
        to scale against, so a session with none doesn't show a legend for a
        feature that isn't drawing anything.
      -->
      <span
        v-if="maxDurationMs > 0"
        class="text-muted-foreground ml-auto text-xs"
        data-testid="timeline-duration-scale-note"
      >Duration bar: log scale, max {{ formatDuration(maxDurationMs) }}</span>

      <label
        class="flex items-center gap-2 text-xs"
        :class="{ 'ml-auto': maxDurationMs === 0 }"
      >
        <span class="text-muted-foreground">Collapse</span>
        <Switch
          :model-value="collapseEnabled"
          data-testid="timeline-collapse-toggle"
          @update:model-value="collapseEnabled = $event"
        />
      </label>
    </div>

    <!--
      Two columns on wide viewports: the event list (left, scrolls on its
      own) and the inspector (right — `EventInspector` decides whether that
      is a persistent panel or an overlay sheet). Both mount unconditionally
      of the list's own load/error/empty state — the inspector's "nothing
      selected yet" placeholder is a real, useful state, not something to
      hide while the list is busy.
    -->
    <div class="flex min-h-0 flex-1 gap-3">
      <ErrorState
        v-if="store.timelineError"
        class="flex-1"
        :error="store.timelineError"
        @retry="retry"
      />

      <template v-else-if="store.timelineLoading && items.length === 0">
        <div class="flex flex-1 flex-col gap-2 px-3">
          <Skeleton
            v-for="n in 6"
            :key="n"
            class="h-10 w-full"
          />
        </div>
      </template>

      <EmptyState
        v-else-if="items.length === 0"
        class="flex-1"
        title="No events match the current filters"
      />

      <div
        v-else
        class="min-w-0 flex-1 overflow-auto"
      >
        <TimelineGroup
          v-for="group in groups"
          :key="group.id"
          :prompt-id="group.promptId"
          :turn="turnFor(group.promptId)"
          :items="group.items"
          :correlation-for="correlationFor"
          :collapsed="isGroupCollapsed(group.id)"
          :is-continuation="group.isContinuation"
          :selected-event-ref="selectedEventRef"
          :origin-ts="originTs"
          :max-duration-ms="maxDurationMs"
          @toggle-collapse="toggleGroup(group.id)"
          @open="openDetail"
        />

        <div
          ref="sentinelRef"
          class="h-1"
        />

        <div
          v-if="store.timelineHasMore"
          class="flex justify-center p-3"
        >
          <Button
            variant="outline"
            size="sm"
            :disabled="store.timelineLoading"
            data-testid="timeline-load-more"
            @click="loadMore"
          >
            {{ store.timelineLoading ? 'Loading…' : 'Load more' }}
          </Button>
        </div>
      </div>

      <EventInspector
        :event-ref="selectedEventRef"
        :open="sheetOpen"
        @update:open="onSheetOpenChange"
      />
    </div>
  </div>
</template>
