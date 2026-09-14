<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import ErrorState from '@/components/common/ErrorState.vue'
import SessionKpiStrip from '@/components/session/SessionKpiStrip.vue'
import CostAttributionCard from '@/components/subagent/CostAttributionCard.vue'
import SubagentTree from '@/components/subagent/SubagentTree.vue'
import Timeline from '@/components/timeline/Timeline.vue'
import ToolCallTable from '@/components/tools/ToolCallTable.vue'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useCaptureReady } from '@/composables/useCaptureReady'
import { formatAbsoluteTime, formatPercent, formatRelativeTime } from '@/lib/format'
import { useSessionDetailStore } from '@/stores/sessionDetail'

const props = defineProps<{ id?: string }>()

const route = useRoute()
const router = useRouter()
const store = useSessionDetailStore()

const TABS = ['timeline', 'subagents', 'tools'] as const
type Tab = (typeof TABS)[number]
const DEFAULT_TAB: Tab = 'timeline'

function isTab(value: unknown): value is Tab {
  return typeof value === 'string' && (TABS as readonly string[]).includes(value)
}

function tabFromRoute(): Tab {
  const raw = route.query.tab
  const value = Array.isArray(raw) ? raw[0] : raw
  return isTab(value) ? value : DEFAULT_TAB
}

// Tab state lives in the URL query (?tab=…), not a nested route, so a reload re-derives it from
// `route.query` on mount instead of resetting to the default ("survives reload").
const activeTab = ref<Tab>(tabFromRoute())

function setTab(next: string | number): void {
  const tab = isTab(next) ? next : DEFAULT_TAB
  activeTab.value = tab
  // `{ query }` alone is resolved relative to `path: '/'`, not the current route, so the current
  // route/params must be given explicitly — omitting them would navigate away from `/sessions/:id`.
  void router.replace({ name: route.name ?? undefined, params: route.params, query: { ...route.query, tab } })
}

// Keeps activeTab in sync with browser back/forward and direct URL edits — router.replace() above
// is this view's own writes; this watcher is what applies an *external* query change too.
watch(
  () => route.query.tab,
  () => {
    activeTab.value = tabFromRoute()
  },
)

// A node click navigates to ?tab=timeline&agent_id=…, and the store's timeline filter must pick that up; `null` clears it when the param is absent (e.g. leaving a filtered link).
watch(
  () => route.query.agent_id,
  (raw) => {
    const value = Array.isArray(raw) ? raw[0] : raw
    store.setTimelineFilters({ agentId: value ?? null })
  },
  { immediate: true },
)

// Registered before the tab-activation watcher so `store.currentId` already points here
// (`loadSession` sets it synchronously, before its first await) by the time that watcher's lazy loads run.
watch(
  () => props.id,
  (id) => {
    if (!id) return
    void store.loadSession(id)
    // `startLive` is idempotent per id and tears down the *previous* id's subscription itself. This
    // view never remounts across an id change, so this watcher, not onMounted, migrates the live
    // subscription across a same-view navigation instead of leaking it.
    store.startLive(id)
    applyLiveDefault(id)
  },
  { immediate: true },
)

/**
 * "Follow session jumps to detail in live mode": honours `?live=1`
 * literally. The follow link's exact param name isn't visible from here, so
 * the *absence* of the param still defaults to live for an `active` session
 * (opening an actively-generating session must show new rows with no manual
 * refresh) — the feature works whether or not the follow link ends up using
 * this exact name.
 */
function applyLiveDefault(id: string): void {
  const raw = route.query.live
  const liveParam = Array.isArray(raw) ? raw[0] : raw
  if (liveParam === '1') {
    store.setLiveEnabled(true)
    return
  }
  if (liveParam === '0') {
    store.setLiveEnabled(false)
    return
  }
  if (store.session && store.session.id === id) {
    store.setLiveEnabled(store.session.status === 'active')
    return
  }
  // First navigation to this id: the session hasn't loaded yet. Waits for it once, then stops —
  // not persistent, since a *later* live frame must never silently re-override a toggle flipped by hand.
  const stopWatchingSession = watch(
    () => store.session,
    (session) => {
      if (!session || session.id !== id) return
      store.setLiveEnabled(session.status === 'active')
      stopWatchingSession()
    },
  )
}

onBeforeUnmount(() => {
  store.stopLive()
})

// Lazy per tab: this view owns tab activation, so it triggers each panel's first fetch.
watch(
  activeTab,
  (tab) => {
    // The tree also needs `toolCalls` loaded — it derives a per-node tool-name breakdown from the
    // same hook-sourced `agent_id` the Tools tab uses. `loadToolCalls` is load-once/cached, so
    // activating the Tools tab later does not refetch.
    if (tab === 'subagents') {
      void store.loadSubagents()
      void store.loadToolCalls()
    }
    if (tab === 'tools') void store.loadToolCalls()
  },
  { immediate: true },
)

function retry(): void {
  if (props.id) void store.loadSession(props.id)
}

/**
 * `Timeline.vue` doesn't own its agent filter (routing is this view's job), so it only emits;
 * clearing the URL query here is what the `route.query.agent_id` watcher above turns back into
 * `store.setTimelineFilters({ agentId: null })`.
 */
function clearAgentFilter(): void {
  const query = { ...route.query }
  delete query.agent_id
  void router.replace({ name: route.name ?? undefined, params: route.params, query })
}

// The screenshot harness blocks on this. "Ready" = the initial session fetch has settled one way or
// another (data, or a definitive error) — never mid-fetch, per useCaptureReady's own contract.
useCaptureReady(() => !store.loading && (store.session !== null || store.error !== null))

const hasSessionYet = computed(() => store.session !== null)
</script>

<template>
  <section
    v-if="store.error && !hasSessionYet"
    class="flex flex-col gap-4"
  >
    <ErrorState
      :error="store.error"
      title="Couldn't load this session"
      @retry="retry"
    />
  </section>

  <section
    v-else-if="!hasSessionYet"
    class="flex flex-col gap-4"
    data-testid="session-detail-loading"
  >
    <Skeleton class="h-8 w-64" />
    <Skeleton class="h-24 w-full" />
    <Skeleton class="h-64 w-full" />
  </section>

  <section
    v-else
    class="flex flex-col gap-4"
  >
    <header class="flex flex-col gap-1">
      <div class="flex flex-wrap items-center gap-2">
        <h1 class="text-xl font-semibold">
          {{ store.session!.project }}
        </h1>
        <Badge variant="secondary">
          {{ store.session!.status }}
        </Badge>
        <Badge
          v-if="store.session!.partial"
          variant="outline"
          data-testid="partial-badge"
        >
          Partial — no session.start seen
        </Badge>

        <!-- "off" stops the timeline appending new rows but never closes the subscription — see sessionDetail.ts's liveEnabled doc for why this isn't liveStore.pause(). -->
        <label class="ml-auto flex items-center gap-2 text-xs">
          <span
            class="text-muted-foreground"
            role="status"
          >{{ store.liveEnabled ? 'Live' : 'Live (paused)' }}</span>
          <Switch
            :model-value="store.liveEnabled"
            data-testid="live-toggle"
            aria-label="Toggle live timeline updates"
            @update:model-value="store.setLiveEnabled"
          />
        </label>
      </div>

      <p class="text-muted-foreground text-xs">
        {{ store.session!.vendor }} · {{ store.session!.id }} · {{ store.session!.cwd }}
      </p>

      <!-- Started/last-event and decision_summary.exact_share share one compact meta line instead of three stacked paragraphs, so the header stays a caption. -->
      <p class="text-muted-foreground text-xs">
        Started
        <time :title="formatAbsoluteTime(store.session!.started_at)">
          {{ formatRelativeTime(store.session!.started_at) }}
        </time>
        · Last event
        <time :title="formatAbsoluteTime(store.session!.last_event_at)">
          {{ formatRelativeTime(store.session!.last_event_at) }}
        </time>
        ·
        <span data-testid="decision-confidence">Decision confidence: {{ formatPercent(store.session!.decision_summary.exact_share) }} exact</span>
      </p>
    </header>

    <SessionKpiStrip :session="store.session" />

    <Tabs
      :model-value="activeTab"
      @update:model-value="setTab"
    >
      <TabsList>
        <TabsTrigger value="timeline">
          Timeline
        </TabsTrigger>
        <TabsTrigger value="subagents">
          Subagents
        </TabsTrigger>
        <TabsTrigger value="tools">
          Tools
        </TabsTrigger>
      </TabsList>

      <TabsContent value="timeline">
        <!-- raw_events_expired: the raw log was pruned, but the aggregates above are still real — a different fact from "no events", so it gets its own notice. -->
        <div
          v-if="store.session!.raw_events_expired"
          data-testid="raw-events-expired-notice"
          class="border-border text-muted-foreground rounded-lg border border-dashed px-6 py-12 text-center text-sm"
        >
          <p class="text-foreground font-medium">
            Raw events expired
          </p>
          <p class="mt-1">
            Retention has pruned this session's raw event log. Turn, tool-call and cost
            aggregates above were computed before expiry and remain accurate.
          </p>
        </div>
        <Timeline
          v-else
          @clear-agent-filter="clearAgentFilter"
        />
      </TabsContent>

      <TabsContent value="subagents">
        <!-- The tree is primary content and sizes to it; the cost table is reference material, capped/scrollable on its own (CostAttributionCard's max-h-48). -->
        <div class="flex flex-col gap-3">
          <div>
            <SubagentTree
              :nodes="store.subagents"
              :loading="store.subagentsLoading"
              :error="store.subagentsError"
              :cost-note="store.costAttribution?.note ?? null"
              :tool-calls="store.toolCalls"
              @retry="store.loadSubagents({ force: true })"
            />
          </div>
          <!-- estimated-usd/estimated-share come from the *session* projection, not costAttribution: by_query_source is reported-cost-only, so an all-estimated session has nothing to derive an estimate from. -->
          <CostAttributionCard
            class="shrink-0"
            :data="store.costAttribution"
            :loading="store.subagentsLoading"
            :error="store.subagentsError"
            :estimated-usd="store.session?.cost.estimated_usd ?? 0"
            :estimated-share="store.session?.cost.estimated_share ?? 0"
            @retry="store.loadSubagents({ force: true })"
          />
        </div>
      </TabsContent>

      <TabsContent value="tools">
        <!-- show-session="false": this is one session's tool calls, so a session column would repeat the same id on every row; the cross-session /tools view passes true. -->
        <ToolCallTable
          :rows="store.toolCalls"
          :loading="store.toolCallsLoading"
          :error="store.toolCallsError"
          :show-session="false"
          @retry="store.loadToolCalls({ force: true })"
        />
      </TabsContent>
    </Tabs>
  </section>
</template>
