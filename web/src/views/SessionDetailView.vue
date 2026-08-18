<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import ErrorState from '@/components/common/ErrorState.vue'
import SessionKpiStrip from '@/components/session/SessionKpiStrip.vue'
import CostAttributionCard from '@/components/subagent/CostAttributionCard.vue'
import SubagentTree from '@/components/subagent/SubagentTree.vue'
import Timeline from '@/components/timeline/Timeline.vue'
import ToolCallTable from '@/components/tools/ToolCallTable.vue'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
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

// Tab state lives in the URL query (?tab=…), not a nested route, so a
// reload re-derives it from `route.query` on mount instead of resetting to
// the default — PLAN P4-03's "tab state survives reload" AC.
const activeTab = ref<Tab>(tabFromRoute())

function setTab(next: string | number): void {
  const tab = isTab(next) ? next : DEFAULT_TAB
  activeTab.value = tab
  // `{ query }` alone is resolved relative to `path: '/'`, not the current
  // route, so the current route/params must be given explicitly — omitting
  // them would navigate away from `/sessions/:id` entirely.
  void router.replace({ name: route.name ?? undefined, params: route.params, query: { ...route.query, tab } })
}

// Keeps activeTab in sync with browser back/forward and any direct edit of
// the URL — router.replace() above is this view's own writes; this watcher
// is what makes an *external* query change (not caused by setTab) take
// effect too.
watch(
  () => route.query.tab,
  () => {
    activeTab.value = tabFromRoute()
  },
)

// PLAN P4-05: "a node click navigates to ?tab=timeline&agent_id=…" — the
// store's timeline filter must pick that up. `null` clears the filter when
// the query param is absent, e.g. navigating away from a filtered link.
watch(
  () => route.query.agent_id,
  (raw) => {
    const value = Array.isArray(raw) ? raw[0] : raw
    store.setTimelineFilters({ agentId: value ?? null })
  },
  { immediate: true },
)

// Fetch the session whenever the route's :id changes. Registered before the
// tab-activation watcher below so `store.currentId` is already pointed at
// this session (loadSession sets it synchronously, before its first await)
// by the time that watcher's lazy loadSubagents/loadToolCalls calls run.
watch(
  () => props.id,
  (id) => {
    if (id) void store.loadSession(id)
  },
  { immediate: true },
)

// Lazy per tab (SPEC/PLAN P4-03 design note): Subagents/Tools panels are
// other tickets' (P4-05/P4-06), but this view owns tab activation, so it is
// what triggers each panel's first fetch — never both at once, never the
// Timeline tab's own turns/tool-calls unless a later ticket asks for them.
watch(
  activeTab,
  (tab) => {
    // Round-6 critic gap ("tool-breakdown" per subagent node): the tree's
    // tool_call_count is a total with no per-tool detail, but ToolCall rows
    // already carry a real (hook-sourced) agent_id — SubagentTree can derive
    // a genuine per-node tool-name breakdown from the same toolCalls the
    // Tools tab uses, provided they're loaded. `loadToolCalls` is
    // load-once/cached (see the store), so activating the Tools tab later
    // does not refetch.
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
 * Round-6 critic gap: the Timeline tab's agent filter (set by a Subagents
 * node click, see SubagentTree.vue) had no visible chip and no way to clear
 * it. `Timeline.vue` deliberately does not own this filter (its own doc
 * comment: routing is this view's job), so it only emits; clearing the URL
 * query here is what the existing `route.query.agent_id` watcher above
 * turns back into `store.setTimelineFilters({ agentId: null })`.
 */
function clearAgentFilter(): void {
  const query = { ...route.query }
  delete query.agent_id
  void router.replace({ name: route.name ?? undefined, params: route.params, query })
}

// The screenshot harness blocks on this. "Ready" = the initial session
// fetch has settled one way or another (data, or a definitive error) —
// never mid-fetch, per useCaptureReady's own contract.
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
      </div>

      <p class="text-muted-foreground text-xs">
        {{ store.session!.vendor }} · {{ store.session!.id }} · {{ store.session!.cwd }}
      </p>

      <!--
        Started/last-event and decision_summary.exact_share (SPEC §4.3 — the
        fraction of this session's accept/reject decisions whose correlation
        is exact rather than heuristic) share one compact meta line instead
        of three stacked paragraphs, so the header stays a caption for the
        tabs below rather than a block competing with them for height.
      -->
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
        <!--
          raw_events_expired (SPEC §1.x, retention): the raw event log was
          pruned, but the session's own aggregates above are still real.
          That is a different fact from "this session simply had no
          events", so it gets its own notice rather than falling into
          whatever empty-timeline placeholder P4-04 renders.
        -->
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
        <!--
          The tree is this tab's primary content (it's the structural view —
          the differentiator the cost table can't show); the cost table is
          reference material, so it's capped and scrollable on its own
          (CostAttributionCard's `max-h-48`) rather than growing to whatever
          height its own row count wants, which previously let it dwarf the
          tree. The tree itself sizes to its actual content — a session with
          only a couple of subagents must not reserve a fixed tall slot it
          doesn't use (round-3 critic gap: "~300px dead canvas above the
          cost table" from an earlier `min-h-[22rem]` here).
        -->
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
          <CostAttributionCard
            class="shrink-0"
            :data="store.costAttribution"
            :loading="store.subagentsLoading"
            :error="store.subagentsError"
            @retry="store.loadSubagents({ force: true })"
          />
        </div>
      </TabsContent>

      <TabsContent value="tools">
        <!--
          `show-session="false"`: this is one session's tool calls, so a
          session column would repeat the same id on every row. The
          cross-session /tools view passes true.
        -->
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
