<script setup lang="ts">
/**
 * The six KPI tiles PLAN.md's P4-09 names: partial sessions, unknown-kind
 * events (24h), clock-skewed events (24h), dropped total, heuristic
 * tool-call share, and oldest raw event.
 *
 * Only "unknown-kind events (24h)" is backed by a real aggregate field
 * anywhere in `server/api/openapi.yaml` (`QualityUnknownKindsResponse`,
 * summed client-side — see `stores/quality.ts`'s `unknownEventsTotal`).
 * The other five have no aggregate endpoint this view can reach:
 *   - partial sessions: `SessionSummary.partial` exists per-session
 *     (`/sessions`), never as a count.
 *   - clock-skewed (24h): `TimelineEvent.clock_skewed` exists per-event,
 *     never as a count.
 *   - dropped total: `StreamStatsFrame.dropped_total` exists only on the
 *     live SSE `/api/v1/stream`, not on any endpoint this store reads.
 *   - heuristic tool-call share: `ToolCall.correlation` (SPEC §1.6) is a
 *     per-row enum with a "heuristic" member, never aggregated into a
 *     share anywhere.
 *   - oldest raw event: no field, on any endpoint, carries the oldest
 *     queryable raw event's timestamp.
 * SPEC §4.1 forbids rendering a fabricated zero for any of these, so each
 * renders `—` via `NullValue` with an honest, tile-specific reason
 * (`NOT_EXPOSED_BY_API`) instead — never silently omitted, so every tile
 * PLAN.md names is still visibly present on the view.
 *
 * `StatTile` (components/analytics) is reused as-is for the four tiles
 * with no color-reactivity requirement below — it already owns the
 * null/loading/error rendering this view needs and has no reason to be
 * duplicated. It has no slot or color-override prop, though, so the two
 * "problem count" tiles whose value must flip to the warn colour when
 * positive (unknown-kind events, and dropped total once/if a future API
 * version exposes it) are built directly here instead, the same way
 * `components/session/SessionKpiStrip.vue` already handles its
 * color-reactive reject-rate tile (explicit `text-warn` class bound to a
 * computed, rather than a StatTile prop that doesn't exist).
 */
import { computed } from 'vue'

import { ApiError } from '@/api/errors'
import StatTile from '@/components/analytics/StatTile.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import NullValue from '@/components/common/NullValue.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { formatCount } from '@/lib/format'
import { NOT_EXPOSED_BY_API } from '@/lib/nullReasons'

interface Props {
  /** Sum of `/quality/unknown-kinds?since=-24h` rows' `count`. `null` only until that fetch first settles. */
  unknownEventsTotal?: number | null
  unknownEventsLoading?: boolean
  unknownEventsError?: ApiError | Error | null
  /**
   * Always `null` today — see the file-level comment. Accepted as a prop
   * (rather than hardcoded) so this tile lights up for free the day a
   * future API version adds an aggregate `dropped_total` this store can
   * fetch, without another round of component surgery.
   */
  droppedTotal?: number | null
  /** `/meta`'s `retention_days`, via `useMetaStore()` — used only to give the "oldest raw event" tile's honest dash a more useful reason. */
  retentionDays?: number | null
}

const props = withDefaults(defineProps<Props>(), {
  unknownEventsTotal: null,
  unknownEventsLoading: false,
  unknownEventsError: null,
  droppedTotal: null,
  retentionDays: null,
})

const emit = defineEmits<{ retry: [] }>()

function isWarn(value: number | null): boolean {
  return value !== null && value > 0
}

const unknownEventsIsNull = computed(() => props.unknownEventsTotal === null)
const unknownEventsWarn = computed(() => isWarn(props.unknownEventsTotal))
const droppedWarn = computed(() => isWarn(props.droppedTotal))

const oldestRawEventReason = computed(() => {
  const retentionNote =
    props.retentionDays !== null
      ? ` Argus currently retains raw events for ${props.retentionDays} days (/meta's retention_days) — the nearest available signal.`
      : ''
  return `${NOT_EXPOSED_BY_API}.${retentionNote}`
})
</script>

<template>
  <div
    data-testid="quality-tiles"
    class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3"
  >
    <!-- Unknown-kind events (24h) — the one tile backed by a real endpoint. -->
    <Card data-testid="quality-tile-unknown-events">
      <CardHeader>
        <CardTitle class="text-muted-foreground text-xs font-normal">
          Unknown-kind events (24h)
        </CardTitle>
      </CardHeader>
      <CardContent>
        <ErrorState
          v-if="unknownEventsError"
          :error="unknownEventsError"
          :retryable="true"
          @retry="emit('retry')"
        />
        <Skeleton
          v-else-if="unknownEventsLoading"
          class="h-8 w-20"
        />
        <template v-else>
          <p
            class="mt-1 text-2xl leading-tight font-semibold tabular-nums"
            :class="{ 'text-warn': unknownEventsWarn }"
            data-testid="quality-tile-unknown-events-value"
          >
            <NullValue
              v-if="unknownEventsIsNull"
              reason="Not yet fetched"
            />
            <template v-else>
              {{ formatCount(unknownEventsTotal) }}
            </template>
          </p>
          <p
            class="text-muted-foreground mt-2 text-xs"
            data-testid="quality-tile-explanation"
          >
            Events in the last 24h whose <code>event_name</code> Argus doesn't recognise — the
            first sign a vendor added a new instrumentation point. A non-zero count is expected
            right after a Claude Code upgrade; inspect the table below and, if it persists, teach
            Argus the new event name.
          </p>
        </template>
      </CardContent>
    </Card>

    <!-- Dropped total — no aggregate endpoint exposes this yet (see file-level comment). -->
    <Card data-testid="quality-tile-dropped-total">
      <CardHeader>
        <CardTitle class="text-muted-foreground text-xs font-normal">
          Dropped total
        </CardTitle>
      </CardHeader>
      <CardContent>
        <p
          class="mt-1 text-2xl leading-tight font-semibold tabular-nums"
          :class="{ 'text-warn': droppedWarn }"
          data-testid="quality-tile-dropped-total-value"
        >
          <NullValue
            v-if="droppedTotal === null"
            :reason="`${NOT_EXPOSED_BY_API}: dropped_total is only reported on the live event stream (/api/v1/stream), not by any endpoint this view reads.`"
          />
          <template v-else>
            {{ formatCount(droppedTotal) }}
          </template>
        </p>
        <p
          class="text-muted-foreground mt-2 text-xs"
          data-testid="quality-tile-explanation"
        >
          Events the ingest pipeline discarded outright, e.g. a subscriber's buffer overflowing
          under load. Any non-zero count means Argus's own view of the data is incomplete; check
          the ingest pipeline's resource headroom and consider raising subscriber buffer sizes.
        </p>
      </CardContent>
    </Card>

    <!-- Partial sessions — SessionSummary.partial exists per-session, never as a count. -->
    <div data-testid="quality-tile-partial-sessions">
      <StatTile
        label="Partial sessions"
        :value="null"
        :reason="`${NOT_EXPOSED_BY_API}: partial is a per-session flag on /sessions, never an aggregate count.`"
      />
      <p
        class="text-muted-foreground mt-2 px-1 text-xs"
        data-testid="quality-tile-explanation"
      >
        Sessions Argus only ever saw via a later reference, with no <code>session.start</code>
        event (SPEC §1.7) — usually a truncated export or a session that started before Argus's
        retention window began. Inspect individual sessions' "Partial" badge on the Sessions view
        to find them.
      </p>
    </div>

    <!-- Clock-skewed (24h) — TimelineEvent.clock_skewed exists per-event, never as a count. -->
    <div data-testid="quality-tile-clock-skewed">
      <StatTile
        label="Clock-skewed events (24h)"
        :value="null"
        :reason="`${NOT_EXPOSED_BY_API}: clock_skewed is a per-event flag on the events/timeline endpoints, never an aggregate count.`"
      />
      <p
        class="text-muted-foreground mt-2 px-1 text-xs"
        data-testid="quality-tile-explanation"
      >
        Events whose reported timestamp fell outside Argus's accepted window and were clamped on
        ingest. If you suspect skew, check the system clock on the machine(s) running Claude Code
        against a reliable time source (NTP).
      </p>
    </div>

    <!-- Heuristic tool-call share — Correlation is a per-row enum, never aggregated into a share. -->
    <div data-testid="quality-tile-heuristic-share">
      <StatTile
        label="Heuristic tool-call share"
        :value="null"
        :reason="`${NOT_EXPOSED_BY_API}: correlation is a per-tool-call field (SPEC §1.6), never aggregated into a share.`"
      />
      <p
        class="text-muted-foreground mt-2 px-1 text-xs"
        data-testid="quality-tile-explanation"
      >
        Share of tool-call correlations resolved by heuristic matching rather than exact
        hook/OTel correlation — the weakest confidence tier. A high share means tool-call
        attribution on this data is less trustworthy; check individual rows' correlation on the
        tool-calls endpoint if a number looks off.
      </p>
    </div>

    <!-- Oldest raw event — no field anywhere carries this. -->
    <div data-testid="quality-tile-oldest-raw-event">
      <StatTile
        label="Oldest raw event"
        :value="null"
        :reason="oldestRawEventReason"
      />
      <p
        class="text-muted-foreground mt-2 px-1 text-xs"
        data-testid="quality-tile-explanation"
      >
        How close Argus's oldest still-queryable raw event is to falling out of the retention
        window. Not exposed today; the retention window itself (above) is the nearest available
        signal for "how much history is left".
      </p>
    </div>
  </div>
</template>
