import { computed, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'

import type { components } from '@/api/schema'
import {
  backoffDelay,
  createEventSource,
  EVENT_SOURCE_CONNECTING,
  RING_CAPACITY,
  streamUrl,
} from '@/lib/sse'
import type { EventSourceLike, LiveTopic } from '@/lib/sse'

export type { LiveTopic } from '@/lib/sse'

export type TimelineEvent = components['schemas']['TimelineEvent']
export type SessionSummary = components['schemas']['SessionSummary']
export type StreamStatsFrame = components['schemas']['StreamStatsFrame']
export type StreamResetFrame = components['schemas']['StreamResetFrame']
export type StreamLagFrame = components['schemas']['StreamLagFrame']

/**
 * A `TimelineEvent` as retained in the live ring buffer — always carrying
 * `receivedAt`, this tab's own wall-clock the instant `handleEventFrame`
 * processed the frame. The wire payload (SPEC §4.3) has no such field, only
 * the vendor-emitted `ts`, and `ts` is not reliable arrival order: two
 * frames can legitimately land out of `ts` order (clock skew, multi-source
 * fan-in), which is exactly why a firehose that displays `ts` down a
 * newest-arrival-first column reads as jumbled. `receivedAt` is stamped
 * once, here, the moment a frame actually lands — monotonic by
 * construction, since frames are stamped in the same order the ring buffer
 * stores them (`EventRing`'s own doc).
 *
 * Optional at the type level even though this store always sets it: a
 * caller of `LiveFeed.vue` (its own tests included) can supply plain
 * `TimelineEvent`s that never went through this store, and that component
 * falls back to the event's own `ts` when `receivedAt` is absent.
 */
export interface LiveTimelineEvent extends TimelineEvent {
  receivedAt?: string
}

export type LiveStatus = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed'

/** A `subscribe()` handle. `close()` is idempotent — a component that unmounts twice (StrictMode-style double effects, or a defensive cleanup) can't double-remove someone else's entry. */
export interface LiveSubscription {
  close(): void
}

/**
 * Fixed-capacity circular buffer for `event` frames (AC: "the ring buffer never exceeds 2000 under
 * 5000 pushed frames and keeps the newest"). Writes are O(1) — a circular index into a preallocated
 * slot array, never a shift/splice — so the buffer's cost doesn't grow with how long the tab has
 * been open, only with `RING_CAPACITY` itself. Reading it back out in order is unavoidably
 * O(RING_CAPACITY), but that's paid at most once per Vue reactive flush (see `events` below, which
 * memoises on a version counter), not once per push — the distinction matters because `stats`'s
 * `events_per_sec` can spike well above one frame per render tick.
 */
class EventRing {
  private readonly slots: (LiveTimelineEvent | undefined)[]
  private writeIndex = 0
  private filled = 0

  constructor(private readonly capacity: number) {
    this.slots = new Array(capacity)
  }

  push(frame: LiveTimelineEvent): void {
    this.slots[this.writeIndex] = frame
    this.writeIndex = (this.writeIndex + 1) % this.capacity
    if (this.filled < this.capacity) this.filled += 1
  }

  clear(): void {
    this.writeIndex = 0
    this.filled = 0
    this.slots.fill(undefined)
  }

  /**
   * Oldest-first (chronological) snapshot of what's currently retained — the same convention
   * `collapseEvents.ts` and the session timeline already use elsewhere in the app. A "live feed"
   * view that wants newest-on-top (PLAN.md P5-05) reverses its own render order; the store stays the
   * one honest, order-stable source of truth.
   */
  toArray(): LiveTimelineEvent[] {
    if (this.filled < this.capacity) return this.slots.slice(0, this.filled) as LiveTimelineEvent[]
    return [...this.slots.slice(this.writeIndex), ...this.slots.slice(0, this.writeIndex)] as LiveTimelineEvent[]
  }
}

function topicsEqual(a: LiveTopic, b: LiveTopic): boolean {
  if (a.kind !== b.kind) return false
  if (a.kind === 'session' && b.kind === 'session') return a.id === b.id
  if (a.kind === 'firehose' && b.kind === 'firehose') {
    if (a.project !== b.project || a.vendor !== b.vendor) return false
    const left = [...(a.kinds ?? [])].sort()
    const right = [...(b.kinds ?? [])].sort()
    return left.length === right.length && left.every((v, i) => v === right[i])
  }
  return false
}

/**
 * Sole owner of the tab's `EventSource` (PLAN.md P5-04 / exit criterion 6: "exactly one EventSource
 * per browser tab regardless of navigation").
 *
 * Reference counting is deliberately **not** "one topic + a refcount": during a route transition two
 * views can be mounted at once (the outgoing `LiveView` and the incoming `SessionDetailView`), so
 * subscriptions form a *stack* instead. The most recently pushed entry is always the active topic;
 * `close()` removes its own entry by id (not by position — a component may unsubscribe out of
 * order, e.g. the outgoing view's unmount hook firing after the incoming view's mount) and whatever
 * is now on top becomes active again. The connection opens when the stack becomes non-empty,
 * re-opens when the active topic changes, and closes when the stack empties — at every instant there
 * is at most one live `EventSource`.
 *
 * "Without dropping frames" on a topic switch does not mean literally holding one socket open across
 * two different topics (a single `EventSource` can't reattach to a different URL) — it means the
 * re-opened connection always carries `?after=<lastEventRef>`, so the server replays whatever gap
 * opened between closing the old connection and the new one reaching `open` (SPEC §5.2).
 */
export const useLiveStore = defineStore('live', () => {
  const status = ref<LiveStatus>('idle')
  const paused = ref(false)
  const bufferedWhilePaused = ref(0)
  const lastEventRef = ref<string | null>(null)
  const malformedFrames = ref(0)
  /** Cumulative `dropped` reported by `lag` frames — the *client's* count of frames it never received. Distinct from `stats.dropped_total`, which is the server's own pipeline-wide counter (see `stats` below). */
  const droppedTotal = ref(0)
  const sessions = ref(new Map<string, SessionSummary>())
  const stats = shallowRef<StreamStatsFrame | null>(null)
  /** The topic currently driving the live `EventSource`, i.e. the top of the subscription stack — `null` when nobody is subscribed. Exposed for a future "watching: firehose / session X" indicator (P5-05/P5-06) and asserted directly by exit-criterion-6 tests. */
  const activeTopic = ref<LiveTopic | null>(null)
  /** Total `EventSource` instances this tab has ever created (not how many are currently live — that's always 0 or 1). Lets a test assert "exactly one" without reaching into the fake factory itself. */
  const eventSourcesCreated = ref(0)

  const ring = new EventRing(RING_CAPACITY)
  const ringVersion = ref(0)
  /**
   * `computed`, not a `shallowRef` reassigned on every push: reassigning a ~2000-element array on
   * every incoming frame (as `sessionDetail.ts`'s paginated lists do on each fetch) would make the
   * buffer's cost scale with feed volume, which is exactly what a firehose under load must not do.
   * `ringVersion` is the only thing this computed depends on, so materialising the array (the one
   * unavoidably O(RING_CAPACITY) step) happens at most once per reactive flush, regardless of how
   * many frames arrived since the last one.
   */
  const events = computed<LiveTimelineEvent[]>(() => {
    void ringVersion.value
    return ring.toArray()
  })

  const resetCallbacks = new Set<() => void>()

  interface StackEntry {
    id: number
    topic: LiveTopic
  }
  const stack: StackEntry[] = []
  let nextSubscriptionId = 1

  let es: EventSourceLike | null = null
  let currentOpenTopic: LiveTopic | null = null
  let reconnectAttempt = 0
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null

  function clearReconnectTimer(): void {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  function teardownConnection(): void {
    clearReconnectTimer()
    if (es) {
      es.onopen = null
      es.onerror = null
      es.close()
      es = null
    }
  }

  /**
   * Parses one frame's `data:` payload, or counts and swallows a malformed one. Never lets a
   * `JSON.parse` failure escape into the caller's `addEventListener` callback: an exception thrown
   * from inside an `EventSource` listener is silently swallowed by the browser, which would make a
   * parse bug invisible instead of visible-but-handled.
   */
  function parseFrame<T>(ev: MessageEvent, frameType: string): T | undefined {
    try {
      return JSON.parse(ev.data as string) as T
    } catch (err) {
      malformedFrames.value += 1
      console.error(`live: malformed SSE '${frameType}' frame payload`, err, ev.data)
      return undefined
    }
  }

  function handleEventFrame(ev: MessageEvent): void {
    const payload = parseFrame<TimelineEvent>(ev, 'event')
    if (!payload) return
    // Read from the parsed body's own `event_ref` field (SPEC §5.1: it's carried both as the SSE
    // `id:` line and inside the JSON), not `ev.lastEventId` — the latter depends on the real
    // `EventSource`/`MessageEvent` setting it correctly, which the fake used in tests has no reason
    // to implement, and jsdom doesn't implement `EventSource` at all.
    lastEventRef.value = payload.event_ref
    // Tracked even while paused: a reconnect that happens mid-pause must still resume from the true
    // last-seen position, not a stale one from before the pause started.
    if (paused.value) {
      bufferedWhilePaused.value += 1
      return
    }
    // Stamped here, not earlier: this is the instant the frame is actually accepted onto the ring
    // (a paused tab discards the frame above without ever storing it, so it never needs a receive
    // stamp it would never show).
    ring.push({ ...payload, receivedAt: new Date().toISOString() })
    ringVersion.value += 1
  }

  function handleSessionFrame(ev: MessageEvent): void {
    const payload = parseFrame<SessionSummary>(ev, 'session')
    if (!payload) return
    // Not gated by `paused`: pause is documented as freezing the raw event feed for reading, not the
    // session/stats projections a KPI strip keeps rendering regardless.
    sessions.value.set(payload.id, payload)
  }

  function handleStatsFrame(ev: MessageEvent): void {
    const payload = parseFrame<StreamStatsFrame>(ev, 'stats')
    if (!payload) return
    stats.value = payload
  }

  /**
   * `reset` and `lag` both mean the same thing from the client's point of view — the local state is
   * provably incomplete (a replay window was exceeded, or the server's per-subscriber buffer
   * overflowed and silently dropped frames before they reached us) — so the only honest recovery is
   * the same for both: hand off to whatever REST refetch the subscribing view registered, rather
   * than trying to patch the gap from partial local state.
   */
  function triggerRefetch(): void {
    for (const cb of resetCallbacks) cb()
  }

  /**
   * SPEC §5.2 on a `reset`: "the client drops local state and refetches via REST". Every piece of
   * local state derived from the stream goes, `sessions` included — a projection snapshot that was
   * true when it arrived is still a snapshot from before an acknowledged gap, and the REST refetch
   * the callback triggers is what replaces it with something whose completeness is knowable. Only
   * the lifetime diagnostic counters survive (`droppedTotal`, `malformedFrames`): they describe the
   * connection's health, not what is currently on screen, so resetting them would erase the very
   * evidence that a gap happened.
   */
  function handleResetFrame(ev: MessageEvent): void {
    const payload = parseFrame<StreamResetFrame>(ev, 'reset')
    if (!payload) return
    ring.clear()
    ringVersion.value += 1
    stats.value = null
    sessions.value.clear()
    triggerRefetch()
  }

  function handleLagFrame(ev: MessageEvent): void {
    const payload = parseFrame<StreamLagFrame>(ev, 'lag')
    if (!payload) return
    droppedTotal.value += payload.dropped
    triggerRefetch()
  }

  /**
   * SPEC §5.1: sent once on graceful server shutdown "so the browser reconnects instead of
   * erroring". Closed proactively here (rather than left to the browser's own error/retry) so the
   * reconnect always goes through this module's `?after=` path instead of depending on whether the
   * server's connection teardown happens to leave the browser in `CONNECTING` or `CLOSED`. A planned
   * restart is not an escalating failure, so the attempt counter resets first — the next reconnect
   * uses the smallest backoff, not wherever an unrelated prior failure streak had reached.
   */
  function handleShutdownFrame(instance: EventSourceLike): void {
    reconnectAttempt = 0
    status.value = 'reconnecting'
    instance.onopen = null
    instance.onerror = null
    instance.close()
    if (es === instance) es = null
    scheduleReconnect()
  }

  function scheduleReconnect(): void {
    clearReconnectTimer()
    const attempt = reconnectAttempt
    reconnectAttempt += 1
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      if (!currentOpenTopic) return
      openEventSource(currentOpenTopic, lastEventRef.value)
    }, backoffDelay(attempt))
  }

  /**
   * Distinguishes the two reconnect paths SPEC §5.2 provides (ticket P5-04's central subtlety):
   *   - `CONNECTING` — the browser is already retrying by itself, carrying `Last-Event-ID` on its
   *     own. Opening a second `EventSource` here is exactly how a tab ends up with two live
   *     connections at once (exit criterion 6) — only the exposed connection state changes.
   *   - anything else (`CLOSED`) — the browser gave up. From here the reconnect is ours: schedule
   *     one after `backoffDelay`, and open a *new* `EventSource` carrying `?after=<lastEventRef>`,
   *     since a fresh connection we open ourselves has no `Last-Event-ID` header to fall back on.
   */
  function onEventSourceError(instance: EventSourceLike): void {
    if (instance.readyState === EVENT_SOURCE_CONNECTING) {
      status.value = 'reconnecting'
      return
    }
    status.value = 'reconnecting'
    scheduleReconnect()
  }

  function attachListeners(instance: EventSourceLike): void {
    instance.onopen = () => {
      if (instance !== es) return
      reconnectAttempt = 0
      status.value = 'open'
    }
    instance.onerror = () => {
      if (instance !== es) return
      onEventSourceError(instance)
    }
    instance.addEventListener('event', (ev) => {
      if (instance === es) handleEventFrame(ev)
    })
    instance.addEventListener('session', (ev) => {
      if (instance === es) handleSessionFrame(ev)
    })
    instance.addEventListener('stats', (ev) => {
      if (instance === es) handleStatsFrame(ev)
    })
    instance.addEventListener('lag', (ev) => {
      if (instance === es) handleLagFrame(ev)
    })
    instance.addEventListener('reset', (ev) => {
      if (instance === es) handleResetFrame(ev)
    })
    instance.addEventListener('shutdown', () => {
      if (instance === es) handleShutdownFrame(instance)
    })
  }

  function openEventSource(topic: LiveTopic, after: string | null): void {
    status.value = 'connecting'
    const instance = createEventSource(streamUrl(topic, { after }))
    eventSourcesCreated.value += 1
    es = instance
    attachListeners(instance)
  }

  /** A deliberate (re)connect — a fresh subscribe or a topic switch — as opposed to a failure retry: always resets the backoff attempt counter. */
  function connect(topic: LiveTopic): void {
    teardownConnection()
    currentOpenTopic = topic
    reconnectAttempt = 0
    openEventSource(topic, lastEventRef.value)
  }

  function reconcileConnection(): void {
    const top = stack.length ? stack[stack.length - 1] : undefined
    activeTopic.value = top?.topic ?? null

    if (!top) {
      teardownConnection()
      currentOpenTopic = null
      status.value = 'closed'
      return
    }
    if (!currentOpenTopic || !topicsEqual(top.topic, currentOpenTopic)) {
      connect(top.topic)
    }
  }

  /** Pushes a new subscription onto the stack and makes it the active topic (see the store's own doc comment for why a stack, not a refcount). */
  function subscribe(topic: LiveTopic): LiveSubscription {
    const id = nextSubscriptionId++
    stack.push({ id, topic })
    reconcileConnection()

    let closed = false
    return {
      close(): void {
        if (closed) return
        closed = true
        const index = stack.findIndex((entry) => entry.id === id)
        if (index !== -1) stack.splice(index, 1)
        reconcileConnection()
      },
    }
  }

  /** Stops the raw event feed from mutating `events`, without touching the connection — SPEC's "a stalled subscriber must not slow ingestion" is a server-side guarantee (SPEC §5.3); this is the client-side complement: a paused *tab* still receives frames (so `lastEventRef` stays current) but stops re-rendering on each one. */
  function pause(): void {
    paused.value = true
  }

  function resume(): void {
    paused.value = false
    bufferedWhilePaused.value = 0
  }

  /** Manually empties the buffer (a "clear feed" UI action) — distinct from a `reset` frame's automatic clear; does not touch the lifetime diagnostic counters (`droppedTotal`, `malformedFrames`), which describe the connection's health, not what's currently visible. */
  function clear(): void {
    ring.clear()
    ringVersion.value += 1
  }

  /** Registers a refetch callback for `reset`/`lag` frames; returns an unregister function. The store can't know how each view refetches (sessions list page vs. one session's detail), so it only ever calls back, never fetches itself. */
  function onReset(cb: () => void): () => void {
    resetCallbacks.add(cb)
    return () => resetCallbacks.delete(cb)
  }

  return {
    status,
    events,
    sessions,
    stats,
    droppedTotal,
    paused,
    bufferedWhilePaused,
    lastEventRef,
    malformedFrames,
    activeTopic,
    eventSourcesCreated,
    pause,
    resume,
    clear,
    subscribe,
    onReset,
  }
})
