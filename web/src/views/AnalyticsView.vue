<script setup lang="ts">
/**
 * SPEC §4.3's fleet dashboard: KPI tiles, a cost timeseries (with a
 * `group_by` switch), a token timeseries, model + project breakdowns, the
 * decision matrix, a tool leaderboard, and an error panel — all driven by
 * `analyticsStore`, which owns every fetch/abort/URL-sync concern (P4-08).
 *
 * This view itself only maps store state to props/events: no fetching, no
 * URL manipulation, no attributability decisions happen here.
 */
import { computed } from 'vue'
import { useRouter } from 'vue-router'

import BreakdownChart from '@/components/analytics/BreakdownChart.vue'
import DecisionMatrix from '@/components/analytics/DecisionMatrix.vue'
import type { DecisionMatrixFilter } from '@/components/analytics/DecisionMatrix.vue'
import EstimatedCostNotice from '@/components/analytics/EstimatedCostNotice.vue'
import StatTile from '@/components/analytics/StatTile.vue'
import TimeSeriesChart from '@/components/analytics/TimeSeriesChart.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import NullValue from '@/components/common/NullValue.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useCaptureReady } from '@/composables/useCaptureReady'
import { formatRejectRate } from '@/lib/format'
import { NOT_ATTRIBUTABLE_TO_MODEL } from '@/lib/nullReasons'
import { ANALYTICS_PRESETS, GROUP_BY_OPTIONS, useAnalyticsStore } from '@/stores/analytics'
import type { AnalyticsPreset, GroupBy } from '@/stores/analytics'
import { useMetaStore } from '@/stores/meta'

const router = useRouter()
const analytics = useAnalyticsStore()
const meta = useMetaStore()
void meta.load()

/**
 * "Ready" across four-plus independent panels means *every* resource has
 * settled at least once (data, an empty page, or an error are all
 * photographable — a still-loading skeleton is not), not just the first one
 * to resolve. `analytics.initialized` is exactly that: it only flips once
 * the store's initial `Promise.allSettled` round over all eight resources
 * has completed (analytics.ts's `fetchAll`), and it never resets on a later
 * refetch — matching `sessions.ts`'s own `initialized` convention.
 */
useCaptureReady(() => analytics.initialized)

function toStringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : []
}

function onPresetChange(value: unknown): void {
  const next = String(value)
  if (next === 'custom') return // handled by the date inputs directly
  if ((ANALYTICS_PRESETS as readonly string[]).includes(next)) {
    analytics.setPreset(next as Exclude<AnalyticsPreset, 'custom'>)
  }
}

function onCustomFromChange(event: Event): void {
  analytics.setCustomRange((event.target as HTMLInputElement).value || null, analytics.customTo)
}

function onCustomToChange(event: Event): void {
  analytics.setCustomRange(analytics.customFrom, (event.target as HTMLInputElement).value || null)
}

function onGroupByChange(value: unknown): void {
  analytics.setGroupBy(String(value) as GroupBy)
}

/**
 * Exit criterion 5's other half: a decision-matrix cell links to `/tools`
 * filtered on the same tool/source, using the API's own snake_case param
 * names (`decision_source`, `tool_name`) — the `/tools` view reads exactly
 * those (another agent's ticket).
 */
function onDecisionFilter(payload: DecisionMatrixFilter): void {
  void router.push({ name: 'tools', query: { decision_source: payload.decision_source, tool_name: payload.tool_name } })
}

const summary = computed(() => analytics.summary.data)

/** `active_seconds` is a count of seconds; `formatDuration`/the 'duration' metric kind expect ms. */
const activeMs = computed(() => {
  const seconds = summary.value?.active_seconds
  return seconds === null || seconds === undefined ? null : seconds * 1000
})

const totalTokens = computed(() => {
  const tokens = summary.value?.tokens
  if (!tokens) return null
  return tokens.input + tokens.output
})

function tileReason(field: string): string | undefined {
  return analytics.isNotAttributable(field) ? NOT_ATTRIBUTABLE_TO_MODEL : undefined
}
</script>

<template>
  <section
    class="flex flex-col gap-6"
    data-testid="analytics-view"
  >
    <div class="flex items-baseline justify-between">
      <h1 class="text-2xl font-semibold">
        Analytics
      </h1>
    </div>

    <!--
      "Logs exporter appears off" banner: `metrics_only_projects` (SPEC
      §4.3) lives on the analytics Summary, not on /meta — a project can
      only be metrics-only *within a window*, which is a fact this store
      owns, not a global one metaStore could carry (see meta.ts's own
      `metricsOnlyProjects`, which is hardcoded to `[]` and says so).
    -->
    <div
      v-if="summary && summary.metrics_only_projects.length > 0"
      role="status"
      class="border-warn/40 bg-warn/10 rounded-lg border p-3 text-sm"
      data-testid="metrics-only-banner"
    >
      <p class="text-foreground font-medium">
        Logs exporter appears off for
        {{ summary.metrics_only_projects.join(', ') }}
      </p>
      <p class="text-muted-foreground mt-1 text-xs">
        {{ summary.metrics_only_projects.length === 1 ? 'This project' : 'These projects' }}
        sent OTel metrics in this window but no logs — almost every analytic below is blind for
        {{ summary.metrics_only_projects.length === 1 ? 'it' : 'them' }} until
        <code>OTEL_LOGS_EXPORTER=otlp</code> is set alongside the metrics exporter.
      </p>
    </div>

    <EstimatedCostNotice
      v-if="summary"
      :estimated-share="summary.cost.estimated_share"
      :estimated-usd="summary.cost.estimated_usd"
      :total-usd="summary.cost.usd"
    />

    <div
      class="flex flex-wrap items-end gap-3"
      data-testid="analytics-filter-bar"
    >
      <div class="flex flex-col gap-1">
        <label class="text-muted-foreground text-xs">Window</label>
        <Select
          :model-value="analytics.preset"
          data-testid="filter-window"
          @update:model-value="onPresetChange"
        >
          <SelectTrigger class="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="24h">
              Last 24h
            </SelectItem>
            <SelectItem value="7d">
              Last 7d
            </SelectItem>
            <SelectItem value="30d">
              Last 30d
            </SelectItem>
            <SelectItem value="custom">
              Custom
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <template v-if="analytics.preset === 'custom'">
        <div class="flex flex-col gap-1">
          <label
            for="analytics-from"
            class="text-muted-foreground text-xs"
          >From</label>
          <Input
            id="analytics-from"
            type="date"
            :model-value="analytics.customFrom ?? ''"
            data-testid="filter-from"
            @change="onCustomFromChange"
          />
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="analytics-to"
            class="text-muted-foreground text-xs"
          >To</label>
          <Input
            id="analytics-to"
            type="date"
            :model-value="analytics.customTo ?? ''"
            data-testid="filter-to"
            @change="onCustomToChange"
          />
        </div>
      </template>

      <div class="flex min-w-36 flex-col gap-1">
        <label class="text-muted-foreground text-xs">Project</label>
        <Select
          multiple
          :model-value="analytics.filters.project"
          data-testid="filter-project"
          @update:model-value="(v) => analytics.setFilters({ project: toStringArray(v) })"
        >
          <SelectTrigger class="w-full">
            <SelectValue placeholder="All projects" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="project in meta.projects"
              :key="project"
              :value="project"
            >
              {{ project }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="flex min-w-36 flex-col gap-1">
        <label class="text-muted-foreground text-xs">Model</label>
        <Select
          multiple
          :model-value="analytics.filters.model"
          data-testid="filter-model"
          @update:model-value="(v) => analytics.setFilters({ model: toStringArray(v) })"
        >
          <SelectTrigger class="w-full">
            <SelectValue placeholder="All models" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="model in meta.models"
              :key="model"
              :value="model"
            >
              {{ model }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="flex min-w-36 flex-col gap-1">
        <label class="text-muted-foreground text-xs">Vendor</label>
        <Select
          multiple
          :model-value="analytics.filters.vendor"
          data-testid="filter-vendor"
          @update:model-value="(v) => analytics.setFilters({ vendor: toStringArray(v) })"
        >
          <SelectTrigger class="w-full">
            <SelectValue placeholder="All vendors" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="vendor in meta.vendors"
              :key="vendor"
              :value="vendor"
            >
              {{ vendor }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <Button
        v-if="analytics.hasModelFilter || analytics.filters.project.length || analytics.filters.vendor.length"
        variant="ghost"
        size="sm"
        data-testid="filter-clear"
        @click="analytics.clearFilters()"
      >
        Clear filters
      </Button>
    </div>

    <!--
      SPEC §6.1's null-vs-zero thesis, tile by tile: every value below is bound directly to the raw
      `Summary` field. Under a model filter the server itself returns `null` (never `0`) for every
      non-attributable counter and lists it in `not_attributable[]` — `tileReason` reads that array
      (never a hardcoded client-side list) to supply StatTile's tooltip reason, and StatTile's own
      null-vs-zero handling renders a measured `0` (e.g. `loc.added: 0`) as "0", never collapsing it
      into the same dash a `null` gets.
    -->
    <div
      class="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-6"
      data-testid="analytics-kpi-grid"
    >
      <StatTile
        label="Sessions"
        :value="summary?.sessions ?? null"
        :reason="tileReason('sessions')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-sessions"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="Turns"
        :value="summary?.turns ?? null"
        :reason="tileReason('turns')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-turns"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="API requests"
        :value="summary?.api_requests ?? null"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-api-requests"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="API errors"
        :value="summary?.api_errors ?? null"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-api-errors"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="Tool calls"
        :value="summary?.tool_calls ?? null"
        :reason="tileReason('tool_calls')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-tool-calls"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="Tool rejects"
        :value="summary?.tool_rejects ?? null"
        :reason="tileReason('tool_rejects')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-tool-rejects"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="Cost"
        metric="cost"
        :value="summary?.cost.usd ?? null"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-cost"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="Tokens"
        metric="tokens"
        :value="totalTokens"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-tokens"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="LOC added"
        :value="summary?.loc?.added ?? null"
        :reason="tileReason('loc')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-loc-added"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="LOC removed"
        :value="summary?.loc?.removed ?? null"
        :reason="tileReason('loc')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-loc-removed"
        @retry="analytics.retrySummary()"
      />
      <StatTile
        label="Active time"
        metric="duration"
        :value="activeMs"
        :reason="tileReason('active_seconds')"
        :loading="analytics.summary.loading"
        :error="analytics.summary.error"
        data-testid="kpi-active-time"
        @retry="analytics.retrySummary()"
      />

      <!-- StatTile has no percent ChartMetricKind (SPEC has none for a rate) — reject_rate is
           formatted directly via `formatRejectRate` rather than misrepresented through 'count'. -->
      <Card
        size="sm"
        data-testid="kpi-reject-rate"
      >
        <CardHeader class="pb-0">
          <CardTitle class="text-muted-foreground text-xs font-normal">
            Reject rate
          </CardTitle>
        </CardHeader>
        <CardContent class="flex min-h-14 flex-col justify-center">
          <p
            v-if="summary && summary.reject_rate !== null"
            class="text-reject text-xl leading-tight font-semibold tabular-nums"
          >
            {{ formatRejectRate(summary.reject_rate) }}
          </p>
          <NullValue
            v-else
            :reason="tileReason('reject_rate')"
          />
        </CardContent>
      </Card>
    </div>

    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <Card data-testid="panel-cost-timeseries">
        <CardHeader class="flex flex-row items-center justify-between gap-2">
          <CardTitle>Cost over time</CardTitle>
          <Select
            :model-value="analytics.groupBy"
            data-testid="filter-group-by"
            @update:model-value="onGroupByChange"
          >
            <SelectTrigger class="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="option in GROUP_BY_OPTIONS"
                :key="option"
                :value="option"
              >
                {{ option }}
              </SelectItem>
            </SelectContent>
          </Select>
        </CardHeader>
        <CardContent>
          <TimeSeriesChart
            :data="analytics.costSeries.data"
            :loading="analytics.costSeries.loading"
            :error="analytics.costSeries.error"
            metric="cost"
            @retry="analytics.retryCostSeries()"
          />
        </CardContent>
      </Card>

      <Card data-testid="panel-token-timeseries">
        <CardHeader>
          <CardTitle>Tokens over time</CardTitle>
        </CardHeader>
        <CardContent>
          <TimeSeriesChart
            :data="analytics.tokenSeries.data"
            :loading="analytics.tokenSeries.loading"
            :error="analytics.tokenSeries.error"
            metric="tokens"
            @retry="analytics.retryTokenSeries()"
          />
        </CardContent>
      </Card>

      <Card data-testid="panel-model-breakdown">
        <CardHeader>
          <CardTitle>Cost by model</CardTitle>
        </CardHeader>
        <CardContent>
          <BreakdownChart
            :data="analytics.modelBreakdown.data"
            :loading="analytics.modelBreakdown.loading"
            :error="analytics.modelBreakdown.error"
            metric="cost"
            @retry="analytics.retryModelBreakdown()"
          />
        </CardContent>
      </Card>

      <Card data-testid="panel-project-breakdown">
        <CardHeader>
          <CardTitle>Cost by project</CardTitle>
        </CardHeader>
        <CardContent>
          <BreakdownChart
            :data="analytics.projectBreakdown.data"
            :loading="analytics.projectBreakdown.loading"
            :error="analytics.projectBreakdown.error"
            metric="cost"
            @retry="analytics.retryProjectBreakdown()"
          />
        </CardContent>
      </Card>

      <!--
        Tool leaderboard + error panel are both refused server-side under a model filter
        (SPEC §4.3: dimension=tool|error_type has no model column, and metric=calls is refused on
        any dimension) — analyticsStore's `isRequestAttributable` guard skips the request entirely
        rather than sending it and eating the 400, so under a model filter this renders an honest
        explanation instead of an empty chart or a fake error banner.
      -->
      <Card data-testid="panel-tool-leaderboard">
        <CardHeader>
          <CardTitle>Tool leaderboard</CardTitle>
        </CardHeader>
        <CardContent>
          <EmptyState
            v-if="analytics.toolBreakdown.notAttributable"
            title="Not available under a model filter"
            description="Tool call counts have no model column to filter on."
          />
          <BreakdownChart
            v-else
            :data="analytics.toolBreakdown.data"
            :loading="analytics.toolBreakdown.loading"
            :error="analytics.toolBreakdown.error"
            metric="count"
            @retry="analytics.retryToolBreakdown()"
          />
        </CardContent>
      </Card>

      <Card data-testid="panel-error-breakdown">
        <CardHeader>
          <CardTitle>Errors by type</CardTitle>
        </CardHeader>
        <CardContent>
          <EmptyState
            v-if="analytics.errorBreakdown.notAttributable"
            title="Not available under a model filter"
            description="Error counts have no model column to filter on."
          />
          <BreakdownChart
            v-else
            :data="analytics.errorBreakdown.data"
            :loading="analytics.errorBreakdown.loading"
            :error="analytics.errorBreakdown.error"
            metric="count"
            @retry="analytics.retryErrorBreakdown()"
          />
        </CardContent>
      </Card>
    </div>

    <Card data-testid="panel-decision-matrix">
      <CardHeader>
        <CardTitle>Decisions by tool × source</CardTitle>
      </CardHeader>
      <CardContent>
        <DecisionMatrix
          :data="analytics.decisions.data"
          :loading="analytics.decisions.loading"
          :error="analytics.decisions.error"
          @retry="analytics.retryDecisions()"
          @filter="onDecisionFilter"
        />
      </CardContent>
    </Card>
  </section>
</template>
