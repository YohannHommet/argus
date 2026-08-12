import { afterEach, beforeEach } from 'vitest'

// jsdom has no real media query engine. Default to "no match" so the
// dark-by-default precedence holds unless a test explicitly overrides
// window.matchMedia to simulate `prefers-color-scheme: light`.
beforeEach(() => {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
})

afterEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark', 'light')
})
