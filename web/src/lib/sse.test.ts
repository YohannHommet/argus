import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  BACKOFF_CAP_MS,
  backoffDelay,
  createEventSource,
  resetEventSourceFactory,
  setEventSourceFactory,
  streamUrl,
} from './sse'
import type { EventSourceLike } from './sse'

describe('backoffDelay', () => {
  it('never exceeds BACKOFF_CAP_MS, however large the attempt', () => {
    for (const attempt of [0, 1, 2, 5, 10, 50]) {
      expect(backoffDelay(attempt, () => 1)).toBeLessThanOrEqual(BACKOFF_CAP_MS)
      expect(backoffDelay(attempt, () => 0)).toBeLessThanOrEqual(BACKOFF_CAP_MS)
    }
  })

  it('is monotonically non-decreasing in attempt for a fixed random() draw', () => {
    const delays = [0, 1, 2, 3, 4, 5, 6].map((attempt) => backoffDelay(attempt, () => 1))
    for (let i = 1; i < delays.length; i++) {
      expect(delays[i]).toBeGreaterThanOrEqual(delays[i - 1]!)
    }
  })

  it('reaches exactly the cap once the uncapped delay exceeds it, with random() at its max', () => {
    expect(backoffDelay(0, () => 1)).toBe(1000)
    expect(backoffDelay(1, () => 1)).toBe(2000)
    expect(backoffDelay(4, () => 1)).toBe(16000)
    expect(backoffDelay(5, () => 1)).toBe(BACKOFF_CAP_MS)
    expect(backoffDelay(9, () => 1)).toBe(BACKOFF_CAP_MS)
  })

  it('jitters only the upper half: delay is always between cap/2 and cap for a saturated attempt', () => {
    for (let i = 0; i < 20; i++) {
      const delay = backoffDelay(9, Math.random)
      expect(delay).toBeGreaterThanOrEqual(BACKOFF_CAP_MS / 2)
      expect(delay).toBeLessThanOrEqual(BACKOFF_CAP_MS)
    }
  })

  it('defaults to Math.random when no random function is given', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    expect(backoffDelay(0)).toBe(750) // 500 + 0.5 * 500
    vi.restoreAllMocks()
  })
})

describe('streamUrl', () => {
  it('builds the firehose path with no query string when there are no filters and no after', () => {
    expect(streamUrl({ kind: 'firehose' })).toBe('/api/v1/stream')
  })

  it('repeats `kinds` once per value and appends project/vendor', () => {
    const url = streamUrl({ kind: 'firehose', kinds: ['tool.call', 'tool.result'], project: 'argus', vendor: 'claude_code' })
    const parsed = new URL(url, 'http://localhost')
    expect(parsed.pathname).toBe('/api/v1/stream')
    expect(parsed.searchParams.getAll('kinds')).toEqual(['tool.call', 'tool.result'])
    expect(parsed.searchParams.get('project')).toBe('argus')
    expect(parsed.searchParams.get('vendor')).toBe('claude_code')
  })

  it('builds the session path with the session id', () => {
    expect(streamUrl({ kind: 'session', id: 'sess-1' })).toBe('/api/v1/sessions/sess-1/stream')
  })

  it('appends `after` only when a position is given, on both topic kinds', () => {
    expect(streamUrl({ kind: 'firehose' }, { after: null })).toBe('/api/v1/stream')
    expect(streamUrl({ kind: 'firehose' }, { after: undefined })).toBe('/api/v1/stream')
    expect(streamUrl({ kind: 'firehose' }, { after: 'ref-1' })).toBe('/api/v1/stream?after=ref-1')
    expect(streamUrl({ kind: 'session', id: 'sess-1' }, { after: 'ref-1' })).toBe('/api/v1/sessions/sess-1/stream?after=ref-1')
  })
})

describe('event source factory injection', () => {
  afterEach(() => {
    resetEventSourceFactory()
  })

  it('setEventSourceFactory redirects createEventSource, never touching the network', () => {
    const fake: EventSourceLike = {
      readyState: 0,
      addEventListener: vi.fn(),
      close: vi.fn(),
      onopen: null,
      onerror: null,
    }
    const factory = vi.fn().mockReturnValue(fake)
    setEventSourceFactory(factory)

    const result = createEventSource('/api/v1/stream')

    expect(factory).toHaveBeenCalledWith('/api/v1/stream')
    expect(result).toBe(fake)
  })
})
