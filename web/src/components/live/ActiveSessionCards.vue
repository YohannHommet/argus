<script setup lang="ts">
/**
 * SPEC §6.2's live-view tiles: one dense row per session currently seen on
 * the firehose, with an identity block (status, project, vendor, short id +
 * copy) on the left and right-aligned tabular metric columns (last event,
 * cost, current tool) plus a "follow" affordance into `SessionDetailView`
 * (P5-06's territory — this only links to `/sessions/:id?live=1`; the query
 * param is a request for that view to open in live-follow mode, honoured or
 * not on its side) on the right.
 *
 * Round-7 critic gap: a per-session `Card` with cost/current-tool stacked
 * *below* the identity heading wasted ~85% of a wide viewport's width on a
 * single active session — the card's own content only needed a fraction of
 * that box, pushing the event feed below it far down the page. Sessions now
 * render as stacked rows inside one contained surface (the same
 * border/rounded/bg-card idiom `SessionTable.vue` uses for its own row
 * container), each row collapsing metrics into right-aligned tabular
 * columns exactly like `SessionRow.vue`'s table cells — visually kin to a
 * sessions-table row, not a floating card. "Started" is dropped from the
 * row entirely (it was never part of this ticket's three requested metric
 * columns — last event, cost, current tool — and a fourth column would
 * undo the width win this round exists to deliver).
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
import { formatAbsoluteTime, formatCost, formatRelativeTime } from '@/lib/format'
import { NO_PROJECT_SIGNAL_YET } from '@/lib/nullReasons'

interface Props {
  /** Latest per-session projection snapshot — one row per entry, insertion order. */
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
      class="border-border bg-card divide-border divide-y overflow-hidden rounded-xl border"
    >
      <div
        v-for="session in sessions"
        :key="session.id"
        data-testid="active-session-card"
        :data-session-id="session.id"
        class="flex min-w-0 items-center justify-between gap-4 px-3 py-3"
      >
        <div class="flex min-w-0 items-center gap-1.5">
          <StatusDot :status="session.status" />
          <span
            class="min-w-0 truncate text-sm font-semibold"
            :title="session.cwd"
            data-testid="active-session-card-title"
          >
            <!--
              Round-8 critic gap: "Unknown Unknown project" — `StatusDot` to
              this row's left already renders its own visible status word
              (falling back to "Unknown" for an out-of-vocabulary/unset
              status), and this placeholder used to lead with "Unknown" too,
              so the two collided into a stutter reading as one garbled
              phrase. Reworded so the status word — whatever it is — appears
              exactly once on the row; this placeholder no longer repeats it.
            -->
            <NullValue
              v-if="session.project === ''"
              label="No project yet"
              :reason="NO_PROJECT_SIGNAL_YET"
            />
            <template v-else>
              {{ session.project }}
            </template>
          </span>
          <span class="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
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
          </span>
        </div>

        <div class="flex shrink-0 items-center gap-4">
          <div class="min-w-[4.5rem] text-right">
            <p class="text-muted-foreground text-[0.6875rem]">
              Last event
            </p>
            <p class="text-sm leading-tight tabular-nums">
              <time :title="formatAbsoluteTime(session.last_event_at)">{{ formatRelativeTime(session.last_event_at) }}</time>
            </p>
          </div>
          <div class="min-w-14 text-right">
            <p class="text-muted-foreground text-[0.6875rem]">
              Cost
            </p>
            <p
              class="text-cost text-sm leading-tight font-semibold tabular-nums"
              data-testid="active-session-card-cost"
            >
              {{ formatCost(session.cost.usd) }}
            </p>
          </div>
          <div class="min-w-20 text-right">
            <p class="text-muted-foreground text-[0.6875rem]">
              Current tool
            </p>
            <!-- The reason tooltip lives on NullValue itself (below) when unknown — no separate info icon needed. -->
            <p
              class="truncate font-mono text-sm leading-tight"
              data-testid="active-session-card-tool"
            >
              <!--
                `plain`: this cell's whole value is one bare EM_DASH glyph
                when no tool.* event has landed yet, the exact case
                `NullValue`'s own doc calls out — its default dotted-
                underline "hint text" styling reads as a rendering glitch
                on a lone glyph with nothing beside it, and (round-6 critic)
                was one of three different em-dash "weights" visible on this
                view. `plain` drops the underline while keeping the same
                title/aria-label reason on hover.
              -->
              <NullValue
                v-if="!currentToolName(session.id)"
                plain
                :reason="currentToolReason(session.id)"
              />
              <template v-else>
                {{ currentToolName(session.id) }}
              </template>
            </p>
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
      </div>
    </div>
  </div>
</template>
