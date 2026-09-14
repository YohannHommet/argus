<script setup lang="ts">
import { computed } from 'vue'

import NullValue from '@/components/common/NullValue.vue'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { NOT_MEASURED } from '@/lib/nullReasons'
import { formatCost, formatCount, formatDuration, formatPercent, formatRejectRate, formatTokens } from '@/lib/format'
import type { components } from '@/api/schema'

// A `SessionSummary`, not `SessionDetail`: typing the prop at this narrower level is what makes "the
// KPI strip's cost matches the list row" a type-level guarantee — both read `SessionSummary.cost.usd`,
// so a `SessionDetail` (which extends it) can never substitute a recomputed figure.
type SessionSummary = components['schemas']['SessionSummary']

const props = defineProps<{
  session: SessionSummary | null
}>()

const totalTokens = computed(() => {
  const t = props.session?.tokens
  if (!t) return null
  // There is no single "session tokens" field — this strip's one number is input + output +
  // cache_read + cache_creation, every token the session actually moved through the model.
  return t.input + t.output + t.cache_read + t.cache_creation
})

/**
 * `tool_call_count === 0` means the rate is *undefined*, not zero (the
 * null-vs-zero distinction extended to a derived ratio: a 0/0 division is a
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

/**
 * `cost.usd` is `reported_usd + estimated_usd` — before a server-side fix,
 * an all-`--cost-mode=omit` session rendered `Cost $0.00` here with nothing to
 * tell an operator that $0.00 meant "never measured", not "measured zero".
 * `estimated_share` is the number that distinguishes them: 0
 * means every dollar shown was vendor-reported (today's behaviour, byte for
 * byte — the marker below simply never renders), `>0` means some or all of it
 * is Argus's own `model_prices` estimate.
 */
const estimatedShare = computed(() => props.session?.cost.estimated_share ?? 0)
const showEstimatedBadge = computed(() => estimatedShare.value > 0)
const fullyEstimated = computed(() => estimatedShare.value >= 1)
const estimatedBadgeLabel = computed(() => (fullyEstimated.value ? 'Estimated' : 'Partly est.'))
const estimatedBadgeReason = computed(() => {
  if (fullyEstimated.value) {
    return "This session's entire cost is estimated from Argus's own model_prices table — no event reported a vendor cost."
  }
  return `${formatPercent(estimatedShare.value)} of this session's cost is estimated from Argus's own model_prices table, not reported by the vendor.`
})
</script>

<template>
  <!-- A single-row band, not six bordered cards — the KPI strip is a caption for the tabs below, not its own dashboard. -->
  <div
    data-testid="session-kpi-strip"
    class="border-border divide-border bg-card flex flex-wrap divide-x rounded-lg border"
  >
    <div class="min-w-20 flex-1 px-3 py-1.5">
      <p class="text-muted-foreground text-[0.6875rem]">
        Cost
      </p>
      <p
        class="text-cost text-sm leading-tight font-semibold tabular-nums"
        data-testid="kpi-cost"
      >
        {{ formatCost(session?.cost.usd) }}
      </p>
      <TooltipProvider v-if="showEstimatedBadge">
        <Tooltip>
          <TooltipTrigger as-child>
            <span
              class="border-warn/40 bg-warn/10 text-warn mt-0.5 inline-block cursor-help rounded px-1 py-0.5 text-[0.625rem] font-medium tracking-wide uppercase"
              data-testid="kpi-cost-estimated-badge"
              :title="estimatedBadgeReason"
              :aria-label="estimatedBadgeReason"
            >{{ estimatedBadgeLabel }}</span>
          </TooltipTrigger>
          <TooltipContent>{{ estimatedBadgeReason }}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>

    <div class="min-w-20 flex-1 px-3 py-1.5">
      <p class="text-muted-foreground text-[0.6875rem]">
        Tokens
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="kpi-tokens"
      >
        {{ formatTokens(totalTokens) }}
      </p>
    </div>

    <div class="min-w-20 flex-1 px-3 py-1.5">
      <p class="text-muted-foreground text-[0.6875rem]">
        Turns
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="kpi-turns"
      >
        {{ formatCount(session?.turn_count) }}
      </p>
    </div>

    <div class="min-w-20 flex-1 px-3 py-1.5">
      <p class="text-muted-foreground text-[0.6875rem]">
        Tool calls
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="kpi-tools"
      >
        {{ formatCount(session?.tool_call_count) }}
      </p>
    </div>

    <div class="min-w-20 flex-1 px-3 py-1.5">
      <p class="text-muted-foreground text-[0.6875rem]">
        Reject rate
      </p>
      <p
        class="text-reject text-sm leading-tight font-semibold tabular-nums"
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

    <div class="min-w-20 flex-1 px-3 py-1.5">
      <p class="text-muted-foreground text-[0.6875rem]">
        Duration
      </p>
      <p
        class="text-sm leading-tight font-semibold tabular-nums"
        data-testid="kpi-duration"
      >
        {{ formatDuration(session?.duration_ms) }}
      </p>
    </div>
  </div>
</template>
