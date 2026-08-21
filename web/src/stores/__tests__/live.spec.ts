import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { backoffDelay, resetEventSourceFactory, setEventSourceFactory } from '@/lib/sse'
import type { EventSourceLike } from '@/lib/sse'
import { listSessions200Default } from '@/test/fixtures'
import { makeTimelineEvent } from '@/test/fixtures.extra'

import { useLiveStore } from '../live'

/** Structural fake for `EventSourceLike` (P5-04's whole reason for the interface being structural instead of `extends EventSource`) — never touches the network, and lets a test drive `onopen`/`onerror`/frames by hand. */
class FakeEventSource implements EventSourceLike {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2

  readyState = FakeEventSource.CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  closed = false
  private readonly listeners = new Map<string, ((ev: MessageEvent) => void)[]>()

  constructor(public readonly url: string) {}

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? []
    list.push(listener)
    this.listeners.set(type, list)
  }

  close(): void {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }

  open(): void {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.(new Event('open'))
  }

  errorConnecting(): void {
    this.readyState = FakeEventSource.CONNECTING
    this.onerror?.(new Event('error'))
  }

  errorClosed(): void {
    this.readyState = FakeEventSource.CLOSED
    this.onerror?.(new Event('error'))
  }

  /** Dispatches a frame with a JSON-serialised body, as the real wire format does (SPEC §5.1). */
  emit(type: string, data: unknown): void {
    this.emitRaw(type, JSON.stringify(data))
  }

  /** Dispatches a frame with a raw string body — used to simulate a malformed (non-JSON) payload. */
  emitRaw(type: string, rawData: string): void {
    const ev = new MessageEvent(type, { data: rawData })
    for (const listener of this.listeners.get(type) ?? []) listener(ev)
  }
}

let instances: FakeEventSource[] = []

function fakeFactory(url: string): EventSourceLike {
  const instance = new FakeEventSource(url)
  instances.push(instance)
  return instance
}

function liveInstances(): FakeEventSource[] {
  return instances.filter((i) => !i.closed)
}

beforeEach(() => {
  instances = []
  setEventSourceFactory(fakeFactory)
  setActivePinia(createPinia())
})

afterEach(() => {
  resetEventSourceFactory()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('useLiveStore — reference counting (exit criterion 6: exactly one EventSource per tab)', () => {
  it('1. two components subscribing to the same topic yield exactly one EventSource', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    store.subscribe({ kind: 'firehose' })
    expect(instances.length).toBe(1)
  })

  it('2. the last unsubscribe closes it; an intermediate unsubscribe does not', () => {
    const store = useLiveStore()
    const a = store.subscribe({ kind: 'firehose' })
    const b = store.subscribe({ kind: 'firehose' })

    a.close()
    expect(instances[0]!.closed).toBe(false)

    b.close()
    expect(instances[0]!.closed).toBe(true)
  })

  it('3. navigation-shaped: firehose -> session -> close(firehose) stays at one live EventSource, ends on the session topic', () => {
    const store = useLiveStore()
    const firehose = store.subscribe({ kind: 'firehose' })
    expect(liveInstances().length).toBe(1)

    store.subscribe({ kind: 'session', id: 'sess-1' })
    expect(liveInstances().length).toBe(1)

    firehose.close()
    expect(liveInstances().length).toBe(1)
    expect(store.activeTopic).toEqual({ kind: 'session', id: 'sess-1' })
  })
})

describe('useLiveStore — reconnect', () => {
  it('4. an error with readyState CLOSED reconnects with increasing delays capped at 30s', async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(1)
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })

    const delays = [0, 1, 2, 3, 4, 5].map((attempt) => backoffDelay(attempt))
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 30000])

    for (const delay of delays) {
      const before = instances.length
      instances[instances.length - 1]!.errorClosed()
      expect(store.status).toBe('reconnecting')

      await vi.advanceTimersByTimeAsync(delay - 1)
      expect(instances.length).toBe(before)

      await vi.advanceTimersByTimeAsync(1)
      expect(instances.length).toBe(before + 1)
    }
  })

  it('5. an error with readyState CONNECTING creates no new EventSource', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    expect(instances.length).toBe(1)

    instances[0]!.errorConnecting()

    expect(instances.length).toBe(1)
    expect(store.status).toBe('reconnecting')
    expect(instances[0]!.closed).toBe(false)
  })

  it('10. a reconnect URL carries ?after=<last event_ref>; a first connect carries none', async () => {
    vi.useFakeTimers()
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    expect(instances[0]!.url).not.toContain('after=')

    instances[0]!.emit('event', makeTimelineEvent({ event_ref: 'ref-xyz' }))
    expect(store.lastEventRef).toBe('ref-xyz')

    instances[0]!.errorClosed()
    await vi.runOnlyPendingTimersAsync()

    expect(instances[1]!.url).toContain('after=ref-xyz')
  })

  it('11. a shutdown frame reconnects rather than entering a terminal closed state', async () => {
    vi.useFakeTimers()
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })

    instances[0]!.emit('shutdown', {})
    expect(store.status).toBe('reconnecting')
    expect(instances[0]!.closed).toBe(true)

    await vi.runOnlyPendingTimersAsync()
    expect(instances.length).toBe(2)
    expect(instances[1]!.closed).toBe(false)
    expect(store.status).not.toBe('closed')
  })
})

describe('useLiveStore — reset / lag', () => {
  // SPEC §5.2: on a reset "the client drops local state and refetches via REST" — so every piece of
  // stream-derived state goes, the per-session projection cache included. A session snapshot that
  // survived a reset would be a pre-gap reading rendered as though it were current, the same class
  // of dishonesty the reset frame exists to announce. The lifetime diagnostic counters deliberately
  // survive: they are the evidence that the gap happened at all.
  it('6. a reset frame clears the buffer, session cache and stats, and calls the refetch callback exactly once', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    instances[0]!.emit('event', makeTimelineEvent({}))
    instances[0]!.emit('session', { ...listSessions200Default.data[0]!, id: 'sess-pre-reset' })
    instances[0]!.emit('stats', { events_per_sec: 3, active_sessions: 1, queue_depth: 0, ingest_lag_ms: 7, dropped_total: 0 })
    instances[0]!.emit('lag', { dropped: 4 })
    expect(store.events.length).toBe(1)
    expect(store.sessions.size).toBe(1)
    expect(store.stats).not.toBeNull()

    const cb = vi.fn()
    store.onReset(cb)
    instances[0]!.emit('reset', { reason: 'replay_window_exceeded', from: '2026-08-18T00:00:00.000Z' })

    expect(store.events.length).toBe(0)
    expect(store.sessions.size).toBe(0)
    expect(store.stats).toBeNull()
    expect(cb).toHaveBeenCalledTimes(1)
    // The lag frame's own refetch fired before the callback was registered, so only the reset's
    // counts here — but its dropped tally must survive the reset.
    expect(store.droppedTotal).toBe(4)
  })

  it('7. a lag frame triggers the refetch callback and accumulates droppedTotal', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    const cb = vi.fn()
    store.onReset(cb)

    instances[0]!.emit('lag', { dropped: 5 })
    instances[0]!.emit('lag', { dropped: 3 })

    expect(store.droppedTotal).toBe(8)
    expect(cb).toHaveBeenCalledTimes(2)
  })
})

describe('useLiveStore — ring buffer', () => {
  it('8. never exceeds 2000 under 5000 pushed frames and keeps the newest', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })

    for (let i = 0; i < 5000; i++) {
      instances[0]!.emit('event', makeTimelineEvent({ event_ref: `ref-${i}` }))
    }

    expect(store.events.length).toBe(2000)
    expect(store.events[0]!.event_ref).toBe('ref-3000')
    expect(store.events[store.events.length - 1]!.event_ref).toBe('ref-4999')
  })

  it('9. paused stops buffer mutation but keeps the connection alive', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    instances[0]!.open()

    store.pause()
    instances[0]!.emit('event', makeTimelineEvent({}))
    instances[0]!.emit('event', makeTimelineEvent({}))

    expect(store.events.length).toBe(0)
    expect(store.bufferedWhilePaused).toBe(2)
    expect(store.status).toBe('open')
    expect(instances[0]!.closed).toBe(false)

    store.resume()
    expect(store.bufferedWhilePaused).toBe(0)
  })
})

describe('useLiveStore — session/stats/malformed frames', () => {
  it('12. session and stats frames update state; a malformed payload increments malformedFrames without throwing', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })

    const session = { ...listSessions200Default.data[0]!, id: 'sess-live' }
    instances[0]!.emit('session', session)
    expect(store.sessions.get('sess-live')).toEqual(session)

    const stats = { events_per_sec: 1, active_sessions: 1, queue_depth: 0, ingest_lag_ms: 5, dropped_total: 0 }
    instances[0]!.emit('stats', stats)
    expect(store.stats).toEqual(stats)

    expect(() => instances[0]!.emitRaw('event', '{not valid json')).not.toThrow()
    expect(store.malformedFrames).toBe(1)
  })
})

describe('useLiveStore — clear() and pending-reconnect cancellation', () => {
  // `clear()` is the "clear feed" UI action, deliberately distinct from a
  // `reset` frame: it empties what is on screen but must NOT erase the
  // lifetime diagnostic counters, which are the record that data was lost.
  it('clear() empties the buffer but keeps droppedTotal and malformedFrames', () => {
    const store = useLiveStore()
    store.subscribe({ kind: 'firehose' })
    instances[0]!.open()
    instances[0]!.emit('event', makeTimelineEvent({ event_ref: 'ref-a' }))
    instances[0]!.emit('event', makeTimelineEvent({ event_ref: 'ref-b' }))
    instances[0]!.emit('lag', { dropped: 3 })
    instances[0]!.emitRaw('event', '{broken')
    expect(store.events.length).toBe(2)

    store.clear()

    expect(store.events.length).toBe(0)
    expect(store.droppedTotal).toBe(3)
    expect(store.malformedFrames).toBe(1)
    expect(store.status).toBe('open')
  })

  // A scheduled reconnect must be cancelled when the last subscriber goes away,
  // or the timer fires against a torn-down store and reopens a connection
  // nobody asked for — an EventSource leak that only shows up after a
  // navigation that happened to race a network blip.
  it('unsubscribing cancels a pending reconnect timer instead of letting it fire', () => {
    vi.useFakeTimers()
    try {
      const store = useLiveStore()
      const handle = store.subscribe({ kind: 'firehose' })
      const created = instances.length

      instances[0]!.errorClosed()
      expect(store.status).toBe('reconnecting')

      handle.close()
      vi.advanceTimersByTime(60_000)

      expect(instances.length).toBe(created)
      expect(store.status).not.toBe('open')
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('useLiveStore — firehose topic identity', () => {
  // `sameTopic` compares the firehose's `kinds` filter by value, order-insensitively.
  // It decides whether a new subscription reopens the connection, so getting it wrong
  // is either a dropped stream on every no-op subscribe, or a stale filter that keeps
  // serving events the caller asked not to receive.
  it('re-subscribing to an identical firehose filter reuses the connection; a different one reopens it', () => {
    const store = useLiveStore()
    const first = store.subscribe({ kind: 'firehose', kinds: ['tool.pre', 'llm.request'] })
    expect(instances.length).toBe(1)

    // Same set, different order — must count as the same topic.
    const second = store.subscribe({ kind: 'firehose', kinds: ['llm.request', 'tool.pre'] })
    expect(instances.length).toBe(1)

    // A genuinely different filter must reopen.
    const third = store.subscribe({ kind: 'firehose', kinds: ['tool.result'] })
    expect(instances.length).toBe(2)

    third.close()
    second.close()
    first.close()
  })

  // Same length, different values — the branch a length-only comparison gets
  // wrong, leaving the connection serving a filter the caller replaced.
  it('a same-length but different firehose kinds filter reopens the connection', () => {
    const store = useLiveStore()
    const first = store.subscribe({ kind: 'firehose', kinds: ['tool.pre'] })
    expect(instances.length).toBe(1)

    const second = store.subscribe({ kind: 'firehose', kinds: ['tool.result'] })
    expect(instances.length).toBe(2)

    second.close()
    first.close()
  })
})
