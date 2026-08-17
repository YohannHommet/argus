import { computed } from 'vue'
import { defineStore } from 'pinia'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import type { components } from '@/api/schema'
import { useApi } from '@/composables/useApi'

export type UnknownKindGroup = components['schemas']['UnknownKindGroup']
export type QualityUnknownKindsResponse = components['schemas']['QualityUnknownKindsResponse']
export type HookLatencyRow = components['schemas']['HookLatencyRow']
export type QualityHookLatencyResponse = components['schemas']['QualityHookLatencyResponse']

/**
 * The ticket's "unknown-kind events 24h" tile fixes this window — SPEC
 * §6.2 frames the whole view around "how a new release becomes visible in
 * minutes", which only needs a short, fixed lookback, not a user-facing
 * date-range control (P4-09 ships none).
 */
export const UNKNOWN_KINDS_WINDOW = '-24h'

/**
 * Owns Argus's two `/quality/*` REST endpoints (SPEC §6.2): the unmapped
 * `event_name` inspector and the hook-latency percentiles. Deliberately
 * does NOT fetch `/api/v1/meta` itself — `useMetaStore()` (already fetched
 * app-wide, 5-minute refresh) is the source for every meta-derived tile
 * (`data_quality.*`, `retention_days`); duplicating that fetch here would
 * just race the app's own boot fetch for no benefit.
 */
export const useQualityStore = defineStore('quality', () => {
  const unknownKinds = useApi<QualityUnknownKindsResponse>(
    (signal) => {
      const client = useApiClient()
      return unwrap(
        client.GET('/api/v1/quality/unknown-kinds', {
          params: { query: { since: UNKNOWN_KINDS_WINDOW } },
          signal,
        }),
      )
    },
    { immediate: true },
  )

  // No from/to: the ticket's live data example queries the endpoint with
  // no window at all and gets back everything Argus has ever measured —
  // there is no "last 24h" framing for hook latency in the ticket or
  // SPEC §6.2, unlike the unknown-kinds tile.
  const hookLatency = useApi<QualityHookLatencyResponse>(
    (signal) => {
      const client = useApiClient()
      return unwrap(client.GET('/api/v1/quality/hook-latency', { signal }))
    },
    { immediate: true },
  )

  const unknownKindRows = computed<UnknownKindGroup[]>(() => unknownKinds.data.value?.rows ?? [])

  /**
   * Sum of every unmapped group's `count` in the window. `null` only until
   * the request has actually resolved once (SPEC §6.1: "we don't know
   * yet" is not "zero") — an empty `rows: []` (the default, clean-data
   * response) is a real, measured "0 unmapped events", not an unknown.
   */
  const unknownEventsTotal = computed<number | null>(() => {
    if (!unknownKinds.data.value) return null
    return unknownKindRows.value.reduce((sum, row) => sum + row.count, 0)
  })

  const hookLatencyRows = computed<HookLatencyRow[]>(() => hookLatency.data.value?.rows ?? [])

  const loading = computed(() => unknownKinds.loading.value || hookLatency.loading.value)

  /**
   * True once BOTH requests have settled one way or another (data or a
   * definitive error) — real data, an empty `rows: []`, and an error are
   * all legitimate first paints for this view; a still-pending request on
   * either endpoint is not. This is the getter `DataQualityView.vue` hands
   * `useCaptureReady`, combined with the meta store's own settlement.
   */
  const settled = computed(
    () =>
      !unknownKinds.loading.value &&
      !hookLatency.loading.value &&
      (unknownKinds.data.value !== null || unknownKinds.error.value !== null) &&
      (hookLatency.data.value !== null || hookLatency.error.value !== null),
  )

  /**
   * Re-issues both requests — the two `useApi({ immediate: true })` calls
   * above already fire once on store creation, so this exists only for an
   * explicit "refresh everything" caller; `DataQualityView.vue` uses the
   * two individual `refetch*` actions below instead, one per retry button.
   */
  async function load(): Promise<void> {
    await Promise.all([unknownKinds.execute(), hookLatency.execute()])
  }

  return {
    unknownKindsData: unknownKinds.data,
    unknownKindsError: unknownKinds.error,
    unknownKindsLoading: unknownKinds.loading,
    hookLatencyData: hookLatency.data,
    hookLatencyError: hookLatency.error,
    hookLatencyLoading: hookLatency.loading,
    unknownKindRows,
    unknownEventsTotal,
    hookLatencyRows,
    loading,
    settled,
    load,
    refetchUnknownKinds: unknownKinds.execute,
    refetchHookLatency: hookLatency.execute,
  }
})
