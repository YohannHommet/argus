import { effectScope, getCurrentScope, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/errors'
import { useApi } from './useApi'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeApiError(status = 400): ApiError {
  return new ApiError({ type: 'urn:argus:error:bad-request', title: 'Bad Request', status }, new Response(null, { status }))
}

function makeAbortError(): DOMException {
  return new DOMException('The operation was aborted.', 'AbortError')
}

describe('useApi', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('starts idle and exposes data/error/loading/execute/abort', () => {
    const scope = effectScope()
    scope.run(() => {
      const api = useApi<number>(async () => 1)
      expect(api.data.value).toBeNull()
      expect(api.error.value).toBeNull()
      expect(api.loading.value).toBe(false)
    })
    scope.stop()
  })

  it('runs immediately when options.immediate is true', async () => {
    const scope = effectScope()
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(async () => 42, { immediate: true })
    })
    await vi.waitFor(() => expect(api.loading.value).toBe(false))
    expect(api.data.value).toBe(42)
    scope.stop()
  })

  it('sets loading true during execute and false after a successful resolve', async () => {
    const scope = effectScope()
    const d = deferred<number>()
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(() => d.promise)
    })
    const run = api.execute()
    expect(api.loading.value).toBe(true)
    d.resolve(7)
    await run
    expect(api.loading.value).toBe(false)
    expect(api.data.value).toBe(7)
    expect(api.error.value).toBeNull()
    scope.stop()
  })

  it('surfaces an ApiError in error and clears data, and never retries it', async () => {
    const scope = effectScope()
    const fetcher = vi.fn(async () => {
      throw makeApiError(422)
    })
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(fetcher)
    })
    await api.execute()
    expect(api.error.value).toBeInstanceOf(ApiError)
    expect((api.error.value as ApiError).status).toBe(422)
    expect(api.data.value).toBeNull()
    expect(api.loading.value).toBe(false)
    expect(fetcher).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('error clears on a subsequent successful run', async () => {
    const scope = effectScope()
    let shouldFail = true
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(async () => {
        if (shouldFail) throw makeApiError(500)
        return 9
      })
    })
    await api.execute()
    expect(api.error.value).toBeInstanceOf(ApiError)

    shouldFail = false
    await api.execute()
    expect(api.error.value).toBeNull()
    expect(api.data.value).toBe(9)
    scope.stop()
  })

  it('retries exactly once on a network error (non-ApiError, non-abort), surfacing a second failure', async () => {
    const scope = effectScope()
    const fetcher = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    })
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(fetcher)
    })
    await api.execute()
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(api.error.value).toBeInstanceOf(TypeError)
    expect(api.data.value).toBeNull()
    scope.stop()
  })

  it('retry succeeds on the second attempt', async () => {
    const scope = effectScope()
    let calls = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(async () => {
        calls++
        if (calls === 1) throw new TypeError('Failed to fetch')
        return 5
      })
    })
    await api.execute()
    expect(calls).toBe(2)
    expect(api.data.value).toBe(5)
    expect(api.error.value).toBeNull()
    scope.stop()
  })

  it('an aborted run does not write to error, and loading stays owned by the superseding run', async () => {
    const scope = effectScope()
    const first = deferred<number>()
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(
        (signal) =>
          new Promise<number>((resolve, reject) => {
            signal.addEventListener('abort', () => reject(makeAbortError()))
            first.promise.then(resolve)
          }),
      )
    })

    const firstRun = api.execute()
    api.abort()
    await firstRun
    expect(api.error.value).toBeNull()
    expect(api.loading.value).toBe(false)
    scope.stop()
  })

  it('a second call supersedes the first: the first resolving late does not overwrite data', async () => {
    const scope = effectScope()
    const first = deferred<number>()
    const second = deferred<number>()
    let call = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      // Deliberately ignores `signal` — the fetcher itself doesn't reject
      // on abort (a real fetch/XHR wouldn't either, until the underlying
      // transport notices), so the only thing that can prevent the stale
      // write is the run-id guard, not abort-driven rejection.
      api = useApi<number>(() => {
        call++
        const mine = call
        return mine === 1 ? first.promise : second.promise
      })
    })

    const run1 = api.execute()
    const run2 = api.execute() // aborts run1's controller and starts a fresh one

    // Resolve the superseded first call's underlying promise late — it
    // resolves (doesn't reject), so only the run-id guard can stop this
    // from overwriting `data` after run2 already won.
    first.resolve(111)
    second.resolve(222)
    await Promise.all([run1, run2])

    expect(api.data.value).toBe(222)
    scope.stop()
  })

  it('aborts the in-flight request on scope dispose (unmount)', async () => {
    const abortedSignals: AbortSignal[] = []
    const Comp = {
      template: '<div />',
      setup() {
        useApi<number>(
          (signal) =>
            new Promise<number>((resolve) => {
              signal.addEventListener('abort', () => abortedSignals.push(signal))
              // never resolves on its own
              void resolve
            }),
          { immediate: true },
        )
        return {}
      },
    }
    const wrapper = mount(Comp)
    await nextTick()
    wrapper.unmount()
    expect(abortedSignals).toHaveLength(1)
    expect(abortedSignals[0].aborted).toBe(true)
  })

  it('an abort whose run was already superseded does not touch loading (the newer run owns it)', async () => {
    const scope = effectScope()
    const first = deferred<number>()
    const second = deferred<number>()
    let call = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>((signal) => {
        call++
        const mine = call
        return new Promise<number>((resolve, reject) => {
          signal.addEventListener('abort', () => reject(makeAbortError()))
          ;(mine === 1 ? first : second).promise.then(resolve)
        })
      })
    })

    const run1 = api.execute() // starts run 1
    const run2 = api.execute() // aborts run 1's controller, starts run 2
    await Promise.resolve() // let run 1's abort listener reject it

    // Run 1 was superseded before it aborted: its own `runId !== currentRunId`,
    // so the abort branch must leave `loading` alone — it is run 2's to own.
    expect(api.loading.value).toBe(true)

    second.resolve(9)
    await Promise.all([run1, run2])

    expect(api.data.value).toBe(9)
    expect(api.error.value).toBeNull()
    expect(api.loading.value).toBe(false)
    scope.stop()
  })

  it('an ApiError from a superseded run never lands in error; the newer run wins', async () => {
    const scope = effectScope()
    const firstError = deferred<never>()
    const second = deferred<number>()
    let call = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(() => {
        call++
        return call === 1 ? (firstError.promise as Promise<number>) : second.promise
      })
    })

    const run1 = api.execute() // run A
    const run2 = api.execute() // supersedes A, run B

    // A rejects with a real ApiError only *after* B has already started.
    firstError.reject(makeApiError(500))
    await run1

    // Assert *before* B settles: if A's superseded-run guard didn't fire,
    // A's own write would be visible right here — B overwriting it later
    // would hide the bug.
    expect(api.error.value).toBeNull()
    expect(api.data.value).toBeNull()
    expect(api.loading.value).toBe(true) // still B's in-flight run

    second.resolve(222)
    await run2

    expect(api.error.value).toBeNull()
    expect(api.data.value).toBe(222)
    scope.stop()
  })

  it('retry path: a superseded run whose retry then settles does not overwrite state', async () => {
    const scope = effectScope()
    const retry = deferred<number>()
    const second = deferred<number>()
    let call = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(() => {
        call++
        if (call === 1) return Promise.reject(new TypeError('Failed to fetch'))
        if (call === 2) return retry.promise // run A's retry attempt
        return second.promise // run B
      })
    })

    const run1 = api.execute() // run A: fails, schedules a retry
    // Wait for A's own retry attempt (call 2) to actually start — i.e. for
    // its `await runOnce(signal)` to be suspended on `retry.promise` —
    // before superseding it, so run B's own fetch (call 3) can't race in
    // ahead of it and swap which deferred each run ends up awaiting.
    while (call < 2) {
      await Promise.resolve()
    }
    const run2 = api.execute() // run B supersedes A while A's retry is in flight

    retry.resolve(111) // A's retry settles late — must not win
    await run1

    // Assert before B settles: if A's superseded-retry guard didn't fire,
    // A's write would be visible right here.
    expect(api.data.value).toBeNull()
    expect(api.error.value).toBeNull()

    second.resolve(222)
    await run2

    expect(api.data.value).toBe(222)
    expect(api.error.value).toBeNull()
    scope.stop()
  })

  it('retry path: a superseded run whose retry then fails (non-abort) does not surface the stale error', async () => {
    const scope = effectScope()
    const retry = deferred<number>()
    const second = deferred<number>()
    let call = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(() => {
        call++
        if (call === 1) return Promise.reject(new TypeError('Failed to fetch'))
        if (call === 2) return retry.promise // run A's retry attempt
        return second.promise // run B
      })
    })

    const run1 = api.execute() // run A: fails, schedules a retry
    while (call < 2) {
      await Promise.resolve()
    }
    const run2 = api.execute() // run B supersedes A while A's retry is in flight

    // A's retry itself fails for real (not an abort) — but A was already
    // superseded, so this must never land in `error`.
    retry.reject(new TypeError('Failed to fetch (retry)'))
    await run1

    expect(api.error.value).toBeNull()
    expect(api.data.value).toBeNull()

    second.resolve(222)
    await run2

    expect(api.data.value).toBe(222)
    expect(api.error.value).toBeNull()
    scope.stop()
  })

  it('retry path: a run whose retry is aborted leaves error untouched', async () => {
    const scope = effectScope()
    const retry = deferred<number>()
    let call = 0
    let capturedSignal: AbortSignal | undefined
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>((signal) => {
        call++
        capturedSignal = signal
        if (call === 1) return Promise.reject(new TypeError('Failed to fetch'))
        return retry.promise
      })
    })

    const run = api.execute()
    // Wait for the retry attempt (call 2) to actually start before aborting
    // it, so the abort lands while it's genuinely in flight.
    while (call < 2) {
      await Promise.resolve()
    }
    // The retry is in flight on the same controller generation — aborting
    // it (unmount, or a bare abort() call) must not surface an error.
    api.abort()
    retry.reject(makeAbortError())
    void capturedSignal
    await run

    expect(api.error.value).toBeNull()
    scope.stop()
  })

  it('retry rejecting with a non-Error reason is wrapped in an Error', async () => {
    const scope = effectScope()
    let call = 0
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(async () => {
        call++
        if (call === 1) throw new TypeError('Failed to fetch')
        // Non-Error rejection reason, deliberately: exercises the
        // `instanceof Error ? … : new Error(String(...))` ternary.
        return Promise.reject('retry-boom')
      })
    })

    await api.execute()

    expect(api.error.value).toBeInstanceOf(Error)
    expect(api.error.value?.message).toBe('retry-boom')
    expect(api.data.value).toBeNull()
    scope.stop()
  })

  it('called outside any effect scope, still returns a working object and does not throw', async () => {
    expect(getCurrentScope()).toBeUndefined()

    let api!: ReturnType<typeof useApi<number>>
    expect(() => {
      api = useApi<number>(async () => 5)
    }).not.toThrow()

    await api.execute()
    expect(api.data.value).toBe(5)
    expect(api.error.value).toBeNull()
    expect(api.loading.value).toBe(false)
  })

  it('execute() aborts a previous in-flight controller before starting a new one', async () => {
    const scope = effectScope()
    const signals: AbortSignal[] = []
    let api!: ReturnType<typeof useApi<number>>
    scope.run(() => {
      api = useApi<number>(
        (signal) =>
          new Promise<number>(() => {
            signals.push(signal)
          }),
      )
    })
    void api.execute()
    void api.execute()
    await nextTick()
    expect(signals).toHaveLength(2)
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)
    scope.stop()
  })
})
