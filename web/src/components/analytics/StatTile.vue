<script setup lang="ts">
/**
 * A single KPI tile: a value (possibly `null`), its unit, and an optional
 * delta vs. the previous window. SPEC §6.1's whole null-vs-zero thesis
 * lives here — under a `?model=` filter the API returns `null` (never
 * `0`) for every non-model-attributable counter and lists it in
 * `not_attributable[]` (SPEC §4.3), so `value: null` must render `—` plus
 * a reason, and a measured `0` must render `0`, never collapse into `—`.
 * A delta against an unknown baseline is meaningless, so a `null` value
 * never renders a delta regardless of what `delta` holds.
 */
import { computed } from 'vue'

import { ApiError } from '@/api/errors'
import { formatterForMetric, type ChartMetricKind } from '@/lib/echarts'
import { NOT_ATTRIBUTABLE_TO_MODEL } from '@/lib/nullReasons'
import NullValue from '@/components/common/NullValue.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Sparkline from '@/components/analytics/Sparkline.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

interface Props {
  label: string
  /** `null` renders `—` (never `0`) — SPEC §6.1's not-attributable-under-a-filter case. */
  value?: number | null
  /** Selects the `lib/format.ts` formatter. Defaults to a plain count. */
  metric?: ChartMetricKind
  /**
   * Why `value` is null. Defaults to `NOT_ATTRIBUTABLE_TO_MODEL` — the
   * common case this tile exists for (a `?model=` filter's non-attributable
   * counters, SPEC §4.3) — override for any other null reason
   * (`lib/nullReasons.ts`'s other constants, or a one-off string).
   */
  reason?: string
  /**
   * Absolute change vs. the previous window, in `value`'s own unit
   * (the host computes it, e.g. `current - previous`). Omitted, or
   * `null`/`undefined`, renders no delta at all — including whenever
   * `value` itself is `null`, since a delta against an unknown baseline
   * is meaningless.
   */
  delta?: number | null
  /**
   * Current-window per-bucket values (see `lib/analyticsDelta.ts`'s
   * `sparklineValues`) for the inline trend line. `null`/omitted and
   * `trendReason` unset renders nothing; `null` with `trendReason` set
   * renders a small "no trend data" tooltip instead of a fabricated flat
   * line — some KPIs (e.g. LOC added/removed, active time, reject rate)
   * have no per-bucket timeseries backing them at all.
   */
  sparkline?: number[] | null
  /** Why there's no `sparkline` for this tile, shown as a tooltip when `sparkline` is null/omitted. */
  trendReason?: string
  /** `'lg'` promotes a tile to the KPI strip's primary row (Cost/Tokens/API requests) — bigger value type, no other behavior change. */
  size?: 'default' | 'lg'
  loading?: boolean
  error?: ApiError | Error | null
}

const props = withDefaults(defineProps<Props>(), {
  value: undefined,
  metric: 'count',
  reason: undefined,
  delta: undefined,
  sparkline: undefined,
  trendReason: undefined,
  size: 'default',
  loading: false,
  error: null,
})

const emit = defineEmits<{ retry: [] }>()

const isNull = computed(() => props.value === null || props.value === undefined)

const formattedValue = computed(() => {
  if (isNull.value) return null
  return formatterForMetric(props.metric)(props.value)
})

const nullReason = computed(() => props.reason ?? NOT_ATTRIBUTABLE_TO_MODEL)

// A delta is only meaningful when both the value and the delta itself are
// known numbers — the null-value case is handled by isNull above, but a
// present value with an unresolvable previous-window comparison (delta
// null/undefined) must also render nothing rather than a misleading "+0".
const showDelta = computed(() => !isNull.value && props.delta !== null && props.delta !== undefined)

const formattedDelta = computed(() => {
  if (!showDelta.value || props.delta === null || props.delta === undefined) return null
  const formatted = formatterForMetric(props.metric)(Math.abs(props.delta))
  const sign = props.delta > 0 ? '+' : props.delta < 0 ? '−' : '±'
  return `${sign}${formatted}`
})

const hasSparkline = computed(() => !isNull.value && !!props.sparkline && props.sparkline.length > 1)
const showTrendReason = computed(() => !isNull.value && !hasSparkline.value && !!props.trendReason)
</script>

<template>
  <Card
    size="sm"
    data-testid="stat-tile"
  >
    <CardHeader class="pb-0">
      <CardTitle class="text-muted-foreground text-xs font-normal">
        {{ label }}
      </CardTitle>
    </CardHeader>
    <CardContent class="flex min-h-14 flex-col justify-center">
      <ErrorState
        v-if="error"
        :error="error"
        :retryable="true"
        @retry="emit('retry')"
      />
      <Skeleton
        v-else-if="loading"
        class="h-7 w-20"
      />
      <template v-else>
        <p
          :class="[size === 'lg' ? 'text-3xl' : 'text-xl', 'leading-tight font-semibold tabular-nums']"
          data-testid="stat-tile-value"
        >
          <NullValue
            v-if="isNull"
            :reason="nullReason"
          />
          <template v-else>
            {{ formattedValue }}
          </template>
        </p>
        <p
          v-if="showDelta"
          class="text-muted-foreground mt-0.5 text-xs tabular-nums"
          data-testid="stat-tile-delta"
        >
          {{ formattedDelta }} vs previous window
        </p>
        <div
          v-if="hasSparkline"
          class="mt-1.5"
          data-testid="stat-tile-sparkline"
        >
          <Sparkline :values="sparkline" />
        </div>
        <div
          v-else-if="showTrendReason"
          class="mt-1.5 text-[11px]"
          data-testid="stat-tile-trend-reason"
        >
          <NullValue
            label="no trend data"
            :reason="trendReason"
          />
        </div>
      </template>
    </CardContent>
  </Card>
</template>
