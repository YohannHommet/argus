<script setup lang="ts">
/**
 * SPEC §6.2's live-view tiles: one card per session currently seen on the
 * firehose, with an identity heading (status, project, vendor, short id,
 * started/last-event — the same identity block `SessionRow`/
 * `SessionDetailView` already establish elsewhere), cost, its current/last
 * tool, and a "follow" affordance into `SessionDetailView` (P5-06's
 * territory — this only links to `/sessions/:id?live=1`; the query param is
 * a request for that view to open in live-follow mode, honoured or not on
 * its side).
 *
 * Fully props-driven (no store read of its own): `sessions` is
 * `Array.from(liveStore.sessions.values())` and `events` is
 * `liveStore.events`, both computed by the host (`LiveView`). Keeping this
 * component ignorant of Pinia keeps it mountable with plain fixture arrays.
 */
import { computed } from 'vue'

import type { SessionSummary, TimelineEvent } from '@/stores/live'
import CopyIconButton from '@/components/common/CopyIconButton.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import NullValue from '@/components/common/NullValue.vue'
import RawValue from '@/components/common/RawValue.vue'
import StatusDot from '@/components/session/StatusDot.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatAbsoluteTime, formatCost, formatRelativeTime } from '@/lib/format'
import { NO_PROJECT_SIGNAL_YET } from '@/lib/nullReasons'

interface Props {
  /** Latest per-session projection snapshot — one card per entry, insertion order. */
  sessions: SessionSummary[]
  /** The live ring buffer, chronological oldest-first — read only to derive each session's current tool (see `latestToolEventBySession` below). */
  events: TimelineEvent[]
}

const props = defineProps<Props>()

const NO_TOOL_EVENT_REASON = 'No tool.* event observed on the live feed yet for this session'
const NO_TOOL_NAME_REASON = 'This tool event carries no tool_name'

/**
 * "Current tool" isn't a field `SessionSummary` carries (checked against
 * `schema.d.ts` — there is no such property), so PLAN.md's P5-05 ticket asks
 * for it to be derived honestly from the stream rather than invented. This
 * walks `events` in array order and keeps overwriting a per-session slot on
 * every `tool.*`-kind event (`tool.pre`/`tool.decision`/
 * `tool.permission_request`/`tool.result`/`tool.batch`), so the last write
 * for a given session_id is its most recent one.
 *
 * Deliberately does NOT sort by `ts` to find "most recent": `events` is
 * documented chronological (oldest-first, `stores/live.ts`'s own doc
 * comment), and a `clock_skewed` event's `ts` cannot be trusted for
 * ordering anyway (the same reasoning `collapseEvents.ts`'s `withinWindow`
 * uses to refuse to compare skewed timestamps) — trusting array order
 * sidesteps that failure mode entirely instead of silently mis-ordering on
 * a skewed clock.
 */
const latestToolEventBySession = computed(() => {
  const map = new Map<string, TimelineEvent>()
  for (const event of props.events) {
    if (!event.kind.startsWith('tool.')) continue
    map.set(event.session_id, event)
  }
  return map
})

function currentToolName(sessionId: string): string | null {
  return latestToolEventBySession.value.get(sessionId)?.tool_name ?? null
}

function currentToolReason(sessionId: string): string {
  return latestToolEventBySession.value.has(sessionId) ? NO_TOOL_NAME_REASON : NO_TOOL_EVENT_REASON
}

function shortId(sessionId: string): string {
  return sessionId.slice(0, 8)
}

/**
 * Round-5 critic gap: the grid used a fixed responsive breakpoint set
 * (1/2/3 columns) regardless of how many sessions were actually active, so
 * 1-2 live sessions rendered as narrow tiles stranded in a mostly-empty row
 * instead of filling it. Column count now tracks the session count itself
 * (capped at the same 3-wide layout larger counts already used) — a `1fr`
 * grid track stretches to fill the row on its own once there are fewer of
 * them than columns.
 */
const gridColsClass = computed(() => {
  const count = props.sessions.length
  if (count <= 1) return 'grid-cols-1'
  if (count === 2) return 'grid-cols-1 sm:grid-cols-2'
  return 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3'
})

/** `?live=1` — a request for `SessionDetailView` to open in live-follow mode (SPEC §6.2: "'follow session' jumps to detail in live mode"). The destination view's own handling of this param is a different ticket's territory. */
function followTarget(sessionId: string) {
  return { path: `/sessions/${sessionId}`, query: { live: '1' } }
}
</script>

<template>
  <div data-testid="active-session-cards">
    <EmptyState
      v-if="sessions.length === 0"
      title="No active sessions"
      description="Sessions appear here as soon as an event arrives on the live feed."
    />
    <div
      v-else
      class="grid items-stretch gap-3"
      :class="gridColsClass"
    >
      <Card
        v-for="session in sessions"
        :key="session.id"
        data-testid="active-session-card"
        :data-session-id="session.id"
      >
        <CardHeader class="pb-0">
          <div class="flex min-w-0 items-center justify-between gap-2">
            <div class="flex min-w-0 items-center gap-1.5">
              <StatusDot :status="session.status" />
              <CardTitle
                class="min-w-0 truncate text-sm font-semibold"
                :title="session.cwd"
                data-testid="active-session-card-title"
              >
                <NullValue
                  v-if="session.project === ''"
                  label="Unknown project"
                  :reason="NO_PROJECT_SIGNAL_YET"
                />
                <template v-else>
                  {{ session.project }}
                </template>
              </CardTitle>
            </div>
            <Button
              as-child
              variant="outline"
              size="sm"
              class="shrink-0"
              data-testid="active-session-card-follow"
            >
              <router-link :to="followTarget(session.id)">
                Follow
              </router-link>
            </Button>
          </div>

          <p class="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
            <RawValue
              :value="session.vendor"
              kind="vendor"
            />
            <span aria-hidden="true">·</span>
            <span
              class="truncate font-mono"
              data-testid="active-session-card-short-id"
              :title="session.id"
            >{{ shortId(session.id) }}</span>
            <CopyIconButton
              :text="session.id"
              label="Copy session id"
            />
          </p>

          <p class="text-muted-foreground text-xs">
            Started
            <time :title="formatAbsoluteTime(session.started_at)">{{ formatRelativeTime(session.started_at) }}</time>
            · Last event
            <time :title="formatAbsoluteTime(session.last_event_at)">{{ formatRelativeTime(session.last_event_at) }}</time>
          </p>
        </CardHeader>
        <CardContent class="flex flex-col gap-1.5">
          <div class="flex items-center justify-between text-xs">
            <span class="text-muted-foreground">Cost</span>
            <span
              class="text-cost font-semibold tabular-nums"
              data-testid="active-session-card-cost"
            >{{ formatCost(session.cost.usd) }}</span>
          </div>
          <div class="flex items-center justify-between gap-2 text-xs">
            <span class="text-muted-foreground">Current tool</span>
            <!-- The reason tooltip lives on NullValue itself (below) when unknown — no separate info icon needed. -->
            <span
              class="truncate font-mono"
              data-testid="active-session-card-tool"
            >
              <NullValue
                v-if="!currentToolName(session.id)"
                :reason="currentToolReason(session.id)"
              />
              <template v-else>
                {{ currentToolName(session.id) }}
              </template>
            </span>
          </div>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
