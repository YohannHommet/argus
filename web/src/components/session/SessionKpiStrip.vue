<script setup lang="ts">
import { computed } from 'vue'

import NullValue from '@/components/common/NullValue.vue'
import { NOT_MEASURED } from '@/lib/nullReasons'
import { formatCost, formatCount, formatDuration, formatRejectRate, formatTokens } from '@/lib/format'
import type { components } from '@/api/schema'

// A `SessionSummary`, not `SessionDetail`: every field this strip reads
// (cost, tokens, turn_count, tool_call_count, tool_reject_count,
// duration_ms) already lives on the summary shape the session list uses.
// Typing the prop at that (narrower) level — rather than SessionDetail —
// is what makes "the KPI strip's cost matches the list row" a type-level
// guarantee: both read `SessionSummary.cost.usd`, so passing the very
// SessionDetail response (which extends SessionSummary) here can never
// substitute a recomputed/re-rounded figure.
type SessionSummary = components['schemas']['SessionSummary']

const props = defineProps<{
  session: SessionSummary | null
}>()

const totalTokens = computed(() => {
  const t = props.session?.tokens
  if (!t) return null
  // SPEC has no single "session tokens" field — this strip's one number is
  // input + output + cache_read + cache_creation, i.e. every token the
  // session actually moved through the model, cache hits included.
  return t.input + t.output + t.cache_read + t.cache_creation
})

/**
 * `tool_call_count === 0` means the rate is *undefined*, not zero (SPEC
 * §6.1's null-vs-zero rule extended to a derived ratio: a 0/0 division is a
 * "we don't know" fact, not a measured "0%"). `null`/`undefined` is handled
 * the same way defensively, even though `SessionSummary.tool_call_count` is
 * typed as a non-nullable `number` — schema.d.ts's shape is the contract as
 * documented, not a guarantee a future server build can't loosen.
 */
const rejectRate = computed(() => {
  const calls = props.session?.tool_call_count
  if (calls === null || calls === undefined || calls === 0) return null
  const rejects = props.session?.tool_reject_count ?? 0
  return rejects / calls
})

const rejectRateReason = computed(() => {
  const calls = props.session?.tool_call_count
  if (calls === 0) return 'No tool calls recorded — reject rate is undefined, not 0%.'
  return NOT_MEASURED
})
</script>

<template>
  <div
    data-testid="session-kpi-strip"
    class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6"
  >
    <div class="border-border rounded-lg border p-2.5">
      <p class="text-muted-foreground text-xs">
        Cost
      </p>
      <p
        class="text-cost mt-0.5 text-lg leading-tight font-semibold tabular-nums"
        data-testid="kpi-cost"
      >
        {{ formatCost(session?.cost.usd) }}
      </p>
    </div>

    <div class="border-border rounded-lg border p-2.5">
      <p class="text-muted-foreground text-xs">
        Tokens
      </p>
      <p
        class="mt-0.5 text-lg leading-tight font-semibold tabular-nums"
        data-testid="kpi-tokens"
      >
        {{ formatTokens(totalTokens) }}
      </p>
    </div>

    <div class="border-border rounded-lg border p-2.5">
      <p class="text-muted-foreground text-xs">
        Turns
      </p>
      <p
        class="mt-0.5 text-lg leading-tight font-semibold tabular-nums"
        data-testid="kpi-turns"
      >
        {{ formatCount(session?.turn_count) }}
      </p>
    </div>

    <div class="border-border rounded-lg border p-2.5">
      <p class="text-muted-foreground text-xs">
        Tool calls
      </p>
      <p
        class="mt-0.5 text-lg leading-tight font-semibold tabular-nums"
        data-testid="kpi-tools"
      >
        {{ formatCount(session?.tool_call_count) }}
      </p>
    </div>

    <div class="border-border rounded-lg border p-2.5">
      <p class="text-muted-foreground text-xs">
        Reject rate
      </p>
      <p
        class="text-reject mt-0.5 text-lg leading-tight font-semibold tabular-nums"
        data-testid="kpi-reject-rate"
      >
        <NullValue
          v-if="rejectRate === null"
          :reason="rejectRateReason"
        />
        <template v-else>
          {{ formatRejectRate(rejectRate) }}
        </template>
      </p>
    </div>

    <div class="border-border rounded-lg border p-2.5">
      <p class="text-muted-foreground text-xs">
        Duration
      </p>
      <p
        class="mt-0.5 text-lg leading-tight font-semibold tabular-nums"
        data-testid="kpi-duration"
      >
        {{ formatDuration(session?.duration_ms) }}
      </p>
    </div>
  </div>
</template>
