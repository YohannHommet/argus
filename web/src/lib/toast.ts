import { toast } from 'vue-sonner'

import { ApiError } from '@/api/errors'

/**
 * The global toast surface for API failures that have no natural inline
 * home — a background refresh (`meta.ts`'s `startAutoRefresh`), a
 * "load more" append that fails while the page already shows good rows,
 * anything a user did not just explicitly ask to see the result of.
 *
 * This is deliberately NOT wired into every store/view's primary fetch
 * path: the AC for every top-level view is still an in-place
 * {@link "@/components/common/ErrorState.vue" | ErrorState}, not a toast —
 * a toast disappears, an inline error state does not, and a user who
 * lands on `/sessions` with a broken API needs the failure to still be on
 * screen after they look away and back. Call this only for a failure that
 * would otherwise be silent.
 */
export function notifyApiFailure(error: ApiError | Error, opts: { title?: string } = {}): void {
  const detail = error instanceof ApiError ? (error.detail ?? error.title) : error.message
  toast.error(opts.title ?? 'Request failed', {
    description: detail,
  })
}
