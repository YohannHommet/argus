<script setup lang="ts">
/**
 * SPEC §6.2's live-view tiles: one card per session currently seen on the
 * firehose, with cost, its current/last tool, and a "follow" link into
 * `SessionDetailView` (P5-06's territory — this only links to
 * `/sessions/:id?live=1`; the query param is a request for that view to
 * open in live-follow mode, honoured or not on its side).
 *
 * Fully props-driven (no store read of its own): `sessions` is
 * `Array.from(liveStore.sessions.values())` and `events` is
 * `liveStore.events`, both computed by the host (`LiveView`). Keeping this
 * component ignorant of Pinia keeps it mountable with plain fixture arrays.
 */
import { computed } from 'vue'

import type { SessionSummary, TimelineEvent } from '@/stores/live'
import EmptyState from '@/components/common/EmptyState.vue'
import NullValue from '@/components/common/NullValue.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatCost } from '@/lib/format'

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
      class="grid grid-cols-1 items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3"
    >
      <Card
        v-for="session in sessions"
        :key="session.id"
        data-testid="active-session-card"
        :data-session-id="session.id"
      >
        <CardHeader class="pb-0">
          <CardTitle class="flex min-w-0 items-center justify-between gap-2 text-xs font-normal">
            <span
              class="text-muted-foreground truncate font-mono"
              :title="session.cwd"
            >{{ session.project }}</span>
            <router-link
              :to="followTarget(session.id)"
              class="text-primary shrink-0 font-medium hover:underline"
              data-testid="active-session-card-follow"
            >
              Follow
            </router-link>
          </CardTitle>
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
