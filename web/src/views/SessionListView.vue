<script setup lang="ts">
/**
 * `/sessions`. Two independent "there's nothing here" facts have to stay
 * distinct (PLAN.md P4-10's AC): a genuinely empty database — nothing has
 * ever landed, so the fix is telemetry setup — versus these particular
 * filters matching nothing, where the fix is clearing a filter. Conflating
 * them would tell a user with an active filter to go set up telemetry
 * they already have, or tell a user with an empty database to try
 * clearing a filter that was never the problem.
 *
 * `metaStore.hasNoData` is the one signal that distinguishes them: it's
 * derived from `facets.projects.length === 0`, a fact about the whole
 * deployment, independent of whatever this view's own filters currently
 * are (see meta.ts's doc comment on `hasNoData`). If the DB is genuinely
 * empty, `hasNoData` is true regardless of active filters — there is
 * nothing to filter either way, so `SetupCard` is the right answer even
 * with a filter active.
 */
import { computed } from 'vue'
import { useRouter } from 'vue-router'

import SessionFilterBar from '@/components/session/SessionFilterBar.vue'
import SessionTable from '@/components/session/SessionTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import SetupCard from '@/components/common/SetupCard.vue'
import SkeletonTable from '@/components/common/SkeletonTable.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useCaptureReady } from '@/composables/useCaptureReady'
import { useMetaStore } from '@/stores/meta'
import { useSessionsStore } from '@/stores/sessions'

const router = useRouter()
const sessions = useSessionsStore()
const meta = useMetaStore()
void meta.load()

// "Ready to decide which empty state applies" needs meta/facets settled too, not just the
// sessions fetch — otherwise a still-loading `hasNoData` (false until loaded, see meta.ts) would
// briefly render the "no sessions match these filters" branch on a genuinely empty database before
// flipping to SetupCard once meta catches up. Real data, a definitive empty page (either kind), and
// an error banner are all legitimate first paints; that transient wrong-branch flash is not.
const metaSettled = computed(() => (meta.meta !== null && meta.facets !== null) || meta.error !== null)
useCaptureReady(() => sessions.initialized && metaSettled.value)

const totalLoaded = computed(() => sessions.sessions.length)

const isGenuinelyEmpty = computed(() => sessions.sessions.length === 0 && meta.hasNoData)
const isFilteredEmpty = computed(() => sessions.sessions.length === 0 && !meta.hasNoData)

function onSelectSession(id: string): void {
  void router.push({ name: 'session-detail', params: { id } })
}

function onRetry(): void {
  void sessions.refresh()
}

function onLoadMore(): void {
  void sessions.loadMore()
}
</script>

<template>
  <section class="flex flex-col gap-5">
    <div class="flex items-center gap-2.5">
      <h1 class="text-2xl font-semibold tracking-tight">
        Sessions
      </h1>
      <Badge
        v-if="totalLoaded > 0"
        variant="secondary"
        class="font-normal"
      >
        {{ totalLoaded }} loaded
      </Badge>
    </div>

    <SessionFilterBar />

    <!--
      SessionTable owns its own error/loading/"filtered empty" rendering
      for every case except the one this ticket adds — a genuinely empty
      database — so it stays the default path and this view only carves
      out the one branch it needs to own: SetupCard, which SessionTable
      has no way to know it should show.
    -->
    <SkeletonTable v-if="!sessions.initialized || !metaSettled" />

    <SetupCard
      v-else-if="isGenuinelyEmpty && !sessions.error"
      :endpoint-url="meta.endpointUrl"
    />

    <EmptyState
      v-else-if="isFilteredEmpty && !sessions.loading && !sessions.error"
      title="No sessions match these filters"
      description="Try widening the date range or clearing a filter."
    >
      <Button
        variant="outline"
        size="sm"
        data-testid="clear-filters"
        @click="sessions.clearFilters()"
      >
        Clear filters
      </Button>
    </EmptyState>

    <SessionTable
      v-else
      :sessions="sessions.sessions"
      :sort="sessions.sort"
      :loading="sessions.loading"
      :loading-more="sessions.loadingMore"
      :error="sessions.error"
      :has-more="sessions.hasMore"
      @retry="onRetry"
      @load-more="onLoadMore"
      @select-session="onSelectSession"
      @sort="sessions.setSort"
    />
  </section>
</template>
