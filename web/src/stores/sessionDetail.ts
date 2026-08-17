import { computed, ref, shallowRef } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { defineStore } from 'pinia'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'

export type SessionDetail = components['schemas']['SessionDetail']
export type Turn = components['schemas']['Turn']
export type ToolCall = components['schemas']['ToolCall']
export type SubagentNode = components['schemas']['SubagentNode']
export type SubagentCostAttribution = components['schemas']['SubagentCostAttribution']
export type TimelineEvent = components['schemas']['TimelineEvent']
export type EventDetail = components['schemas']['EventDetail']
export type TimelineOrder = 'asc' | 'desc'
export type Kind = components['schemas']['Kind']

type StoreError = ApiError | Error | null

function toStoreError(err: unknown): Error {
  return err instanceof Error ? err : new Error(String(err))
}

/**
 * Per-session bundle: everything P4-03/04/05/06 fetch and cache for one
 * `/sessions/:id`. Every field is its own ref (rather than one big reactive
 * object) so a computed reading e.g. `entry.turns.value` re-runs only when
 * turns change, not on every session-detail poll.
 */
interface SessionDetailEntry {
  session: ShallowRef<SessionDetail | null>
  sessionLoading: Ref<boolean>
  sessionError: ShallowRef<StoreError>

  turns: ShallowRef<Turn[]>
  turnsLoading: Ref<boolean>
  turnsError: ShallowRef<StoreError>
  turnsLoaded: boolean

  toolCalls: ShallowRef<ToolCall[]>
  toolCallsLoading: Ref<boolean>
  toolCallsError: ShallowRef<StoreError>
  toolCallsLoaded: boolean

  subagents: ShallowRef<SubagentNode[]>
  costAttribution: ShallowRef<SubagentCostAttribution | null>
  subagentsLoading: Ref<boolean>
  subagentsError: ShallowRef<StoreError>
  subagentsLoaded: boolean

  timelineItems: ShallowRef<TimelineEvent[]>
  timelineNextCursor: ShallowRef<string | null>
  timelineHasMore: Ref<boolean>
  timelineLoading: Ref<boolean>
  timelineError: ShallowRef<StoreError>
  timelineLoaded: boolean

  eventCache: Map<string, EventDetail>
}

function createEntry(): SessionDetailEntry {
  return {
    session: shallowRef(null),
    sessionLoading: ref(false),
    sessionError: shallowRef(null),

    turns: shallowRef([]),
    turnsLoading: ref(false),
    turnsError: shallowRef(null),
    turnsLoaded: false,

    toolCalls: shallowRef([]),
    toolCallsLoading: ref(false),
    toolCallsError: shallowRef(null),
    toolCallsLoaded: false,

    subagents: shallowRef([]),
    costAttribution: shallowRef(null),
    subagentsLoading: ref(false),
    subagentsError: shallowRef(null),
    subagentsLoaded: false,

    timelineItems: shallowRef([]),
    timelineNextCursor: shallowRef(null),
    timelineHasMore: ref(false),
    timelineLoading: ref(false),
    timelineError: shallowRef(null),
    timelineLoaded: false,

    eventCache: new Map(),
  }
}

/** Cap on `docs/PLAN.md` P4-03's "LRU of 3" — one entry per distinct session id visited. */
export const SESSION_DETAIL_LRU_SIZE = 3

export const useSessionDetailStore = defineStore('sessionDetail', () => {
  // Plain (non-reactive-collection) Map: reactivity comes from the refs
  // held inside each SessionDetailEntry, not from the Map's own structure.
  // Map iteration order is insertion order, and `touch()` deletes+reinserts
  // an entry to move it to the end — the standard JS-Map LRU trick — so
  // `entries.keys().next()` is always the true least-recently-*used* key,
  // not merely the least-recently-*inserted* one.
  const entries = new Map<string, SessionDetailEntry>()
  const currentId = ref<string | null>(null)

  /** Marks `id` as most-recently-used without creating it. Returns the entry, or undefined if absent. */
  function touch(id: string): SessionDetailEntry | undefined {
    const entry = entries.get(id)
    if (entry) {
      entries.delete(id)
      entries.set(id, entry)
    }
    return entry
  }

  /** Gets-or-creates `id`'s entry, evicting the true LRU entry once the cache is over capacity. */
  function ensureEntry(id: string): SessionDetailEntry {
    const existing = touch(id)
    if (existing) return existing

    const entry = createEntry()
    entries.set(id, entry)
    if (entries.size > SESSION_DETAIL_LRU_SIZE) {
      const oldestKey = entries.keys().next().value
      if (oldestKey !== undefined) entries.delete(oldestKey)
    }
    return entry
  }

  function currentEntry(): SessionDetailEntry | undefined {
    return currentId.value ? entries.get(currentId.value) : undefined
  }

  // --- Timeline filters (SPEC §4.3 / PLAN P4-04). Shared across whichever
  // session is current — only one timeline is ever on screen at a time —
  // rather than duplicated per LRU entry. Changing a filter does not by
  // itself clear cached pages; callers refetch by passing `{ reset: true }`
  // to loadTimeline().
  const kinds = ref<Kind[]>([])
  const agentId = ref<string | null>(null)
  const promptId = ref<string | null>(null)
  const order = ref<TimelineOrder>('asc')

  function setTimelineFilters(filters: {
    kinds?: Kind[]
    agentId?: string | null
    promptId?: string | null
    order?: TimelineOrder
  }): void {
    if (filters.kinds !== undefined) kinds.value = filters.kinds
    if (filters.agentId !== undefined) agentId.value = filters.agentId
    if (filters.promptId !== undefined) promptId.value = filters.promptId
    if (filters.order !== undefined) order.value = filters.order
  }

  // --- session ---------------------------------------------------------

  /**
   * Loads `id`'s SessionDetail and makes it the current session. A cache
   * hit (an entry that already has a `session` value — i.e. this id is
   * still one of the LRU's 3 slots) returns immediately without calling the
   * API: this is the whole point of the LRU (PLAN P4-03's sharpest AC —
   * "back-navigation within the LRU does not refetch").
   */
  async function loadSession(id: string): Promise<void> {
    currentId.value = id
    const entry = ensureEntry(id)

    if (entry.session.value !== null) return

    entry.sessionLoading.value = true
    entry.sessionError.value = null
    const client = useApiClient()
    try {
      const result = await unwrap(client.GET('/api/v1/sessions/{id}', { params: { path: { id } } }))
      entry.session.value = result
    } catch (err) {
      entry.session.value = null
      entry.sessionError.value = err instanceof ApiError ? err : toStoreError(err)
    } finally {
      entry.sessionLoading.value = false
    }
  }

  const session = computed(() => currentEntry()?.session.value ?? null)
  const loading = computed(() => currentEntry()?.sessionLoading.value ?? false)
  const error = computed<StoreError>(() => currentEntry()?.sessionError.value ?? null)

  // --- turns -------------------------------------------------------------

  /** Fetches turns once per session; a later call is a no-op unless `force`. Lazy — call on the Timeline/Turns tab's first activation. */
  async function loadTurns(options: { force?: boolean } = {}): Promise<void> {
    const id = currentId.value
    if (!id) return
    const entry = ensureEntry(id)
    if (entry.turnsLoaded && !options.force) return

    entry.turnsLoading.value = true
    entry.turnsError.value = null
    const client = useApiClient()
    try {
      const result = await unwrap(client.GET('/api/v1/sessions/{id}/turns', { params: { path: { id }, query: {} } }))
      entry.turns.value = result.data
      entry.turnsLoaded = true
    } catch (err) {
      entry.turnsError.value = err instanceof ApiError ? err : toStoreError(err)
    } finally {
      entry.turnsLoading.value = false
    }
  }

  const turns = computed(() => currentEntry()?.turns.value ?? [])
  const turnsLoading = computed(() => currentEntry()?.turnsLoading.value ?? false)
  const turnsError = computed<StoreError>(() => currentEntry()?.turnsError.value ?? null)

  // --- tool calls ----------------------------------------------------------

  /** Fetches this session's tool calls once; lazy — call on the Tools tab's first activation (PLAN P4-06). */
  async function loadToolCalls(options: { force?: boolean } = {}): Promise<void> {
    const id = currentId.value
    if (!id) return
    const entry = ensureEntry(id)
    if (entry.toolCallsLoaded && !options.force) return

    entry.toolCallsLoading.value = true
    entry.toolCallsError.value = null
    const client = useApiClient()
    try {
      const result = await unwrap(
        client.GET('/api/v1/sessions/{id}/tool-calls', { params: { path: { id }, query: {} } }),
      )
      entry.toolCalls.value = result.data
      entry.toolCallsLoaded = true
    } catch (err) {
      entry.toolCallsError.value = err instanceof ApiError ? err : toStoreError(err)
    } finally {
      entry.toolCallsLoading.value = false
    }
  }

  const toolCalls = computed(() => currentEntry()?.toolCalls.value ?? [])
  const toolCallsLoading = computed(() => currentEntry()?.toolCallsLoading.value ?? false)
  const toolCallsError = computed<StoreError>(() => currentEntry()?.toolCallsError.value ?? null)

  // --- subagents -----------------------------------------------------------

  /** Fetches the subagent tree once; lazy — call on the Subagents tab's first activation (PLAN P4-05). No `page` on this endpoint — it's one full tree, not paginated. */
  async function loadSubagents(options: { force?: boolean } = {}): Promise<void> {
    const id = currentId.value
    if (!id) return
    const entry = ensureEntry(id)
    if (entry.subagentsLoaded && !options.force) return

    entry.subagentsLoading.value = true
    entry.subagentsError.value = null
    const client = useApiClient()
    try {
      const result = await unwrap(client.GET('/api/v1/sessions/{id}/subagents', { params: { path: { id } } }))
      entry.subagents.value = result.data
      entry.costAttribution.value = result.cost_attribution
      entry.subagentsLoaded = true
    } catch (err) {
      entry.subagentsError.value = err instanceof ApiError ? err : toStoreError(err)
    } finally {
      entry.subagentsLoading.value = false
    }
  }

  const subagents = computed(() => currentEntry()?.subagents.value ?? [])
  const costAttribution = computed(() => currentEntry()?.costAttribution.value ?? null)
  const subagentsLoading = computed(() => currentEntry()?.subagentsLoading.value ?? false)
  const subagentsError = computed<StoreError>(() => currentEntry()?.subagentsError.value ?? null)

  // --- timeline --------------------------------------------------------------

  /**
   * Loads a page of the current session's timeline using the current
   * filters (`kinds`/`agentId`/`promptId`/`order`). `{ reset: true }` (the
   * default on first call for a session) replaces `timelineItems`;
   * otherwise this is `loadMoreTimeline`'s implementation, appending onto
   * the existing page.
   *
   * `raw_events_expired` (surfaced on `session`, not here) is a
   * session-level fact the caller checks before ever calling this — an
   * expired session's timeline endpoint may still return `[]`, and that
   * `[]` must not be confused with "there were never any events".
   */
  async function loadTimeline(options: { reset?: boolean } = {}): Promise<void> {
    const id = currentId.value
    if (!id) return
    const entry = ensureEntry(id)
    const reset = options.reset ?? !entry.timelineLoaded

    entry.timelineLoading.value = true
    entry.timelineError.value = null
    const client = useApiClient()
    try {
      const result = await unwrap(
        client.GET('/api/v1/sessions/{id}/timeline', {
          params: {
            path: { id },
            query: {
              kinds: kinds.value.length > 0 ? kinds.value : undefined,
              agent_id: agentId.value ?? undefined,
              prompt_id: promptId.value ?? undefined,
              order: order.value,
              cursor: reset ? undefined : (entry.timelineNextCursor.value ?? undefined),
            },
          },
        }),
      )
      entry.timelineItems.value = reset ? result.data : [...entry.timelineItems.value, ...result.data]
      entry.timelineNextCursor.value = result.page.next_cursor
      entry.timelineHasMore.value = result.page.has_more
      entry.timelineLoaded = true
    } catch (err) {
      entry.timelineError.value = err instanceof ApiError ? err : toStoreError(err)
    } finally {
      entry.timelineLoading.value = false
    }
  }

  async function loadMoreTimeline(): Promise<void> {
    const entry = currentEntry()
    if (!entry || !entry.timelineHasMore.value || entry.timelineLoading.value) return
    await loadTimeline({ reset: false })
  }

  const timelineItems = computed(() => currentEntry()?.timelineItems.value ?? [])
  const timelineHasMore = computed(() => currentEntry()?.timelineHasMore.value ?? false)
  const timelineLoading = computed(() => currentEntry()?.timelineLoading.value ?? false)
  const timelineError = computed<StoreError>(() => currentEntry()?.timelineError.value ?? null)

  // --- per-event cache -----------------------------------------------------

  /**
   * The detail drawer's data source (PLAN P4-04's EventDetailSheet):
   * `TimelineEvent` (slim, no `attrs`) is what the timeline list holds,
   * `EventDetail` (with `attrs`) is fetched lazily per event and cached by
   * `event_ref` for the lifetime of the session's LRU slot — evicted only
   * when the whole session entry is evicted.
   */
  async function loadEvent(eventRef: string): Promise<EventDetail | null> {
    const id = currentId.value
    if (!id) return null
    const entry = ensureEntry(id)

    const cached = entry.eventCache.get(eventRef)
    if (cached) return cached

    const client = useApiClient()
    const result = await unwrap(client.GET('/api/v1/events/{ref}', { params: { path: { ref: eventRef } } }))
    entry.eventCache.set(eventRef, result)
    return result
  }

  return {
    currentId,
    loadSession,
    session,
    loading,
    error,

    kinds,
    agentId,
    promptId,
    order,
    setTimelineFilters,

    loadTurns,
    turns,
    turnsLoading,
    turnsError,

    loadToolCalls,
    toolCalls,
    toolCallsLoading,
    toolCallsError,

    loadSubagents,
    subagents,
    costAttribution,
    subagentsLoading,
    subagentsError,

    loadTimeline,
    loadMoreTimeline,
    timelineItems,
    timelineHasMore,
    timelineLoading,
    timelineError,

    loadEvent,
  }
})
