<script setup lang="ts">
/**
 * SPEC §4.1: `cost.usd` is `reported_usd + estimated_usd` — the estimated
 * part is filled in from `model_prices` whenever an `llm.request` event
 * carried no reported cost (e.g. `--cost-mode=omit` demo data, or a
 * request Claude Code itself didn't attach a price to). `estimated_share`
 * can legitimately be anywhere from just-above-zero to a full `1` (100%,
 * live-verified against `--cost-mode=omit` data), so the copy below reads
 * correctly at both ends rather than assuming "estimated" always means
 * "a sliver of the total".
 *
 * Self-guarding on `estimatedShare > 0` (rather than the host wrapping it
 * in a `v-if`) so a mount test can assert both "renders nothing at 0" and
 * "renders the percentage at 0.02/1" against the same component.
 */
import { computed } from 'vue'

import { formatCost, formatPercent } from '@/lib/format'

interface Props {
  estimatedShare: number
  estimatedUsd: number
  totalUsd: number
}

const props = defineProps<Props>()

const isVisible = computed(() => props.estimatedShare > 0)
</script>

<template>
  <div
    v-if="isVisible"
    role="status"
    class="border-warn/40 bg-warn/10 flex flex-col gap-1 rounded-lg border p-3 text-sm"
    data-testid="estimated-cost-notice"
  >
    <p class="text-foreground font-medium">
      <span data-testid="estimated-cost-share">{{ formatPercent(estimatedShare) }}</span>
      of this window's {{ formatCost(totalUsd) }} cost is estimated, not reported by the vendor.
    </p>
    <p class="text-muted-foreground text-xs">
      {{ formatCost(estimatedUsd) }} of it comes from <code>llm.request</code> events that carried
      no cost of their own — Argus priced those from its own <code>model_prices</code> table
      instead. It can take roughly a minute after ingest for this figure to appear or settle, while
      the cost rollup catches up.
    </p>
  </div>
</template>
