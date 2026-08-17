import { getCurrentScope, onScopeDispose, ref, shallowRef } from 'vue'
import type { Ref, ShallowRef } from 'vue'

import { ApiError } from '@/api/errors'

export interface UseApiOptions {
  /** Runs `execute()` once immediately, on the calling scope's setup. Default false. */
  immediate?: boolean
}

export interface UseApiReturn<T> {
  data: ShallowRef<T | null>
  error: Ref<ApiError | Error | null>
  loading: Ref<boolean>
  execute: () => Promise<void>
  abort: () => void
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException ? err.name === 'AbortError' : (err as Error)?.name === 'AbortError'
}

/**
 * Wraps a single fetcher with the honesty/lifecycle rules every view needs
 * so no screen ever renders a race's loser: abort on unmount, abort a
 * still-running previous call on re-request, ignore a superseded run's
 * settlement entirely (no stale `data`/`error`/`loading` write), retry
 * exactly once on a transient network failure, and never retry a real
 * `ApiError` (the server answered — a 400 won't fix itself).
 *
 * `data` is cleared to `null` on a definitive error rather than kept at
 * its last good value: a view showing stale numbers next to an error
 * banner is worse than a view showing nothing, and callers that want
 * "keep the last chart while refreshing" can hold their own copy from a
 * previous successful `data`.
 */
export function useApi<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  options: UseApiOptions = {},
): UseApiReturn<T> {
  const data = shallowRef<T | null>(null) as ShallowRef<T | null>
  const error = ref<ApiError | Error | null>(null) as Ref<ApiError | Error | null>
  const loading = ref(false)

  let controller: AbortController | null = null
  // Monotonic run id: the settlement handler for a run only writes state
  // when its id is still the latest — a superseded run's resolve/reject
  // is otherwise indistinguishable from the current one settling late.
  let currentRunId = 0

  function abort(): void {
    controller?.abort()
  }

  async function runOnce(signal: AbortSignal): Promise<T> {
    return fetcher(signal)
  }

  async function execute(): Promise<void> {
    abort()
    controller = new AbortController()
    const { signal } = controller
    const runId = ++currentRunId

    loading.value = true
    error.value = null

    try {
      const result = await runOnce(signal)
      if (runId !== currentRunId) return
      data.value = result
      error.value = null
      loading.value = false
    } catch (err) {
      if (isAbortError(err) || signal.aborted) {
        // Aborted runs are not errors — never written to `error`. If a
        // newer execute() already superseded this run, `runId` no longer
        // matches and `loading` is that newer run's to own; leave it
        // alone. If nothing superseded it (a bare `abort()` call, or
        // unmount), this run's own `loading: true` would otherwise be
        // stuck forever with nothing left in flight to clear it.
        if (runId === currentRunId) {
          loading.value = false
        }
        return
      }

      if (err instanceof ApiError) {
        // The server answered — a 4xx/5xx is not transient, so it is
        // never retried.
        if (runId !== currentRunId) return
        data.value = null
        error.value = err
        loading.value = false
        return
      }

      // Anything else is `fetch` itself failing (DNS, offline, CORS,
      // "Failed to fetch") — retried exactly once, on a fresh signal from
      // the same controller generation so an abort() during the retry
      // still cancels it.
      try {
        const retryResult = await runOnce(signal)
        if (runId !== currentRunId) return
        data.value = retryResult
        error.value = null
        loading.value = false
      } catch (retryErr) {
        if (isAbortError(retryErr) || signal.aborted) return
        if (runId !== currentRunId) return
        data.value = null
        error.value = retryErr instanceof Error ? retryErr : new Error(String(retryErr))
        loading.value = false
      }
    }
  }

  if (options.immediate) {
    void execute()
  }

  // `getCurrentScope()` covers both a component's setup() and a pinia
  // setup-store's setup() — `onUnmounted` would silently no-op (and warn)
  // in the latter since there's no component instance to unmount.
  if (getCurrentScope()) {
    onScopeDispose(() => {
      abort()
    })
  }

  return { data, error, loading, execute, abort }
}
