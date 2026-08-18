/**
 * Reactive `window.matchMedia` wrapper — the one thing `EventInspector.vue`
 * needs to decide between a persistent right-side panel (wide viewports,
 * SPEC/critic reference: Langfuse's span-tree + inspector visible together)
 * and the existing overlay `Sheet` (narrow viewports, where a permanent
 * panel would starve the timeline of width).
 *
 * jsdom has no real media-query engine (see `test-setup.ts`'s global
 * `window.matchMedia` stub, which always reports `matches: false`), so this
 * composable degrades to "no match" whenever `matchMedia` is unsupported —
 * the same "unsupported means false" convention `stores/ui.ts` already uses
 * for `prefers-color-scheme`. That makes the narrow/overlay path the
 * deterministic default in unit tests unless a test explicitly stubs
 * `window.matchMedia` to report a match (see `stores/__tests__/ui.spec.ts`
 * for the existing pattern this follows).
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'

export function useMediaQuery(query: string) {
  const matches = ref(false)
  let mql: MediaQueryList | null = null

  function update() {
    matches.value = mql?.matches ?? false
  }

  onMounted(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    mql = window.matchMedia(query)
    update()
    mql.addEventListener('change', update)
  })

  onBeforeUnmount(() => {
    mql?.removeEventListener('change', update)
    mql = null
  })

  return matches
}
