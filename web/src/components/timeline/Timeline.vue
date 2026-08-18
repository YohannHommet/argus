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

import { type TimelineItem, collapseEvents } from '@/lib/collapseEvents'
import { eventKindMeta } from '@/lib/eventKinds'
import type { Kind } from '@/lib/eventKinds'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import type { Correlation } from './DecisionBadge.vue'
import EventDetailSheet from './EventDetailSheet.vue'
import TimelineGroup from './TimelineGroup.vue'

const store = useSessionDetailStore()

const collapseEnabled = ref(true)

const items = computed<TimelineItem[]>(() => collapseEvents(store.timelineItems, { collapse: collapseEnabled.value }))

interface Group {
  /** Stable across re-renders: the group's anchor (first) item's key — unique even when two runs share the same `promptId` (see module doc). */
  id: string
  promptId: string | null
  items: TimelineItem[]
}

/** Contiguous runs of the same `prompt_id` — see the module doc for why this is a run split, not a global bucket keyed by prompt_id. */
const groups = computed<Group[]>(() => {
  const result: Group[] = []
  for (const item of items.value) {
    const key = item.prompt_id
    const current = result[result.length - 1]
    if (current && current.promptId === key) {
      current.items.push(item)
    } else {
      result.push({ id: item.key, promptId: key, items: [item] })
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

      <label class="ml-auto flex items-center gap-2 text-xs">
        <span class="text-muted-foreground">Collapse</span>
        <Switch
          :model-value="collapseEnabled"
          data-testid="timeline-collapse-toggle"
          @update:model-value="collapseEnabled = $event"
        />
      </label>
    </div>

    <ErrorState
      v-if="store.timelineError"
      :error="store.timelineError"
      @retry="retry"
    />

    <template v-else-if="store.timelineLoading && items.length === 0">
      <div class="flex flex-col gap-2 px-3">
        <Skeleton
          v-for="n in 6"
          :key="n"
          class="h-10 w-full"
        />
      </div>
    </template>

    <EmptyState
      v-else-if="items.length === 0"
      title="No events match the current filters"
    />

    <div
      v-else
      class="flex-1 overflow-auto"
    >
      <TimelineGroup
        v-for="group in groups"
        :key="group.id"
        :prompt-id="group.promptId"
        :turn="turnFor(group.promptId)"
        :items="group.items"
        :correlation-for="correlationFor"
        :collapsed="isGroupCollapsed(group.id)"
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

    <EventDetailSheet
      :event-ref="selectedEventRef"
      :open="sheetOpen"
      @update:open="onSheetOpenChange"
    />
  </div>
</template>
