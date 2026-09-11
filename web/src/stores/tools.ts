import { ref } from 'vue'
import { defineStore } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import type { LocationQuery, LocationQueryRaw } from 'vue-router'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'

export type ToolCall = components['schemas']['ToolCall']

const DEFAULT_LIMIT = 50

/**
 * `GET /api/v1/tool-calls`'s real, verified query surface (schema.d.ts's
 * `operations['listToolCalls']`, generated from `server/api/openapi.yaml`):
 * `project`, `tool`, `decision_source`, `from`, `to`, `limit`, `cursor`.
 *
 * Deliberately **not** here, even though earlier planning docs mentioned them:
 * `correlation` and `session` are not query parameters this endpoint
 * accepts, and there is **no `sort` parameter at all** — unlike
 * `GET /api/v1/sessions` (`sessions.ts`'s `sort: "last_event_at" | ... `),
 * `listToolCalls`'s inline query type has no `sort` field, and
 * `server/api/openapi.yaml`'s `/api/v1/tool-calls` parameters list
 * confirms it. This store never sends a `sort` param, and
 * `ToolCallTable.vue`'s column sort is a client-side reorder of whatever
 * page is currently loaded, not a refetch (see that component's own doc
 * comment).
 */
export interface ToolCallFilters {
  project: string[]
  tool: string[]
  decisionSource: string[]
  /** RFC 3339 or relative shorthand (`-24h`, `-7d`). */
  from: string | null
  to: string | null
}

export function emptyToolCallFilters(): ToolCallFilters {
  return {
    project: [],
    tool: [],
    decisionSource: [],
    from: null,
    to: null,
  }
}

function queryToArray(value: LocationQuery[string]): string[] {
  if (value === undefined || value === null) return []
  const raw = Array.isArray(value) ? value : [value]
  return raw.filter((v): v is string => typeof v === 'string')
}

/**
 * Pure — parses a route query into filter state, mirroring `sessions.ts`'s
 * `parseFiltersFromQuery`/`filtersToQuery` pair (same "single source of
 * truth is the URL" contract, same "drop anything unrecognised rather than
 * throw" rule). Duplicated rather than shared: `ToolCallFilters` is a
 * different, non-overlapping shape from `SessionFilters` (no `vendor`,
 * `model`, `status`, `q`, or `sort`), and this is only the *second*
 * occurrence of the pattern — duplicate once, extract on the third.
 *
 * `tool` is read from **both** `tool` (this endpoint's real query param —
 * see above) and `tool_name` (the field name `DecisionMatrix.vue` emits on
 * its `filter` event, matching `ToolCall.tool_name` — the `/analytics`
 * host hasn't been built yet, so this accepts whichever key a
 * not-yet-written navigation lands on rather than guessing one). The two
 * are merged, not one overriding the other — a hand-built or shared URL
 * would never carry both at once in practice.
 */
export function parseFiltersFromQuery(query: LocationQuery): ToolCallFilters {
  const toolFromApiKey = queryToArray(query.tool)
  const toolFromMatrixKey = queryToArray(query.tool_name)
  return {
    project: queryToArray(query.project),
    tool: Array.from(new Set([...toolFromApiKey, ...toolFromMatrixKey])),
    decisionSource: queryToArray(query.decision_source),
    from: typeof query.from === 'string' ? query.from : null,
    to: typeof query.to === 'string' ? query.to : null,
  }
}

/**
 * Pure — inverse of {@link parseFiltersFromQuery}. Always serialises the
 * `tool` filter back out under the API's own `tool` key (never `tool_name`)
 * so the URL normalises to the real query param on the very first
 * `router.replace` — a reload or a copy-pasted link then round-trips
 * through the `tool` branch alone. Empty arrays/nulls are omitted entirely,
 * matching `sessions.ts`'s convention.
 */
export function filtersToQuery(filters: ToolCallFilters): LocationQueryRaw {
  const query: LocationQueryRaw = {}
  if (filters.project.length) query.project = filters.project
  if (filters.tool.length) query.tool = filters.tool
  if (filters.decisionSource.length) query.decision_source = filters.decisionSource
  if (filters.from) query.from = filters.from
  if (filters.to) query.to = filters.to
  return query
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException ? err.name === 'AbortError' : (err as Error)?.name === 'AbortError'
}

/**
 * `/tools`'s cross-session tool-call list (the "decision-provenance
 * drill-down"), filtered and keyset-paginated, URL query as the single
 * source of truth for filter state — same contract as `sessions.ts`. Also
 * backs the deep link from `DecisionMatrix.vue`'s `filter`
 * event: whatever is in `route.query` at the moment this store is first
 * created (i.e. `/tools`'s first mount) becomes the initial filter set and
 * is fetched immediately.
 *
 * There is no `sort` here — see {@link ToolCallFilters}'s doc comment. Every
 * filter mutation goes through `setFilters`/`clearFilters`, never direct
 * `.filters` assignment, so the URL and the refetch stay in sync.
 */
export const useToolsStore = defineStore('tools', () => {
  const router = useRouter()
  const route = useRoute()

  const filters = ref<ToolCallFilters>(parseFiltersFromQuery(route.query))

  const toolCalls = ref<ToolCall[]>([])
  const nextCursor = ref<string | null>(null)
  const hasMore = ref(false)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref<ApiError | Error | null>(null)
  /** True once the first fetch has settled — real data, an empty page, or an error are all
   * legitimate first paints; this is the getter `ToolExplorerView.vue` hands `useCaptureReady`. */
  const initialized = ref(false)

  let controller: AbortController | null = null
  let runId = 0

  function buildQuery(cursor: string | null): Record<string, unknown> {
    return {
      project: filters.value.project,
      tool: filters.value.tool,
      decision_source: filters.value.decisionSource,
      from: filters.value.from ?? undefined,
      to: filters.value.to ?? undefined,
      limit: DEFAULT_LIMIT,
      cursor: cursor ?? undefined,
    }
  }

  async function fetchToolCalls(mode: 'replace' | 'append' = 'replace'): Promise<void> {
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
    // Only 'append' ever sends a cursor — a filter change already cleared nextCursor synchronously in
    // setFilters, so a stale cursor can never reach this call (a mismatch 400s).
    const cursor = mode === 'append' ? nextCursor.value : null

    try {
      const result = await unwrap(client.GET('/api/v1/tool-calls', { params: { query: buildQuery(cursor) }, signal }))
      if (id !== runId) return

      if (mode === 'replace') {
        toolCalls.value = result.data
      } else {
        const seen = new Set(toolCalls.value.map((t) => t.id))
        toolCalls.value = [...toolCalls.value, ...result.data.filter((t) => !seen.has(t.id))]
      }
      nextCursor.value = result.page.next_cursor
      hasMore.value = result.page.has_more
    } catch (err) {
      if (isAbortError(err) || signal.aborted) return
      if (id !== runId) return
      error.value = err instanceof Error ? err : new Error(String(err))
      if (mode === 'replace') {
        toolCalls.value = []
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

  function syncRoute(): void {
    void router.replace({ query: filtersToQuery(filters.value) })
  }

  /**
   * Merges a filter patch, replaces the URL query (`replace`, never `push`), drops pagination
   * synchronously, and refetches from the first page. No debounce: unlike `sessions.ts`'s `q` text
   * search, nothing here is typed character-by-character, so there is no keystroke burst to collapse.
   */
  function setFilters(patch: Partial<ToolCallFilters>): void {
    filters.value = { ...filters.value, ...patch }
    syncRoute()
    nextCursor.value = null
    hasMore.value = false
    void fetchToolCalls('replace')
  }

  function clearFilters(): void {
    setFilters(emptyToolCallFilters())
  }

  async function loadMore(): Promise<void> {
    if (!hasMore.value || loading.value || loadingMore.value) return
    await fetchToolCalls('append')
  }

  function refresh(): Promise<void> {
    return fetchToolCalls('replace')
  }

  // Initial load: the very first render already has a filter state (parsed from route.query, how the
  // DecisionMatrix deep link arrives already filtered), so it fetches immediately.
  void fetchToolCalls('replace')

  return {
    filters,
    toolCalls,
    nextCursor,
    hasMore,
    loading,
    loadingMore,
    error,
    initialized,
    setFilters,
    clearFilters,
    loadMore,
    refresh,
  }
})
