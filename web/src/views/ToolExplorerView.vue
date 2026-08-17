<script setup lang="ts">
/**
 * `/tools` — SPEC §6.2's "decision-provenance drill-down, linked from the
 * analytics decision matrix. The differentiator's dedicated view." Cross-
 * session (`ToolCallTable`'s `show-session`), filtered/paginated by
 * `toolsStore`, reachable both directly and via `DecisionMatrix.vue`'s
 * `filter` event (`{ tool_name, decision_source }`) — Phase-4 exit
 * criterion 5.
 */
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'

import ToolCallTable from '@/components/tools/ToolCallTable.vue'
import type { SortableKey } from '@/components/tools/ToolCallTable.vue'
import RawValue from '@/components/common/RawValue.vue'
import { Button } from '@/components/ui/button'
import { useCaptureReady } from '@/composables/useCaptureReady'
import { useToolsStore } from '@/stores/tools'

const router = useRouter()
const tools = useToolsStore()

// Real data, an empty page, and an error banner are all legitimate first paints; only the initial
// still-in-flight fetch is not (see tools.ts's `initialized`).
useCaptureReady(() => tools.initialized)

const totalLoaded = computed(() => tools.toolCalls.length)

// Client-side-only (no server sort param exists for this endpoint — see tools.ts's doc comment on
// ToolCallFilters). Lives here, not in the store: it's a display concern of the currently-loaded
// page, not fetched state.
const sortKey = ref<SortableKey | null>(null)

function onSortChange(key: SortableKey): void {
  sortKey.value = key
}

function onRetry(): void {
  void tools.refresh()
}

function onLoadMore(): void {
  void tools.loadMore()
}

function onRowClick(row: { session_id: string }): void {
  void router.push({ name: 'session-detail', params: { id: row.session_id } })
}

function clearTool(): void {
  tools.setFilters({ tool: [] })
}

function clearDecisionSource(): void {
  tools.setFilters({ decisionSource: [] })
}

const hasActiveFilters = computed(() => tools.filters.tool.length > 0 || tools.filters.decisionSource.length > 0)

function clearAllFilters(): void {
  tools.clearFilters()
}
</script>

<template>
  <section class="flex flex-col gap-4">
    <div class="flex items-baseline justify-between">
      <div>
        <h1 class="text-2xl font-semibold">
          Tool explorer
        </h1>
        <p class="text-muted-foreground mt-1 text-sm">
          Every tool call across every session, with who decided and how confidently Argus knows it.
        </p>
      </div>
      <p
        v-if="totalLoaded > 0"
        class="text-muted-foreground text-sm"
      >
        {{ totalLoaded }} loaded
      </p>
    </div>

    <!--
      Active-filter chips: the visible half of exit criterion 5's "arrives with the filter applied"
      — a click from DecisionMatrix.vue lands here with tools.filters already populated (parsed from
      the URL on this store's creation), and this is where that fact becomes legible rather than
      silently baked into an unlabeled request.
    -->
    <div
      v-if="hasActiveFilters"
      class="flex flex-wrap items-center gap-2"
      data-testid="active-filters"
    >
      <span class="text-muted-foreground text-xs">Filtered by:</span>
      <Button
        v-for="tool in tools.filters.tool"
        :key="`tool-${tool}`"
        variant="secondary"
        size="sm"
        data-testid="filter-chip-tool"
        @click="clearTool"
      >
        tool: {{ tool }} ✕
      </Button>
      <Button
        v-for="source in tools.filters.decisionSource"
        :key="`source-${source}`"
        variant="secondary"
        size="sm"
        data-testid="filter-chip-decision-source"
        @click="clearDecisionSource"
      >
        decision_source:
        <RawValue
          :value="source"
          kind="decision_source"
        /> ✕
      </Button>
      <Button
        variant="ghost"
        size="sm"
        @click="clearAllFilters"
      >
        Clear all
      </Button>
    </div>

    <ToolCallTable
      :rows="tools.toolCalls"
      :loading="tools.loading"
      :loading-more="tools.loadingMore"
      :error="tools.error"
      :has-more="tools.hasMore"
      :sort="sortKey"
      show-session
      @retry="onRetry"
      @load-more="onLoadMore"
      @sort-change="onSortChange"
      @row-click="onRowClick"
    />
  </section>
</template>
