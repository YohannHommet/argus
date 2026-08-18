<script setup lang="ts">
/**
 * Root of the Subagents tab (PLAN P4-05). Owns the loading/error/empty
 * states around the tree, computes the shared duration-bar scale once for
 * the whole visible tree (rather than each `SubagentNode` guessing its
 * own), and turns a node click into the cross-tab navigation SPEC/PLAN
 * P4-05 describes: `?tab=timeline&agent_id=…`. The store
 * (`useSessionDetailStore`) already watches `route.query.agent_id` and
 * applies it via `setTimelineFilters` (see `SessionDetailView.vue`), so
 * this component only needs to write the query — it does not touch the
 * store directly.
 */
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { Skeleton } from '@/components/ui/skeleton'
import SubagentNode from './SubagentNode.vue'
import type { SubagentNodeData } from './SubagentNode.vue'
import type { ApiError } from '@/api/errors'
import { formatDuration } from '@/lib/format'
import { useSessionDetailStore } from '@/stores/sessionDetail'

interface Props {
  nodes?: SubagentNodeData[]
  loading?: boolean
  error?: ApiError | Error | null
  /** `cost_attribution.note`, when the caller has one — passed through to every node's cost tooltip in place of the generic constant. */
  costNote?: string | null
}

const props = withDefaults(defineProps<Props>(), {
  nodes: () => [],
  loading: false,
  error: null,
  costNote: null,
})

const emit = defineEmits<{
  retry: []
  /**
   * Re-emitted after this component has already performed the
   * `?tab=timeline&agent_id=…` navigation itself — informational, for a
   * host that wants to react to the selection without re-deriving it from
   * the route (e.g. analytics).
   */
  'select-agent': [agentId: string]
}>()

const route = useRoute()
const router = useRouter()
const store = useSessionDetailStore()

const isEmpty = computed(() => !props.loading && !props.error && props.nodes.length === 0)

function walkMaxDuration(nodes: SubagentNodeData[]): number {
  let max = 0
  for (const node of nodes) {
    if (node.started_at && node.ended_at) {
      const ms = new Date(node.ended_at).getTime() - new Date(node.started_at).getTime()
      if (Number.isFinite(ms) && ms > max) max = ms
    }
    const childMax = walkMaxDuration(node.children)
    if (childMax > max) max = childMax
  }
  return max
}

const maxDurationMs = computed(() => walkMaxDuration(props.nodes))

/**
 * Round-5 critic gap: "the Subagents duration scale reads '0-4ms' —
 * meaningless at these magnitudes". A bar chart (and its legend) implies the
 * differences it's drawing are worth comparing; at sub-second magnitudes
 * across a handful of subagent calls, a few milliseconds of spread is
 * scheduler/clock-resolution noise, not a real "this one took visibly
 * longer" signal — the bar would render near-identical widths for values
 * that differ 4x, which is a worse read than no bar. 1s is the same
 * order-of-magnitude floor `formatDuration` switches from decimal seconds
 * to whole seconds at, i.e. the point where a duration starts being a
 * human-meaningful span rather than noise. Below it, every node still shows
 * its own honest duration text (never dropped) — only the comparative
 * bar/legend, which stops being honest at this scale, goes away.
 */
const hasMeaningfulDurationSpread = computed(() => maxDurationMs.value >= 1000)

/**
 * PLAN P4-05: "clicking a node navigates to `?tab=timeline&agent_id=…`
 * and the store applies the filter". Both halves are done directly here
 * rather than relying solely on `SessionDetailView.vue`'s own
 * `route.query.agent_id` watcher: the URL is updated (`{ query }` alone
 * resolves relative to `path: '/'`, not the current route, so the current
 * route's name/params are carried forward explicitly — matching
 * `SessionDetailView.vue`'s own `setTab`) so back/forward and a reload
 * keep the filter, *and* the store's filter is set synchronously so this
 * component's own behaviour is fully testable without mounting the view
 * that owns the tab wiring. The view's watcher re-applies the same value
 * when the URL change lands — a harmless no-op, not a race.
 */
function onSelectAgent(agentId: string): void {
  void router.replace({
    name: route.name ?? undefined,
    params: route.params,
    query: { ...route.query, tab: 'timeline', agent_id: agentId },
  })
  store.setTimelineFilters({ agentId })
  emit('select-agent', agentId)
}
</script>

<template>
  <div data-testid="subagent-tree">
    <ErrorState
      v-if="error"
      :error="error"
      title="Couldn't load subagents"
      @retry="emit('retry')"
    />

    <div
      v-else-if="loading"
      class="flex flex-col gap-2"
      data-testid="subagent-tree-loading"
    >
      <Skeleton class="h-8 w-full" />
      <Skeleton class="h-8 w-full" />
      <Skeleton class="h-8 w-full" />
    </div>

    <EmptyState
      v-else-if="isEmpty"
      title="No subagents recorded"
      description="This session made no subagent calls, or hook coverage was unavailable for this run."
    />

    <template v-else>
      <p
        v-if="hasMeaningfulDurationSpread"
        class="text-muted-foreground mb-1 flex justify-end text-[0.6875rem]"
        data-testid="subagent-tree-duration-scale"
      >
        Duration scale: 0 – {{ formatDuration(maxDurationMs) }}
      </p>

      <ul class="flex flex-col gap-0.5">
        <SubagentNode
          v-for="root in nodes"
          :key="root.agent_id"
          :node="root"
          :render-depth="0"
          :max-duration-ms="maxDurationMs"
          :show-duration-bar="hasMeaningfulDurationSpread"
          :cost-note="costNote"
          @select-agent="onSelectAgent"
        />
      </ul>
    </template>
  </div>
</template>
