<script setup lang="ts">
import { computed } from 'vue'

import { formatCost, formatCount, formatTokens } from '@/lib/format'
import type { SessionSummary } from '@/stores/sessions'

/**
 * Round-4 UI nit: a slim strip anchoring the table with totals for the sessions actually on
 * screen. Deliberately labelled "loaded set" everywhere it's shown — the API paginates (SPEC
 * §4.1's keyset cursor), so this is a sum over `sessions.sessions` (whatever's been fetched into
 * the client so far), never a global/server-side total. Conflating the two would silently
 * understate cost/tokens for anyone who hasn't hit "Load more" yet.
 */
const props = defineProps<{
  sessions: SessionSummary[]
}>()

const totalCost = computed(() => props.sessions.reduce((sum, s) => sum + s.cost.usd, 0))

const totalTokens = computed(() =>
  props.sessions.reduce((sum, s) => sum + s.tokens.input + s.tokens.output + s.tokens.cache_read + s.tokens.cache_creation, 0),
)
</script>

<template>
  <div
    v-if="sessions.length > 0"
    data-testid="session-list-summary"
    class="border-border bg-muted/20 text-muted-foreground flex flex-wrap items-center gap-x-5 gap-y-1 rounded-lg border px-3 py-2 text-xs"
  >
    <span class="font-medium">Loaded set</span>
    <span>
      <span
        class="text-cost font-semibold tabular-nums"
        data-testid="summary-cost"
      >{{ formatCost(totalCost) }}</span> cost
    </span>
    <span>
      <span
        class="text-foreground font-semibold tabular-nums"
        data-testid="summary-tokens"
      >{{ formatTokens(totalTokens) }}</span> tokens
    </span>
    <span>
      <span
        class="text-foreground font-semibold tabular-nums"
        data-testid="summary-sessions"
      >{{ formatCount(sessions.length) }}</span> sessions
    </span>
  </div>
</template>
