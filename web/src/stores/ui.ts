import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type Theme = 'dark' | 'light'

/**
 * localStorage key. Its JSON shape ({ theme }) must stay in sync with the
 * anti-flash inline script in index.html, which reads it before Vue boots.
 */
const STORAGE_KEY = 'argus-ui'

function readStoredTheme(): Theme | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as { theme?: unknown }
    return parsed.theme === 'dark' || parsed.theme === 'light' ? parsed.theme : null
  } catch {
    return null
  }
}

/**
 * Merges `{ theme }` over whatever the `argus-ui` key already holds — a
 * sibling field (e.g. a future sidebar/density/filter preset, SPEC.md
 * §Phase 4) must survive a theme change, not be clobbered by it — and
 * swallows a write failure (quota exceeded, private-mode storage
 * disabled) so it can't escape `toggle()`'s `flush: 'sync'` watcher after
 * the `<html>` class has already been applied. Logged, not silently
 * dropped, so an operator can tell theme persistence is broken.
 */
function persistTheme(theme: Theme) {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    const existing = raw ? (JSON.parse(raw) as Record<string, unknown>) : {}
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...existing, theme }))
  } catch (error) {
    console.error('Failed to persist theme to localStorage', error)
  }
}

/**
 * Theme precedence (must match index.html's inline script exactly):
 *   1. a localStorage entry wins
 *   2. else if `prefers-color-scheme: light` matches, use light
 *   3. else dark (covers both "no match" and an unsupported media query)
 */
function detectInitialTheme(): Theme {
  const stored = readStoredTheme()
  if (stored) return stored

  const prefersLight = window.matchMedia?.('(prefers-color-scheme: light)').matches ?? false
  return prefersLight ? 'light' : 'dark'
}

function applyThemeClass(theme: Theme) {
  const root = document.documentElement
  root.classList.remove('dark', 'light')
  root.classList.add(theme)
}

export const useUiStore = defineStore('ui', () => {
  const theme = ref<Theme>(detectInitialTheme())

  applyThemeClass(theme.value)

  function setTheme(next: Theme) {
    theme.value = next
  }

  function toggle() {
    theme.value = theme.value === 'dark' ? 'light' : 'dark'
  }

  // Synchronous flush: toggling the theme must update <html>'s class and
  // localStorage immediately, not on the next microtask — callers (and
  // tests) read document.documentElement right after calling toggle()/
  // setTheme().
  watch(
    theme,
    (next) => {
      applyThemeClass(next)
      persistTheme(next)
    },
    { flush: 'sync' },
  )

  return { theme, setTheme, toggle }
})
