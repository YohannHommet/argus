/**
 * Transport primitives for Argus's live SSE feed (SPEC §5, ticket P5-04).
 * Deliberately free of Pinia/Vue reactivity: `stores/live.ts` owns *when*
 * to open/close a connection and how to react to frames; this module owns
 * *how* a connection is described (URL) and retried (backoff), so both
 * halves are unit-testable without a DOM `EventSource` — see
 * `setEventSourceFactory` below.
 */

/**
 * Minimal structural subset of `EventSource` the store uses. Structural,
 * not `extends EventSource`, so a test can hand in a plain object that
 * never touches the network — the whole point of the P5-04 AC ("tests
 * with a fake EventSource").
 */
export interface EventSourceLike {
  readonly readyState: number
  addEventListener(type: string, listener: (ev: MessageEvent) => void): void
  close(): void
  onopen: ((ev: Event) => void) | null
  onerror: ((ev: Event) => void) | null
}

export type EventSourceFactory = (url: string) => EventSourceLike

/**
 * WHATWG `EventSource.readyState` values. Hardcoded rather than read off
 * the global `EventSource` class: jsdom (this repo's test environment)
 * doesn't implement `EventSource` at all, and these three numbers are
 * fixed by the spec, so there's nothing to gain from indirecting through
 * a constructor that may not exist where this module is evaluated.
 */
export const EVENT_SOURCE_CONNECTING = 0
export const EVENT_SOURCE_OPEN = 1
export const EVENT_SOURCE_CLOSED = 2

let eventSourceFactory: EventSourceFactory = (url) => new EventSource(url)

/**
 * Test seam (prescribed by ticket P5-04): the store never calls
 * `new EventSource` directly, only `createEventSource` below, so a spec
 * can redirect every connection this module ever opens to a fake without
 * touching the network. A module-level setter — mirroring
 * `api/context.ts`'s singleton + `__resetApiClientSingleton` pattern —
 * reads better here than a per-`subscribe()` option: every production
 * call site (`useLiveStore().subscribe(...)`) then stays free of test
 * plumbing, and a spec sets the factory once in `beforeEach` rather than
 * threading it through every `subscribe()` call.
 */
export function setEventSourceFactory(factory: EventSourceFactory): void {
  eventSourceFactory = factory
}

/** Test-only: restores the real-network default so a leaked fake factory can't survive across files. */
export function resetEventSourceFactory(): void {
  eventSourceFactory = (url) => new EventSource(url)
}

export function createEventSource(url: string): EventSourceLike {
  return eventSourceFactory(url)
}

/** SPEC §5.3 / ARGUS_STREAM_REPLAY_MAX: the client-side mirror of the server's own replay cap. */
export const RING_CAPACITY = 2000

/** Ticket P5-04's AC: reconnect delays must never exceed this, however many attempts fail in a row. */
export const BACKOFF_CAP_MS = 30_000

/**
 * First attempt's uncapped delay (before doubling/jitter). Not SPEC-mandated — no reconnect base is
 * specified anywhere in SPEC.md's §5.2, only the 30s cap — chosen so a single dropped connection
 * doesn't retry near-instantly (which would just re-fail against a server still restarting) while
 * still feeling responsive for a genuinely transient blip.
 */
const BACKOFF_BASE_MS = 1_000

/**
 * Jittered exponential backoff, capped at {@link BACKOFF_CAP_MS}. `random` is injectable so a test
 * can assert the growth curve deterministically (stub it and fake timers — see live.spec.ts).
 *
 * Invariants a caller/test can rely on:
 *   - never exceeds `BACKOFF_CAP_MS`.
 *   - monotonically non-decreasing in `attempt`, for any *fixed* `random()` return value — the two
 *     things the AC asserts (an "increasing... capped at 30s" sequence).
 *
 * Full jitter (`random() * delay`) would violate the second invariant — it can return a smaller
 * value for a larger attempt, which would make "increasing delays" untestable without a huge sample.
 * Instead only the *upper half* is jittered: `delay = cap'/2 + random() * cap'/2`, where
 * `cap' = min(base * 2^attempt, cap)`. Since `cap'` alone is non-decreasing in `attempt` and `delay`
 * is an increasing function of `cap'` for any fixed `random()` in `[0, 1)`, the sequence is monotone
 * — while still spreading a thundering herd of tabs across `[cap'/2, cap')` instead of reconnecting
 * in lockstep.
 */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const uncapped = BACKOFF_BASE_MS * 2 ** attempt
  const cappedDelay = Math.min(uncapped, BACKOFF_CAP_MS)
  return cappedDelay / 2 + random() * (cappedDelay / 2)
}

/** SPEC §5's two channels: the fleet-wide firehose (optionally filtered) and one session's own stream. */
export type LiveTopic =
  | { kind: 'firehose'; kinds?: string[]; project?: string; vendor?: string }
  | { kind: 'session'; id: string }

/**
 * Builds the stream path for a topic, optionally resuming from a replay position.
 *
 * Same-origin, relative path — matching `api/client.ts`'s own default `baseUrl`, which is `''`
 * because Argus serves ops/read/ingest/stream from one origin (SPEC §4.4); `pnpm dev`'s Vite proxy
 * and the embedded-SPA deployment both cover a bare `/api/v1/...` path without needing an absolute
 * URL here.
 *
 * `kinds`/`project`/`vendor` are Argus's own vendor-vocabulary pass-through fields (SPEC §0): they
 * are appended verbatim, never validated against a closed set. `kinds` is repeated once per value
 * (SPEC §4.1's "repeated params OR within a field" convention, same as the REST list endpoints) —
 * `project`/`vendor` are single-valued here because that's the `LiveTopic` shape this ticket
 * prescribes, even though the equivalent REST query params (`Project`/`Vendor` in `schema.d.ts`) are
 * repeatable.
 *
 * `opts.after` is the SSE reconnect fallback (SPEC §5.2): omitted entirely (not even as `after=`)
 * when there's no prior position, since the wire distinguishes "no replay requested" from "replay
 * from an empty string" — the fallback for a client-initiated reconnect the browser's own
 * `Last-Event-ID` header can't cover (see stores/live.ts's `onerror` handling for why both paths
 * exist).
 */
export function streamUrl(topic: LiveTopic, opts: { after?: string | null } = {}): string {
  const params = new URLSearchParams()

  if (topic.kind === 'firehose') {
    for (const kind of topic.kinds ?? []) params.append('kinds', kind)
    if (topic.project) params.append('project', topic.project)
    if (topic.vendor) params.append('vendor', topic.vendor)
  }
  if (opts.after) params.append('after', opts.after)

  const base = topic.kind === 'firehose' ? '/api/v1/stream' : `/api/v1/sessions/${encodeURIComponent(topic.id)}/stream`
  const query = params.toString()
  return query ? `${base}?${query}` : base
}
