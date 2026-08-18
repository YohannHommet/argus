<script lang="ts">
/**
 * Recursion guard (design note, PLAN P4-05): the server caps synthetic
 * subagent-tree depth at 16 (a malformed `parent_agent_id` cycle cannot
 * hang the server's own query). This client-side cap is set higher than
 * that — 24 — so it never fires on any tree the server actually intends
 * to produce, while still bounding worst-case recursion (and DOM size) to
 * something well short of a stack overflow or a hung tab if a payload
 * ever reaches the browser without having gone through that server-side
 * cap (a bug, a different server build, a hand-crafted response in a
 * test). Past the cap, recursion stops and a visible marker is rendered
 * instead of silently truncating the tree.
 *
 * Lives in a plain (non-setup) `<script>` block because `<script setup>`
 * cannot itself export a runtime value — only the `<script setup>` block
 * below needs it, but this is where a co-located export is allowed.
 */
export const MAX_RENDER_DEPTH = 24
</script>

<script setup lang="ts">
/**
 * One row of the subagent tree (PLAN P4-05), rendered recursively for
 * `node.children`. Two independent depth numbers are in play here and
 * must not be confused: `node.depth` is the server's own field (shown as
 * a badge, informational only) and `renderDepth` is this component's own
 * recursion counter, incremented on every nested instance regardless of
 * what the payload's `depth` field claims. `renderDepth` is what the
 * recursion guard below checks — a malformed/cyclic payload could send a
 * `depth` field that lies, but it cannot lie about how many
 * `<SubagentNode>` instances Vue has actually created to get here.
 */
import { computed, ref } from 'vue'
import { ChevronDown, ChevronRight, TriangleAlert } from '@lucide/vue'

import NullValue from '@/components/common/NullValue.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Badge } from '@/components/ui/badge'
import type { components } from '@/api/schema'
import { formatAbsoluteTime, formatCost, formatCount, formatDuration } from '@/lib/format'
import { NO_HOOK_COVERAGE, NO_PER_AGENT_COST } from '@/lib/nullReasons'

export type SubagentNodeData = components['schemas']['SubagentNode']

interface Props {
  node: SubagentNodeData
  /** This component's own recursion depth (0 for a tree root). Distinct from `node.depth` — see file doc comment. */
  renderDepth?: number
  /** Precomputed max duration (ms) across the whole visible tree, for sizing the duration bar. `null`/0 renders every bar as an indeterminate empty track. */
  maxDurationMs?: number | null
  /** Tooltip text for the (always-null, SPEC §1.9) cost column. Prefer `cost_attribution.note` when the caller has one. */
  costNote?: string | null
}

const props = withDefaults(defineProps<Props>(), {
  renderDepth: 0,
  maxDurationMs: null,
  costNote: null,
})

const emit = defineEmits<{
  /** Emitted on a row click (this node or any descendant, re-emitted upward) with the clicked node's `agent_id`. The host wires this to a Timeline-tab filter navigation. */
  'select-agent': [agentId: string]
}>()

const expanded = ref(true)
const isRoot = computed(() => props.node.parent_agent_id === null)
const hasChildren = computed(() => props.node.children.length > 0)
const depthLimitReached = computed(() => props.renderDepth >= MAX_RENDER_DEPTH)

const durationMs = computed(() => {
  if (!props.node.started_at || !props.node.ended_at) return null
  const ms = new Date(props.node.ended_at).getTime() - new Date(props.node.started_at).getTime()
  return Number.isFinite(ms) ? ms : null
})

/** Width of the duration bar's filled portion, 0-100. Only meaningful when `durationMs` is non-null — the track itself renders indeterminate otherwise. */
const durationBarPercent = computed(() => {
  if (durationMs.value === null || !props.maxDurationMs || props.maxDurationMs <= 0) return 0
  return Math.min(100, Math.max(2, (durationMs.value / props.maxDurationMs) * 100))
})

const effectiveCostNote = computed(() => props.costNote ?? NO_PER_AGENT_COST)

function toggle(): void {
  if (hasChildren.value) expanded.value = !expanded.value
}

function onRowActivate(): void {
  emit('select-agent', props.node.agent_id)
}

function onChildSelect(agentId: string): void {
  emit('select-agent', agentId)
}
</script>

<template>
  <li
    class="list-none"
    data-testid="subagent-node"
    :data-agent-id="node.agent_id"
  >
    <div
      class="border-border/60 group flex items-center gap-2 rounded-md border-l-2 py-1.5 pr-2 hover:bg-muted/40"
      :style="{ paddingLeft: `${0.5 + renderDepth * 1.25}rem` }"
      role="button"
      tabindex="0"
      data-testid="subagent-node-row"
      @click="onRowActivate"
      @keydown.enter="onRowActivate"
    >
      <button
        v-if="hasChildren"
        type="button"
        class="text-muted-foreground hover:text-foreground shrink-0"
        data-testid="subagent-node-toggle"
        :aria-expanded="expanded"
        @click.stop="toggle"
      >
        <ChevronDown
          v-if="expanded"
          class="size-4"
        />
        <ChevronRight
          v-else
          class="size-4"
        />
      </button>
      <span
        v-else
        class="inline-block size-4 shrink-0"
      />

      <Badge
        v-if="isRoot"
        variant="default"
        data-testid="subagent-node-main-badge"
      >
        Main agent
      </Badge>
      <Badge variant="outline">
        <RawValue
          :value="node.agent_type"
          kind="agent_type"
        />
      </Badge>

      <Badge
        variant="secondary"
        data-testid="subagent-node-status"
      >
        <RawValue
          :value="node.status"
          kind="status"
        />
      </Badge>

      <span class="text-muted-foreground text-xs">
        tools:
        <NullValue
          v-if="node.tool_call_count === null"
          :reason="NO_HOOK_COVERAGE"
        />
        <span
          v-else
          data-testid="subagent-node-tool-count"
        >{{ formatCount(node.tool_call_count) }}</span>
      </span>

      <span class="text-muted-foreground flex min-w-24 items-center gap-1.5 text-xs">
        <span
          class="bg-muted relative h-1.5 w-16 overflow-hidden rounded-full"
          :class="durationMs === null ? 'border-border border border-dashed' : ''"
          data-testid="subagent-node-duration-track"
        >
          <span
            v-if="durationMs !== null"
            class="bg-vendor-claude absolute inset-y-0 left-0 rounded-full"
            :style="{ width: `${durationBarPercent}%` }"
          />
        </span>
        <time :title="formatAbsoluteTime(node.started_at)">{{ formatDuration(durationMs) }}</time>
      </span>

      <span
        class="text-cost ml-auto text-xs font-medium tabular-nums"
        data-testid="subagent-node-cost"
      >
        <NullValue
          v-if="node.cost_usd === null"
          :reason="effectiveCostNote"
        />
        <template v-else>
          {{ formatCost(node.cost_usd) }}
        </template>
      </span>
    </div>

    <ul
      v-if="hasChildren && expanded && !depthLimitReached"
      class="border-border/40 ml-4 border-l pl-0"
    >
      <SubagentNode
        v-for="child in node.children"
        :key="child.agent_id"
        :node="child"
        :render-depth="renderDepth + 1"
        :max-duration-ms="maxDurationMs"
        :cost-note="costNote"
        @select-agent="onChildSelect"
      />
    </ul>

    <p
      v-else-if="hasChildren && expanded && depthLimitReached"
      class="text-warn ml-6 flex items-center gap-1.5 py-1 text-xs"
      data-testid="subagent-node-depth-limit"
    >
      <TriangleAlert class="size-3.5" />
      Depth limit reached ({{ MAX_RENDER_DEPTH }}) — remaining nodes not rendered.
    </p>
  </li>
</template>
