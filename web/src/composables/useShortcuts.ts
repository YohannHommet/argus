import { onBeforeUnmount, onMounted } from 'vue'

export interface ShortcutHandlers {
  /** `/` — focus the current view's search input. Skipped while already typing in a field. */
  onFocusSearch?: () => void
  /** `j` — move selection to the next item (e.g. down a list). Skipped while typing. */
  onMoveNext?: () => void
  /** `k` — move selection to the previous item. Skipped while typing. */
  onMovePrev?: () => void
  /** `Esc` — close whatever sheet/dialog/overlay is open. Fires even while typing (dismissing is always safe, and a text field is a plausible place to press Esc from). */
  onEscape?: () => void
  /** `?` — toggle the shortcuts help overlay. Skipped while typing, so a literal "?" typed into a search box never triggers it. */
  onToggleHelp?: () => void
}

/** A form control or contenteditable region — a single-letter shortcut would otherwise steal the keystroke from whatever the user is typing into it. */
function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  if (target.isContentEditable) return true
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

/**
 * App-wide single-key shortcuts: `/` focus search, `j`/`k` list navigation, `Esc`
 * close, `?` help. A plain `window` keydown listener scoped to the calling component's mounted
 * lifetime — a view wires only the handlers it has something to do for (`SessionListView.vue`
 * registers `onFocusSearch`/`onMoveNext`/`onMovePrev`, `AppShell.vue` registers `onToggleHelp`), and
 * unmount removes the listener, so navigating away from a view never leaves a stale handler
 * double-firing on top of whatever the next view sets up.
 *
 * Modifier combinations (Ctrl/Cmd/Alt) and anything the target already called `preventDefault()` on
 * are always left alone — this hook only ever claims a bare, unmodified key, and never shadows a
 * native or another component's own shortcut.
 */
export function useShortcuts(handlers: ShortcutHandlers): void {
  function onKeydown(event: KeyboardEvent): void {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return

    // Esc is exempt from the typing guard below: dismissing an open sheet/dialog/overlay is always
    // safe, including from inside whatever field happens to have focus.
    if (event.key === 'Escape') {
      handlers.onEscape?.()
      return
    }

    if (isTypingTarget(event.target)) return

    switch (event.key) {
      case '/':
        if (handlers.onFocusSearch) {
          event.preventDefault()
          handlers.onFocusSearch()
        }
        break
      case 'j':
        handlers.onMoveNext?.()
        break
      case 'k':
        handlers.onMovePrev?.()
        break
      case '?':
        handlers.onToggleHelp?.()
        break
    }
  }

  onMounted(() => window.addEventListener('keydown', onKeydown))
  onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
}
