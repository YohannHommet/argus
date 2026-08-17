import { ref } from 'vue'
import { defineStore } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import type { LocationQuery, LocationQueryRaw } from 'vue-router'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'

export type SessionSummary = components['schemas']['SessionSummary']
export type SessionStatus = components['schemas']['SessionStatus']

/** SPEC §4.1: keyset sort is desc-only on one of these four keys + id — there is no asc/desc toggle
 * to build a UI for. */
export const SORT_KEYS = ['last_event_at', 'started_at', 'cost_usd', 'event_count'] as const
export type SortKey = (typeof SORT_KEYS)[number]
export const DEFAULT_SORT: SortKey = 'last_event_at'

/** Argus-computed, closed (SPEC §1.7) — unlike vendor/free-form fields, safe to validate a query
 * param against. */
export const SESSION_STATUSES = ['active', 'ended', 'abandoned', 'unknown'] as const satisfies readonly SessionStatus[]

const DEFAULT_LIMIT = 50

/** Search is debounced ~300ms so a user typing doesn't fire a request per keystroke; select/date
 * filters use FILTER_DEBOUNCE_MS, which exists only to collapse several state changes landing in the
 * same tick (e.g. "clear all filters" touching six fields at once) into the single refetch the AC
 * requires — not as a UX delay. */
export const SEARCH_DEBOUNCE_MS = 300
const FILTER_DEBOUNCE_MS = 0

/** Repeated params OR within a field, AND across fields (SPEC §4.1's own filter semantics). */
export interface SessionFilters {
  project: string[]
  vendor: string[]
  model: string[]
  status: SessionStatus[]
  tool: string[]
  decisionSource: string[]
  /** RFC 3339 or relative shorthand (`-24h`, `-7d`); bounds `last_event_at` on this endpoint. */
  from: string | null
  to: string | null
  /** Substring match on id/project/cwd. */
  q: string
}

export function emptySessionFilters(): SessionFilters {
  return {
    project: [],
    vendor: [],
    model: [],
    status: [],
    tool: [],
    decisionSource: [],
    from: null,
    to: null,
    q: '',
  }
}

function queryToArray(value: LocationQuery[string]): string[] {
  if (value === undefined || value === null) return []
  const raw = Array.isArray(value) ? value : [value]
  return raw.filter((v): v is string => typeof v === 'string')
}

/**
 * Pure — parses a route query into filter/sort state. Exported so the round-trip (filters ->
 * `filtersToQuery` -> back through here) is testable without a router instance, which is exactly
 * what "survives a reload" (Phase-4 exit criterion 1) reduces to: a fresh page load hands the store
 * nothing but `route.query`, so it alone must be enough to reproduce the filter state that produced
 * it. An unrecognised `status` value (or a garbled `sort`) is dropped rather than thrown on — a
 * hand-edited or stale URL must degrade to "no filter"/"default sort", not a broken page.
 */
export function parseFiltersFromQuery(query: LocationQuery): { filters: SessionFilters; sort: SortKey } {
  const status = queryToArray(query.status).filter((s): s is SessionStatus =>
    (SESSION_STATUSES as readonly string[]).includes(s),
  )
  const sort =
    typeof query.sort === 'string' && (SORT_KEYS as readonly string[]).includes(query.sort)
      ? (query.sort as SortKey)
      : DEFAULT_SORT

  return {
    filters: {
      project: queryToArray(query.project),
      vendor: queryToArray(query.vendor),
      model: queryToArray(query.model),
      status,
      tool: queryToArray(query.tool),
      decisionSource: queryToArray(query.decision_source),
      from: typeof query.from === 'string' ? query.from : null,
      to: typeof query.to === 'string' ? query.to : null,
      q: typeof query.q === 'string' ? query.q : '',
    },
    sort,
  }
}

/**
 * Pure — inverse of {@link parseFiltersFromQuery}. Empty arrays and an empty `q` are omitted
 * entirely rather than serialised as `?project=` (matching the API's own repeated-param style, and
 * keeping the URL clean); `sort` is omitted when it's the default so an untouched page doesn't grow
 * a `?sort=last_event_at` nobody asked for.
 */
export function filtersToQuery(filters: SessionFilters, sort: SortKey): LocationQueryRaw {
  const query: LocationQueryRaw = {}
  if (filters.project.length) query.project = filters.project
  if (filters.vendor.length) query.vendor = filters.vendor
  if (filters.model.length) query.model = filters.model
  if (filters.status.length) query.status = filters.status
  if (filters.tool.length) query.tool = filters.tool
  if (filters.decisionSource.length) query.decision_source = filters.decisionSource
  if (filters.from) query.from = filters.from
  if (filters.to) query.to = filters.to
  if (filters.q) query.q = filters.q
  if (sort !== DEFAULT_SORT) query.sort = sort
  return query
}

/**
 * Reject rate is *undefined*, not zero, whenever there's nothing meaningful to divide
 * (SPEC §6.1's null-vs-zero rule, applied to a derived metric): no hook coverage at all
 * (`tool_call_count` unset) and exactly zero tool calls both return `null` so a formatter renders
 * `—`, never a misleading `0%`. Loosely typed on purpose (not `Pick<SessionSummary, ...>`) — the
 * live schema has both fields as non-nullable `number`, but this function is the one place that
 * still has to cope if a future/degraded payload ever sends `null` for them.
 */
export function computeRejectRate(session: {
  tool_call_count: number | null | undefined
  tool_reject_count: number | null | undefined
}): number | null {
  const calls = session.tool_call_count
  if (calls === null || calls === undefined || calls === 0) return null
  const rejects = session.tool_reject_count
  if (rejects === null || rejects === undefined) return null
  return rejects / calls
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException ? err.name === 'AbortError' : (err as Error)?.name === 'AbortError'
}

/**
 * SPEC §4.1's session list, filtered/sorted/paginated, with the URL query as the single source of
 * truth for filter state (Phase-4 exit criterion 1: filtering changes the result set, is reflected
 * in the URL, and survives a reload).
 *
 * Every filter/sort mutation goes through `setFilters`/`setSearch`/`setSort` — never assign
 * `.filters`/`.sort` directly — because those three actions are what keeps the URL and the debounced
 * refetch in sync with state. Directly mutating the returned refs would desync the route from what's
 * actually being fetched.
 */
export const useSessionsStore = defineStore('sessions', () => {
  const router = useRouter()
  const route = useRoute()

  const initial = parseFiltersFromQuery(route.query)
  const filters = ref<SessionFilters>(initial.filters)
  const sort = ref<SortKey>(initial.sort)

  const sessions = ref<SessionSummary[]>([])
  const nextCursor = ref<string | null>(null)
  const hasMore = ref(false)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref<ApiError | Error | null>(null)
  /** True once the first fetch has settled, one way or another — real data, an empty page, or an
   * error are all legitimate first paints; a still-pending request is not. This is exactly the
   * getter `SessionListView.vue` hands `useCaptureReady`. */
  const initialized = ref(false)

  let controller: AbortController | null = null
  let runId = 0
  let debounceTimer: ReturnType<typeof setTimeout> | null = null

  function clearDebounce(): void {
    if (debounceTimer !== null) {
      clearTimeout(debounceTimer)
      debounceTimer = null
    }
  }

  function buildQuery(cursor: string | null): Record<string, unknown> {
    return {
      project: filters.value.project,
      vendor: filters.value.vendor,
      model: filters.value.model,
      status: filters.value.status,
      tool: filters.value.tool,
      decision_source: filters.value.decisionSource,
      from: filters.value.from ?? undefined,
      to: filters.value.to ?? undefined,
      q: filters.value.q || undefined,
      sort: sort.value,
      limit: DEFAULT_LIMIT,
      cursor: cursor ?? undefined,
    }
  }

  async function fetchSessions(mode: 'replace' | 'append' = 'replace'): Promise<void> {
    controller?.abort()
    controller = new AbortController()
    const { signal } = controller
    const id = ++runId

    if (mode === 'replace') {
      loading.value = true
    } else {
      loadingMore.value = true
    }
    error.value = null

    const client = useApiClient()
    // Only an explicit 'append' (the "load more" button) ever sends a cursor — a filter/sort change
    // already cleared nextCursor synchronously in scheduleFetch, well before this call, so a stale
    // cursor from a previous filter state can never reach the request that follows it.
    const cursor = mode === 'append' ? nextCursor.value : null

    try {
      const result = await unwrap(client.GET('/api/v1/sessions', { params: { query: buildQuery(cursor) }, signal }))
      if (id !== runId) return

      if (mode === 'replace') {
        sessions.value = result.data
      } else {
        const seen = new Set(sessions.value.map((s) => s.id))
        sessions.value = [...sessions.value, ...result.data.filter((s) => !seen.has(s.id))]
      }
      nextCursor.value = result.page.next_cursor
      hasMore.value = result.page.has_more
    } catch (err) {
      if (isAbortError(err) || signal.aborted) return
      if (id !== runId) return
      error.value = err instanceof Error ? err : new Error(String(err))
      if (mode === 'replace') {
        sessions.value = []
        hasMore.value = false
        nextCursor.value = null
      }
    } finally {
      if (id === runId) {
        loading.value = false
        loadingMore.value = false
        initialized.value = true
      }
    }
  }

  function scheduleFetch(delayMs: number): void {
    clearDebounce()
    // A filter/sort change invalidates pagination the instant it happens, not when the debounced
    // fetch eventually fires — sending a cursor minted under the old filters is a 400
    // (urn:argus:error:invalid-cursor, SPEC §4.1).
    nextCursor.value = null
    hasMore.value = false
    debounceTimer = setTimeout(() => {
      debounceTimer = null
      void fetchSessions('replace')
    }, delayMs)
  }

  function syncRoute(): void {
    void router.replace({ query: filtersToQuery(filters.value, sort.value) })
  }

  /**
   * Merges a filter patch, replaces the URL query (`replace`, not `push` — filtering must never grow
   * the history stack), and schedules the (lightly debounced) refetch. Selects/date-range inputs call
   * this directly.
   */
  function setFilters(patch: Partial<SessionFilters>): void {
    filters.value = { ...filters.value, ...patch }
    syncRoute()
    scheduleFetch(FILTER_DEBOUNCE_MS)
  }

  /** Same contract as {@link setFilters}, but on the ~300ms search debounce — this is the path the
   * AC's "triggers exactly one (debounced) refetch" exercises. */
  function setSearch(q: string): void {
    filters.value = { ...filters.value, q }
    syncRoute()
    scheduleFetch(SEARCH_DEBOUNCE_MS)
  }

  function setSort(next: SortKey): void {
    if (sort.value === next) return
    sort.value = next
    syncRoute()
    scheduleFetch(FILTER_DEBOUNCE_MS)
  }

  function clearFilters(): void {
    setFilters(emptySessionFilters())
  }

  async function loadMore(): Promise<void> {
    if (!hasMore.value || loading.value || loadingMore.value) return
    await fetchSessions('append')
  }

  function refresh(): Promise<void> {
    return fetchSessions('replace')
  }

  /**
   * In-place row patch for a session already in the list — Phase 5's SSE feed lands updates here.
   * A no-op for an id the list doesn't currently hold (it is never appended), so an update for a
   * session outside the loaded page can't silently duplicate or reorder the list.
   */
  function applySessionUpdate(session: SessionSummary): void {
    const index = sessions.value.findIndex((s) => s.id === session.id)
    if (index === -1) return
    const next = sessions.value.slice()
    next[index] = session
    sessions.value = next
  }

  // Initial load: the very first render already has a filter/sort state (parsed from route.query
  // above), so it fetches immediately rather than waiting for a caller to kick it off.
  void fetchSessions('replace')

  return {
    filters,
    sort,
    sessions,
    nextCursor,
    hasMore,
    loading,
    loadingMore,
    error,
    initialized,
    setFilters,
    setSearch,
    setSort,
    clearFilters,
    loadMore,
    refresh,
    applySessionUpdate,
  }
})
