import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import type { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { notifyApiFailure } from '@/lib/toast'

export type Meta = components['schemas']['Meta']
export type Facets = components['schemas']['Facets']

/** SPEC §6.4: `/api/v1/meta` + `/api/v1/facets`, fetched at boot, refreshed every 5 minutes. */
export const META_REFRESH_INTERVAL_MS = 5 * 60 * 1000

export const useMetaStore = defineStore('meta', () => {
  const meta = ref<Meta | null>(null)
  const facets = ref<Facets | null>(null)
  const loading = ref(false)
  const error = ref<ApiError | Error | null>(null)
  const lastFetchedAt = ref<Date | null>(null)

  let refreshTimer: ReturnType<typeof setInterval> | null = null

  /**
   * Fetches meta + facets in parallel via `Promise.allSettled` — the two
   * endpoints are independent, and a health-strip/setup-hint UI reading
   * `facets` shouldn't go blank just because `meta` (or vice versa)
   * happened to 500. Skips the refetch when the last one is still fresh
   * unless `force` is passed, so mounting several consumers at once
   * (health strip + filter bar, say) doesn't fan out duplicate requests.
   */
  async function load(options: { force?: boolean } = {}): Promise<void> {
    const { force = false } = options
    if (
      !force &&
      lastFetchedAt.value &&
      Date.now() - lastFetchedAt.value.getTime() < META_REFRESH_INTERVAL_MS
    ) {
      return
    }

    loading.value = true
    const client = useApiClient()

    const [metaResult, facetsResult] = await Promise.allSettled([
      unwrap(client.GET('/api/v1/meta', {})),
      unwrap(client.GET('/api/v1/facets', {})),
    ])

    let latestError: ApiError | Error | null = null

    if (metaResult.status === 'fulfilled') {
      meta.value = metaResult.value
    } else {
      latestError = metaResult.reason instanceof Error ? metaResult.reason : new Error(String(metaResult.reason))
    }

    if (facetsResult.status === 'fulfilled') {
      facets.value = facetsResult.value
    } else {
      // Last error wins when both fail — a single `error` ref can't carry
      // two failures at once, and which one wins doesn't change what the
      // caller should do (retry `load()`).
      latestError = facetsResult.reason instanceof Error ? facetsResult.reason : new Error(String(facetsResult.reason))
    }

    error.value = latestError
    lastFetchedAt.value = new Date()
    loading.value = false
  }

  function startAutoRefresh(): void {
    stopAutoRefresh()
    refreshTimer = setInterval(() => {
      // A background refresh has no retry button and no inline slot for a
      // failure to land in — the view that started this a while ago is
      // still showing the last-good meta/facets, and should keep doing
      // so. A toast is the one place this failure can surface at all
      // without silently swallowing it or tearing down a working screen.
      void load({ force: true }).then(() => {
        if (error.value) notifyApiFailure(error.value, { title: 'Background refresh failed' })
      })
    }, META_REFRESH_INTERVAL_MS)
  }

  function stopAutoRefresh(): void {
    if (refreshTimer !== null) {
      clearInterval(refreshTimer)
      refreshTimer = null
    }
  }

  function reset(): void {
    stopAutoRefresh()
    meta.value = null
    facets.value = null
    loading.value = false
    error.value = null
    lastFetchedAt.value = null
  }

  const projects = computed(() => facets.value?.projects ?? [])
  const models = computed(() => facets.value?.models ?? [])
  const vendors = computed(() => facets.value?.vendors ?? [])
  const tools = computed(() => facets.value?.tools ?? [])
  const decisionSources = computed(() => facets.value?.decision_sources ?? [])
  const querySources = computed(() => facets.value?.query_sources ?? [])

  /**
   * "Is there any data at all yet" for empty-state/setup-hint screens.
   * Meta's `data_quality` flags answer "have we seen logs/metrics/hooks
   * *at all*", which stays true forever once any event has ever landed —
   * not "is the DB empty right now" (retention could since have expired
   * everything). `facets.projects` is the one signal that reflects what's
   * actually queryable today: an ingest pipeline with zero distinct
   * projects has nothing to show, full stop. `null` (not yet loaded) is
   * treated as "unknown", not "empty" — false, not true — so a
   * pre-boot-fetch screen doesn't flash an empty state.
   */
  const hasNoData = computed(() => meta.value !== null && facets.value !== null && facets.value.projects.length === 0)

  /**
   * `metrics_only_projects` (SPEC §4.1 "Null vs zero") lives on the
   * analytics `Summary` response, not on `Meta`/`Facets` — it's a
   * per-window fact ("which of the projects in *this* window only ever
   * emitted metrics, never logs"), not a global one. Neither endpoint this
   * store owns carries it, so this always reads `[]`; the analytics store
   * a later ticket adds is the real source and should expose its own copy
   * from `Summary.metrics_only_projects` rather than route through here.
   */
  const metricsOnlyProjects = computed<string[]>(() => [])
  const logsExporterSeen = computed(() => meta.value?.data_quality.logs_exporter_seen ?? false)
  const metricsExporterSeen = computed(() => meta.value?.data_quality.metrics_exporter_seen ?? false)
  const hooksSeen = computed(() => meta.value?.data_quality.hooks_seen ?? false)
  const toolDetailsSeen = computed(() => meta.value?.data_quality.tool_details_seen ?? false)

  /**
   * Where to point a "send your telemetry here" setup hint. `Meta` (SPEC
   * §4.3) carries no ingest-endpoint field — it describes what Argus has
   * already seen, not how to reach it — so this is derived from the
   * browser's own origin, which is correct for Argus's actual deployment
   * shape (SPEC §4.4: ops/read/ingest are one origin, `servers: [{url: /}]`
   * in openapi.yaml).
   */
  const endpointUrl = computed(() => window.location.origin)

  return {
    meta,
    facets,
    loading,
    error,
    lastFetchedAt,
    load,
    startAutoRefresh,
    stopAutoRefresh,
    reset,
    projects,
    models,
    vendors,
    tools,
    decisionSources,
    querySources,
    hasNoData,
    metricsOnlyProjects,
    logsExporterSeen,
    metricsExporterSeen,
    hooksSeen,
    toolDetailsSeen,
    endpointUrl,
  }
})
