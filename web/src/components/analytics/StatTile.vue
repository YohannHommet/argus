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
import { CircleHelp } from '@lucide/vue'

import { ApiError } from '@/api/errors'
import { formatterForMetric, type ChartMetricKind } from '@/lib/echarts'
import { metricPolarity, type MetricKey } from '@/lib/echartsTheme'
import { NOT_ATTRIBUTABLE_TO_MODEL } from '@/lib/nullReasons'
import NullValue from '@/components/common/NullValue.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Sparkline from '@/components/analytics/Sparkline.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

interface Props {
  label: string
  /** `null` renders `—` (never `0`) — SPEC §6.1's not-attributable-under-a-filter case. */
  value?: number | null
  /** Selects the `lib/format.ts` formatter. Defaults to a plain count. */
  metric?: ChartMetricKind
  /**
   * Which KPI this tile is — selects the sparkline's hue and the delta's
   * direction-tint via `lib/echartsTheme.ts`'s `METRIC_SEMANTICS` table
   * (round-3 UI pass gap: "sparklines, deltas ... rendered in one
   * undifferentiated blue/gray"). Defaults to `'cost'` (neutral/primary)
   * for callers that don't pass one.
   */
  metricKey?: MetricKey
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
  /**
   * One short muted line under the value — the entire on-card prose budget
   * (round-6 UI-pass fix: tiles that used to carry a 4-5-line paragraph
   * *outside* the card, breaking containment and dwarfing the metric).
   * Truncates to a single line; the full story lives in `description`'s
   * tooltip instead of pushing the card taller.
   */
  summary?: string
  /** Full explanation, shown in a tooltip off an info icon next to `label`. Omit to skip the icon entirely. */
  description?: string
}

const props = withDefaults(defineProps<Props>(), {
  value: undefined,
  metric: 'count',
  metricKey: 'cost',
  reason: undefined,
  delta: undefined,
  sparkline: undefined,
  trendReason: undefined,
  size: 'default',
  loading: false,
  error: null,
  summary: undefined,
  description: undefined,
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

/**
 * Direction-tints the delta text by this tile's metric polarity (round-3
 * UI pass gap): a zero/absent delta stays muted, a `'destructive'` metric
 * (errors, rejects) reads red rising / green falling, and every other
 * metric reads accent rising / muted falling — never a moralizing
 * red/green for "spent more money" or "ran more sessions".
 */
const deltaClass = computed(() => {
  if (!showDelta.value || !props.delta) return 'text-muted-foreground'
  const polarity = metricPolarity(props.metricKey)
  if (polarity === 'destructive') return props.delta > 0 ? 'text-reject' : 'text-accept'
  return props.delta > 0 ? 'text-primary' : 'text-muted-foreground'
})
</script>

<template>
  <Card
    size="sm"
    data-testid="stat-tile"
  >
    <CardHeader class="pb-0">
      <CardTitle class="text-muted-foreground flex items-center gap-1 text-xs font-normal">
        {{ label }}
        <TooltipProvider v-if="description">
          <Tooltip>
            <TooltipTrigger as-child>
              <CircleHelp
                class="size-3 cursor-help"
                :title="description"
                :aria-label="description"
                data-testid="stat-tile-info"
              />
            </TooltipTrigger>
            <TooltipContent class="max-w-64 text-wrap">
              {{ description }}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
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
          :class="[deltaClass, 'mt-0.5 text-xs tabular-nums']"
          data-testid="stat-tile-delta"
        >
          {{ formattedDelta }} vs previous window
        </p>
        <div
          v-if="hasSparkline"
          class="mt-1.5"
          data-testid="stat-tile-sparkline"
        >
          <Sparkline
            :values="sparkline"
            :metric-key="metricKey"
          />
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
        <p
          v-if="summary"
          class="text-muted-foreground mt-1.5 line-clamp-1 text-xs"
          data-testid="stat-tile-summary"
        >
          {{ summary }}
        </p>
      </template>
    </CardContent>
  </Card>
</template>
