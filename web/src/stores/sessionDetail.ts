import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { defineStore } from 'pinia'

import { unwrap } from '@/api/client'
import { useApiClient } from '@/api/context'
import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'
import { useLiveStore } from '@/stores/live'
import type { LiveSubscription } from '@/stores/live'

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
  /**
   * P5-06: mirrors `timelineItems`' `event_ref`s for O(1) dedupe. A live SSE frame and a REST page
   * fetch can both deliver the same `event_ref` (a live event arrives, then a later `loadMoreTimeline`
   * page re-delivers it near a cursor boundary, or vice versa) — without this, checking "is this ref
   * already present" would be a `.some()` linear scan of `timelineItems` on every single incoming
   * live frame, which is exactly the cost a firehose-adjacent feature must not pay per frame. Kept as
   * a plain field (like `eventCache` below), not a ref: nothing renders from the Set directly, so it
   * needs no reactivity of its own — only `timelineItems` does.
   */
  timelineRefs: Set<string>

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
    timelineRefs: new Set(),

    eventCache: new Map(),
  }
}

/**
 * Orders two events by `(ts, seq)` — the same tie-break the server's own keyset pagination uses
 * (SPEC §4.3), so a binary search against server-sorted pages and live-inserted events never
 * disagrees with the server about ordering.
 */
function compareEvents(a: TimelineEvent, b: TimelineEvent): number {
  const tsDelta = new Date(a.ts).getTime() - new Date(b.ts).getTime()
  if (tsDelta !== 0) return tsDelta
  return a.seq - b.seq
}

/**
 * The insertion point for `event` into `arr` (already sorted per `order`) that keeps it sorted.
 * Binary search, not a linear scan: a firehose-adjacent feature inserting one event at a time into a
 * list that can hold a full timeline page must not pay O(n) per insert on top of the O(n) copy
 * `insertEvent` below already pays for immutability.
 */
function findInsertIndex(arr: readonly TimelineEvent[], event: TimelineEvent, order: TimelineOrder): number {
  let lo = 0
  let hi = arr.length
  while (lo < hi) {
    const mid = (lo + hi) >>> 1
    const cmp = compareEvents(arr[mid]!, event)
    const goesAfter = order === 'asc' ? cmp <= 0 : cmp >= 0
    if (goesAfter) lo = mid + 1
    else hi = mid
  }
  return lo
}

/**
 * The one insertion path both REST pages (`loadTimeline`'s append branch) and live SSE frames
 * (`startLive`'s watcher) funnel through — SPEC §1.7 / ticket P5-06's central point: the same
 * `event_ref` can legitimately arrive from both channels (a live event lands, then a later REST page
 * re-delivers it near a cursor boundary — or the reverse order), and out-of-order arrival on the live
 * side is a first-class reality, not an edge case (a reordering proxy, a replayed reconnect window,
 * or simply two vendor processes racing).
 *
 * A naive `timelineItems.value.push(event)` is wrong on both counts: it would duplicate a ref already
 * present (nothing here dedupes), and it would place an out-of-order live event **after** later
 * events already on screen instead of where it actually belongs in `(ts, seq)` order, silently
 * breaking the "chronological" invariant every other reader of `timelineItems` (Timeline.vue's
 * grouping, the duration-bar origin, `collapseEvents`) already depends on.
 *
 * `timelineRefs.has()` is checked first (O(1)) — the whole reason that Set exists alongside the
 * array — so a duplicate is rejected before paying for the binary search or the array copy.
 */
function insertEvent(entry: SessionDetailEntry, event: TimelineEvent, order: TimelineOrder): void {
  if (entry.timelineRefs.has(event.event_ref)) return
  entry.timelineRefs.add(event.event_ref)
  const arr = entry.timelineItems.value
  const index = findInsertIndex(arr, event, order)
  const next = arr.slice()
  next.splice(index, 0, event)
  entry.timelineItems.value = next
}

/** Cap on `docs/PLAN.md` P4-03's "LRU of 3" — one entry per distinct session id visited. */
export const SESSION_DETAIL_LRU_SIZE = 3

/**
 * Cap on the sessionless event cache `loadEvent` falls back to when no session
 * is open — the `/live` firehose, where a user can click through an unbounded
 * number of distinct events, unlike one session's finite timeline. Exported so
 * the eviction test asserts against the real bound rather than a duplicated
 * literal that could drift away from it.
 */
export const ORPHAN_EVENT_CACHE_MAX = 200

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
      if (reset) {
        // A fresh page from the server is already sorted per the requested `order` and carries no
        // refs `timelineItems` previously held — a plain replace, not a merge through `insertEvent`,
        // which exists for the *append* case where live/REST overlap is possible.
        entry.timelineItems.value = result.data
        entry.timelineRefs = new Set(result.data.map((event) => event.event_ref))
      } else {
        // `loadMoreTimeline`'s page: routed through the same idempotent, order-preserving insertion a
        // live frame uses (see `insertEvent`'s doc comment) — a page boundary can re-deliver a ref a
        // live frame already appended, and this is where that dedupe actually happens.
        for (const event of result.data) insertEvent(entry, event, order.value)
      }
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
   * Bounded cache for `loadEvent` calls made with no session open at all —
   * the `/live` firehose, where clicking a feed row must still fill the detail
   * sheet. `GET /api/v1/events/{ref}` is addressed purely by `event_ref` (SPEC
   * §4.1: "there is no lookup by id"), so it never needed a session; the
   * per-entry cache exists only to pick an LRU slot to evict with.
   *
   * Capped because the firehose has no natural bound on how many distinct
   * events a user can click through in one sitting, unlike a single session's
   * timeline. Oldest-inserted is evicted first; an eviction only costs one
   * refetch, since `attrs` for a persisted event never change.
   */
  const orphanEventCache = new Map<string, EventDetail>()

  /**
   * The detail drawer's data source (PLAN P4-04's EventDetailSheet):
   * `TimelineEvent` (slim, no `attrs`) is what the timeline list holds,
   * `EventDetail` (with `attrs`) is fetched lazily per event and cached by
   * `event_ref` for the lifetime of the session's LRU slot — evicted only
   * when the whole session entry is evicted.
   *
   * P5-05 integration gap: this used to `return null` whenever `currentId` was
   * unset, which is *always* the case on `/live` — a firehose has no single
   * current session. So a live-feed row click opened the sheet with the right
   * `event_ref` and permanently blank content. The lookup is by `event_ref`
   * alone, so the sessionless path is served from `orphanEventCache` instead of
   * being refused.
   */
  async function loadEvent(eventRef: string): Promise<EventDetail | null> {
    const id = currentId.value
    const cache = id ? ensureEntry(id).eventCache : orphanEventCache

    const cached = cache.get(eventRef)
    if (cached) return cached

    const client = useApiClient()
    const result = await unwrap(client.GET('/api/v1/events/{ref}', { params: { path: { ref: eventRef } } }))
    if (!id && cache.size >= ORPHAN_EVENT_CACHE_MAX) {
      const oldest = cache.keys().next()
      if (!oldest.done) cache.delete(oldest.value)
    }
    cache.set(eventRef, result)
    return result
  }

  // --- live (PLAN.md P5-06 / Phase-5 exit criterion 2) ----------------------

  /**
   * Gates whether an incoming live event is actually appended — the header toggle's "off" state
   * (`setLiveEnabled(false)`). Deliberately *not* `liveStore.pause()`: that pause is tab-wide (SPEC
   * P5-04's own doc comment on it), so calling it from a per-view toggle would also freeze e.g. a
   * `LiveView` firehose feed some other tab/route is reading from the very same ring buffer — a side
   * effect this view has no business causing. Keeping the subscription itself open while "off" (see
   * `startLive` below) is the other half of that choice: a toggle a user expects to flip back on
   * without a reconnect/replay round-trip, and one that must never interrupt `loadMoreTimeline` or
   * the KPI strip's own session-frame updates (the AC: "toggling live off ... keeps the REST view
   * fully usable").
   */
  const liveEnabled = ref(true)

  function setLiveEnabled(enabled: boolean): void {
    liveEnabled.value = enabled
  }

  let liveSessionId: string | null = null
  let liveSubscription: LiveSubscription | null = null
  let unregisterReset: (() => void) | null = null
  let stopEventsWatch: (() => void) | null = null
  let stopSessionWatch: (() => void) | null = null

  /**
   * Applies one live `event` frame to `entry`'s timeline, or drops it — three independent reasons an
   * arriving frame must not appear, each documented because a silent drop is otherwise
   * indistinguishable from a bug:
   *
   * - **Wrong session** (`event.session_id !== id`): the firehose can be the tab's active topic when
   *   the user arrives here from `/live` (P5-05's own follow link, or simply a stack transition still
   *   settling — see `stores/live.ts`'s subscription-stack doc comment), so frames for *other*
   *   sessions genuinely reach this watcher. The ring buffer is also never cleared on a topic switch
   *   (only a `reset` frame or `.clear()` does that), so stale firehose entries can still be sitting
   *   in it at the moment this session's own subscription opens.
   * - **Filtered kind/agent/prompt**: the store's own timeline filters (`kinds`/`agentId`/`promptId`)
   *   are enforced server-side for a REST page, but a live frame bypasses REST entirely — without this
   *   check, live mode would silently *un-filter* rows the user explicitly filtered out.
   * - **Already known** (`insertEvent`'s own `timelineRefs` check): the dedupe this ticket is
   *   centrally about — see `insertEvent`'s doc comment.
   */
  function applyLiveEvent(id: string, entry: SessionDetailEntry, event: TimelineEvent): void {
    if (event.session_id !== id) return
    if (kinds.value.length > 0 && !kinds.value.includes(event.kind)) return
    if (agentId.value !== null && event.agent_id !== agentId.value) return
    if (promptId.value !== null && event.prompt_id !== promptId.value) return
    insertEvent(entry, event, order.value)
  }

  /**
   * Subscribes this store to `id`'s own SSE stream and wires the two frame types it cares about.
   * Idempotent for the same id (a route change from `/sessions/a?tab=x` to `/sessions/a?tab=y` must
   * not tear down and reopen the connection), and always tears down any *previous* id's subscription
   * first — `stopLive` — so at most one session-scoped subscription is ever open regardless of how
   * many ids this LRU has cached (exit criterion 6's "exactly one EventSource" extends to "exactly one
   * *subscription* stack entry from this store", the same property `liveStore`'s own stack guarantees
   * for the tab as a whole).
   *
   * `SessionDetailView.vue` does not remount across a `:id` change (it watches `props.id` and calls
   * `loadSession` reactively), so a `watch(currentId)`-shaped teardown is essential here — an
   * `onMounted`/`onBeforeUnmount` pair alone would leak the previous id's subscription forever.
   */
  function startLive(id: string): void {
    if (liveSessionId === id) return
    stopLive()

    const live = useLiveStore()
    const entry = ensureEntry(id)
    liveSessionId = id

    const subscription = live.subscribe({ kind: 'session', id })
    liveSubscription = subscription

    // SPEC §5.2: a `reset` means local stream-derived state is provably incomplete — the only honest
    // recovery is the REST refetch this view already knows how to do, exactly like `liveStore` itself
    // has no REST client of its own and only ever calls back (see its own `onReset` doc comment).
    unregisterReset = live.onReset(() => {
      void loadTimeline({ reset: true })
    })

    // Not `{ immediate: true }`: at subscribe time `live.events` may still hold frames from whatever
    // topic was active *before* this one (see `applyLiveEvent`'s doc comment) with nothing new to
    // react to yet — this watcher's job is frames that arrive *after* subscribing, not a backlog scan.
    // Re-scans the *whole* current buffer on every change (not just what changed since last time)
    // rather than tracking a cursor into it: `liveStore`'s ring buffer can wrap (evicting its oldest
    // entries) independently of how often this watcher runs, which would desync a length- or
    // index-based cursor; `insertEvent`'s O(1) `timelineRefs` check makes a full rescan of the
    // (≤`RING_CAPACITY` = 2000) buffer cheap enough that correctness is worth more here than the
    // saved iterations.
    stopEventsWatch = watch(
      () => live.events,
      (events) => {
        if (!liveEnabled.value) return
        for (const event of events) applyLiveEvent(id, entry, event)
      },
    )

    // The KPI strip's live half (exit criterion 2): a `session` frame is a `SessionSummary`, a subset
    // of the `SessionDetail` this entry holds, so merging it in (rather than replacing wholesale)
    // keeps the detail-only fields (`decision_summary`, `raw_events_expired`, ...) intact — exactly
    // what lets `SessionKpiStrip.vue` need no changes of its own (it only ever reads the
    // `SessionSummary`-shaped subset). `.get(id)` (not iterating `live.sessions`) tracks a dependency
    // on that one key, so this only re-runs for this session's own frames. Not gated by `liveEnabled`:
    // matches `liveStore.pause()`'s own documented split between the raw feed and session/stats
    // projections — see `liveEnabled`'s doc comment above.
    stopSessionWatch = watch(
      () => live.sessions.get(id),
      (frame) => {
        if (!frame || !entry.session.value) return
        entry.session.value = { ...entry.session.value, ...frame }
      },
    )
  }

  /** Tears down whatever this store's own live subscription currently is — a no-op if none is open. */
  function stopLive(): void {
    stopEventsWatch?.()
    stopEventsWatch = null
    stopSessionWatch?.()
    stopSessionWatch = null
    unregisterReset?.()
    unregisterReset = null
    liveSubscription?.close()
    liveSubscription = null
    liveSessionId = null
  }

  // Defensive backstop, mirroring `useApi.ts`'s own `onScopeDispose` guard: the view calling
  // `stopLive()` on unmount is the primary teardown path (asserted directly by the exit-criterion-6
  // test), this only covers the store itself being disposed without that ever happening.
  onScopeDispose(() => {
    stopLive()
  })

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

    liveEnabled,
    setLiveEnabled,
    startLive,
    stopLive,
  }
})
