import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useUiStore } from '@/stores/ui'

const STORAGE_KEY = 'argus-ui'

describe('useUiStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('defaults to dark with no localStorage entry and no prefers-color-scheme match', () => {
    const ui = useUiStore()

    expect(ui.theme).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.classList.contains('light')).toBe(false)
  })

  it('toggle() flips the <html> class and persists to localStorage', () => {
    const ui = useUiStore()

    expect(document.documentElement.classList.contains('dark')).toBe(true)

    ui.toggle()

    expect(ui.theme).toBe('light')
    expect(document.documentElement.classList.contains('light')).toBe(true)
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}')).toEqual({ theme: 'light' })

    ui.toggle()

    expect(ui.theme).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.classList.contains('light')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}')).toEqual({ theme: 'dark' })
  })

  it('honours a persisted localStorage entry over prefers-color-scheme', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ theme: 'light' }))

    const ui = useUiStore()

    expect(ui.theme).toBe('light')
    expect(document.documentElement.classList.contains('light')).toBe(true)
  })

  it('toggle() does not throw when localStorage.setItem fails (m22)', () => {
    const ui = useUiStore()
    const setItemSpy = vi
      .spyOn(Storage.prototype, 'setItem')
      .mockImplementation(() => {
        throw new DOMException('QuotaExceededError', 'QuotaExceededError')
      })
    const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(() => ui.toggle()).not.toThrow()

    // <html>'s class still flips even though persistence failed — the
    // failure must not roll back work already applied by the watcher.
    expect(ui.theme).toBe('light')
    expect(document.documentElement.classList.contains('light')).toBe(true)
    expect(consoleErrorSpy).toHaveBeenCalled()

    setItemSpy.mockRestore()
    consoleErrorSpy.mockRestore()
  })

  it('preserves an unrelated sibling field already in the argus-ui key on a theme change (m22)', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ theme: 'dark', sidebar: 'collapsed' }))
    const ui = useUiStore()

    ui.toggle()

    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}')).toEqual({
      theme: 'light',
      sidebar: 'collapsed',
    })
  })

  it('uses light when prefers-color-scheme: light matches and no localStorage entry exists', () => {
    window.matchMedia = (query: string) =>
      ({
        matches: query === '(prefers-color-scheme: light)',
        media: query,
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList

    const ui = useUiStore()

    expect(ui.theme).toBe('light')
  })
})
