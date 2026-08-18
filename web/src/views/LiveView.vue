<script setup lang="ts">
/**
 * SPEC §6.2: `/live` — the firehose feed, active-session cards, and the
 * ingest health strip. This view is the sole owner of the tab's firehose
 * subscription for as long as it is mounted (`stores/live.ts`'s exit
 * criterion 6: "exactly one EventSource per tab" — the subscription stack
 * is how a concurrent `SessionDetailView` session-topic subscription and
 * this one coexist without opening a second connection).
 *
 * Like `AnalyticsView.vue`, this is a thin composition root: no formatting
 * or filtering logic of its own, just wiring `useLiveStore()` (already
 * committed, P5-04) and `useMetaStore()` (exporters-seen) into
 * `HealthStrip`/`ActiveSessionCards`/`LiveFeed`.
 */
import { computed, onMounted, onScopeDispose, onUnmounted } from 'vue'

import { useCaptureReady } from '@/composables/useCaptureReady'
import { useLiveStore, type LiveSubscription } from '@/stores/live'
import { useMetaStore } from '@/stores/meta'
import ActiveSessionCards from '@/components/live/ActiveSessionCards.vue'
import LiveFeed from '@/components/live/LiveFeed.vue'
import HealthStrip from '@/components/layout/HealthStrip.vue'

const live = useLiveStore()
const meta = useMetaStore()
void meta.load()

let subscription: LiveSubscription | null = null
onMounted(() => {
  subscription = live.subscribe({ kind: 'firehose' })
})
onUnmounted(() => {
  subscription?.close()
  subscription = null
})

/**
 * SPEC §5.2: a `reset`/`lag` frame means "the client's local state is
 * provably incomplete, refetch via REST" — `liveStore` already drops its
 * own stream-derived state (events/sessions/stats) before calling this
 * back, so the only *externally fetched* data this view still owns is
 * `meta` (exporters-seen, in `HealthStrip`). The firehose itself has no
 * REST-backed backlog to refetch — there is no "history" endpoint for it,
 * only whatever the reopened connection replays from the server's current
 * window — so a forced `meta.load` is the entirety of this view's refetch
 * responsibility.
 */
const unregisterReset = live.onReset(() => {
  void meta.load({ force: true })
})
onScopeDispose(unregisterReset)

const activeSessions = computed(() => Array.from(live.sessions.values()))

/**
 * "Ready" for the screenshot harness (see `useCaptureReady`'s own doc:
 * "the view has something real on it", not merely mounted) means the
 * stream has actually reached `open` AND at least one frame — an `event`
 * or a `stats` frame — has landed. `status === 'open'` alone is not enough:
 * a genuinely quiet deployment (SPEC's own low-traffic case) could sit at
 * `open` with an empty ring buffer and no `stats` frame yet (frames arrive
 * every ~2s per SPEC §5.1, not instantly on connect), which would let the
 * harness photograph a connected-but-empty feed.
 */
useCaptureReady(() => live.status === 'open' && (live.events.length > 0 || live.stats !== null))

function onPause(): void {
  live.pause()
}

function onResume(): void {
  live.resume()
}
</script>

<template>
  <section
    class="flex flex-col gap-6"
    data-testid="live-view"
  >
    <header>
      <h1 class="text-2xl font-semibold">
        Live
      </h1>
      <p class="text-muted-foreground mt-2 text-sm">
        The fleet-wide firehose — every event as it lands, which sessions are currently active, and
        this tab's own connection health.
      </p>
    </header>

    <HealthStrip
      :stats="live.stats"
      :client-dropped-total="live.droppedTotal"
      :status="live.status"
      :logs-exporter-seen="meta.logsExporterSeen"
      :metrics-exporter-seen="meta.metricsExporterSeen"
      :hooks-seen="meta.hooksSeen"
      :tool-details-seen="meta.toolDetailsSeen"
    />

    <section class="flex flex-col gap-2">
      <h2 class="text-lg font-medium">
        Active sessions
      </h2>
      <ActiveSessionCards
        :sessions="activeSessions"
        :events="live.events"
      />
    </section>

    <LiveFeed
      :events="live.events"
      :sessions="activeSessions"
      :paused="live.paused"
      :buffered-count="live.bufferedWhilePaused"
      @pause="onPause"
      @resume="onResume"
    />
  </section>
</template>
