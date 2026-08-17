import { onScopeDispose, toValue, watch, type MaybeRefOrGetter } from 'vue'

/**
 * The attribute scripts/ui-capture.sh's Playwright half waits on
 * (`[data-capture-ready="true"]`) before it takes a screenshot. It exists so
 * the harness never has to guess a per-view selector, and so it can never
 * hand the visual gauntlet a picture of a skeleton: the view itself declares
 * when its first fetch has resolved.
 */
export const CAPTURE_READY_ATTR = 'data-capture-ready'

/**
 * Every top-level view calls this once with a getter that is true when the
 * view has finished its initial load — meaning it is showing real data, an
 * empty state, or an error banner. All three are legitimate things to
 * photograph; a spinner is not.
 *
 * The attribute goes on `<html>` rather than the view's own root because
 * that survives whatever the view does with its subtree (portals, `v-if`
 * swaps between skeleton and content) and gives the harness one stable
 * selector for all six routes. It is removed when the view's scope is
 * disposed, so a client-side navigation cannot leave the previous route's
 * "ready" claim standing while the next one is still loading.
 */
export function useCaptureReady(isReady: MaybeRefOrGetter<boolean>): void {
  const root = document?.documentElement
  if (!root) return

  const apply = (ready: boolean) => {
    if (ready) {
      root.setAttribute(CAPTURE_READY_ATTR, 'true')
    } else {
      root.removeAttribute(CAPTURE_READY_ATTR)
    }
  }

  apply(toValue(isReady))
  watch(() => toValue(isReady), apply)
  onScopeDispose(() => root.removeAttribute(CAPTURE_READY_ATTR))
}
