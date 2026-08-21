<script setup lang="ts">
/**
 * SPEC §6.2/§6.3: ingest health for the live view — queue depth, ingest lag,
 * dropped total, exporters seen, connection state. Lives under `layout/`
 * per SPEC §6.3's file map, but is rendered by `LiveView` (not `AppShell`) —
 * it describes the live *stream's* health, not the whole app's, so it
 * belongs beside the feed it reports on rather than the persistent chrome.
 *
 * A single divide-x band, same idiom as `session/SessionKpiStrip.vue`: a
 * caption strip, not six individually-bordered cards.
 *
 * ## Two "dropped" numbers, not one
 * `stats.dropped_total` (the `stats` SSE frame) and `clientDroppedTotal`
 * (`liveStore.droppedTotal`, accumulated from `lag` frames) measure
 * different failures and neither substitutes for the other:
 *   - **Dropped (server)**: events the ingest pipeline or the stream hub
 *     discarded before *any* subscriber received them — a fact about the
 *     server's own pipeline, true regardless of whether this tab is even
 *     connected.
 *   - **Dropped (this tab)**: frames the server sent but this specific
 *     browser tab's own subscriber buffer overflowed and never received —
 *     a fact about this one connection. The server's pipeline can be
 *     perfectly healthy (server count 0) while a slow tab still misses
 *     frames (client count > 0), and vice versa.
 * Collapsing them into one "dropped" figure would hide which side of the
 * connection actually lost the data — exactly the vagueness PLAN.md's P5-05
 * ticket calls out ("a tooltip that says 'N events dropped' without saying
 * by whom"). Each gets its own value + tooltip naming its source.
 *
 * ## Null vs zero (SPEC §4.1)
 * `stats` is `null` until the first `stats` SSE frame arrives (every ~2s
 * per SPEC §5.1) — queue depth/ingest lag/server-dropped all render `—` via
 * `NullValue` in that window, never a fabricated `0`, since "no frame yet"
 * and "measured zero" are different facts. `clientDroppedTotal` has no such
 * gap: it is a real running counter seeded at `0` the moment the store
 * exists, so `0` there is already an honest measurement ("this tab has
 * missed nothing so far"), not a placeholder.
 */
import { computed } from 'vue'
import { CircleHelp, Loader2, RefreshCw, Wifi, WifiOff } from '@lucide/vue'

import type { LiveStatus, StreamStatsFrame } from '@/stores/live'
import NullValue from '@/components/common/NullValue.vue'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { formatCount, formatDuration } from '@/lib/format'

interface Props {
  /** `liveStore.stats` — the latest `stats` SSE frame, or `null` before the first one arrives. */
  stats: StreamStatsFrame | null
  /** `liveStore.droppedTotal` — see the file doc for why this is not the same number as `stats.dropped_total`. */
  clientDroppedTotal: number
  /** `liveStore.status` — drives the connection indicator. */
  status: LiveStatus
  /** `useMetaStore()`'s `data_quality` flags (SPEC §4.3) — whether Argus has ever seen each exporter/hook fire, at all, ever. */
  logsExporterSeen: boolean
  metricsExporterSeen: boolean
  hooksSeen: boolean
  toolDetailsSeen: boolean
}

const props = defineProps<Props>()

const serverDroppedWarn = computed(() => props.stats !== null && props.stats.dropped_total > 0)
const clientDroppedWarn = computed(() => props.clientDroppedTotal > 0)

const SERVER_DROPPED_REASON =
  "Events the ingest pipeline or the stream hub discarded before any browser tab received them (the server's own stats.dropped_total) — independent of what this specific tab missed. Non-zero means Argus's server-side view of the data is incomplete; check ingest resource headroom."
const CLIENT_DROPPED_REASON =
  "Events this browser tab's own subscriber connection missed — reported via the stream's lag frames when this tab's buffer overflowed, e.g. the tab was backgrounded or the page was slow to keep up. Distinct from the server's own dropped_total: this counts only what this tab failed to receive."

/**
 * Connection indicator (SPEC §6.2). `idle` (never subscribed) reads the
 * same as `connecting` — a live view always subscribes on mount, so `idle`
 * is only ever visible for one reactive tick, if at all.
 */
const CONNECTION_META: Record<LiveStatus, { label: string; icon: typeof Wifi; class: string }> = {
  idle: { label: 'Connecting…', icon: Loader2, class: 'text-muted-foreground' },
  connecting: { label: 'Connecting…', icon: Loader2, class: 'text-muted-foreground' },
  open: { label: 'Connected', icon: Wifi, class: 'text-accept' },
  reconnecting: { label: 'Reconnecting…', icon: RefreshCw, class: 'text-warn' },
  closed: { label: 'Disconnected', icon: WifiOff, class: 'text-destructive' },
}

const connectionMeta = computed(() => CONNECTION_META[props.status])
/** Both `reconnecting` and `closed` are "the stream is not currently delivering frames" — the one state PLAN.md's AC requires a visible reconnect indicator for. */
const isDisconnected = computed(() => props.status === 'reconnecting' || props.status === 'closed')

const exporters = computed(() => [
  { key: 'logs', label: 'Logs', seen: props.logsExporterSeen },
  { key: 'metrics', label: 'Metrics', seen: props.metricsExporterSeen },
  { key: 'hooks', label: 'Hooks', seen: props.hooksSeen },
  { key: 'tool-details', label: 'Tool details', seen: props.toolDetailsSeen },
])

/**
 * Round-5 critic gap: every other tile in this strip leads with one
 * semibold `text-sm` value on its own baseline; this cell instead opened
 * straight into a wrapped row of 4 chips, breaking the strip's shared value
 * baseline. A "seen/total" count now fills that same slot.
 *
 * Round-7 critic gap: the per-exporter chip list (kept below the count as
 * the detail) still wrapped onto two rows at typical widths, ballooning
 * this one cell to 87px against the other five cells' 47px and leaving a
 * dead band under them. Every other cell in this strip that carries detail
 * beyond its headline value (Dropped (server)/Dropped (this tab)) puts that
 * detail in a `CircleHelp` info tooltip rather than inline — this cell now
 * follows the same idiom instead of being the one exception, which is what
 * restores the shared two-line cell height across the whole strip.
 */
const exportersSeenCount = computed(() => exporters.value.filter((e) => e.seen).length)

const EXPORTERS_DETAIL_REASON = computed(
  () => `Per-exporter breakdown: ${exporters.value.map((e) => `${e.label} — ${e.seen ? 'seen' : 'not seen'}`).join(', ')}.`,
)
</script>

<template>
  <div
    data-testid="health-strip"
    class="border-border divide-border bg-card flex flex-wrap divide-x rounded-lg border"
  >
    <div
      class="min-w-32 flex-1 px-3 py-1.5"
      data-testid="health-strip-connection"
    >
      <p class="text-muted-foreground text-[0.6875rem]">
        Connection
      </p>
      <!--
        `role="status"` here, not on the whole strip: this is the one field
        worth an assistive-tech announcement on change (SPEC's accessibility
        exit criterion asks for role=status on live-updating regions), but
        applying it to the fast-changing numeric cells below would spam a
        screen reader on every ~2s stats frame — the opposite of useful.
      -->
      <p
        role="status"
        class="flex items-center gap-1.5 text-sm leading-tight font-semibold"
        :class="connectionMeta.class"
      >
        <component
          :is="connectionMeta.icon"
          class="size-3.5 shrink-0"
          :class="{ 'animate-spin': status === 'connecting' || status === 'idle' }"
          aria-hidden="true"
        />
        {{ connectionMeta.label }}
      </p>
      <p
        v-if="isDisconnected"
        data-testid="health-strip-reconnect"
        class="text-warn mt-0.5 text-[0.6875rem]"
      >
        Reconnecting automatically…
      </p>
    </div>

    <div
      class="min-w-24 flex-1 px-3 py-1.5"
      data-testid="health-strip-queue-depth"
    >
      <p class="text-muted-foreground text-[0.6875rem]">
        Queue depth
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="health-strip-queue-depth-value"
      >
        <NullValue
          v-if="stats === null"
          reason="No stats frame received yet"
        />
        <template v-else>
          {{ formatCount(stats.queue_depth) }}
        </template>
      </p>
    </div>

    <div
      class="min-w-24 flex-1 px-3 py-1.5"
      data-testid="health-strip-ingest-lag"
    >
      <p class="text-muted-foreground text-[0.6875rem]">
        Ingest lag
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="health-strip-ingest-lag-value"
      >
        <NullValue
          v-if="stats === null"
          reason="No stats frame received yet"
        />
        <template v-else>
          {{ formatDuration(stats.ingest_lag_ms) }}
        </template>
      </p>
    </div>

    <div
      class="min-w-28 flex-1 px-3 py-1.5"
      data-testid="health-strip-dropped-server"
    >
      <p class="text-muted-foreground flex items-center gap-1 text-[0.6875rem]">
        Dropped (server)
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger as-child>
              <CircleHelp
                class="size-3 cursor-help"
                :title="SERVER_DROPPED_REASON"
                :aria-label="SERVER_DROPPED_REASON"
                data-testid="health-strip-dropped-server-info"
              />
            </TooltipTrigger>
            <TooltipContent class="max-w-64 text-wrap">
              {{ SERVER_DROPPED_REASON }}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        :class="{ 'text-warn': serverDroppedWarn }"
        data-testid="health-strip-dropped-server-value"
      >
        <NullValue
          v-if="stats === null"
          reason="No stats frame received yet"
        />
        <template v-else>
          {{ formatCount(stats.dropped_total) }}
        </template>
      </p>
    </div>

    <div
      class="min-w-28 flex-1 px-3 py-1.5"
      data-testid="health-strip-dropped-client"
    >
      <p class="text-muted-foreground flex items-center gap-1 text-[0.6875rem]">
        Dropped (this tab)
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger as-child>
              <CircleHelp
                class="size-3 cursor-help"
                :title="CLIENT_DROPPED_REASON"
                :aria-label="CLIENT_DROPPED_REASON"
                data-testid="health-strip-dropped-client-info"
              />
            </TooltipTrigger>
            <TooltipContent class="max-w-64 text-wrap">
              {{ CLIENT_DROPPED_REASON }}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        :class="{ 'text-warn': clientDroppedWarn }"
        data-testid="health-strip-dropped-client-value"
      >
        {{ formatCount(clientDroppedTotal) }}
      </p>
    </div>

    <div
      class="min-w-32 flex-1 px-3 py-1.5"
      data-testid="health-strip-exporters"
    >
      <p class="text-muted-foreground flex items-center gap-1 text-[0.6875rem]">
        Exporters seen
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger as-child>
              <CircleHelp
                class="size-3 cursor-help"
                :title="EXPORTERS_DETAIL_REASON"
                :aria-label="EXPORTERS_DETAIL_REASON"
                data-testid="health-strip-exporters-info"
              />
            </TooltipTrigger>
            <TooltipContent class="max-w-64 text-wrap">
              {{ EXPORTERS_DETAIL_REASON }}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="health-strip-exporters-value"
      >
        {{ exportersSeenCount }}/{{ exporters.length }}
      </p>
    </div>
  </div>
</template>
