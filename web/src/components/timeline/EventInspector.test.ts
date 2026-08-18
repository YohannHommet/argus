import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getEvent200Default } from '@/test/fixtures'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import EventInspector from './EventInspector.vue'

/**
 * Round-3 critic gap: "prefer a persistent right-side pane on wide
 * viewports ... over a modal/overlay drawer". `useMediaQuery` (see that
 * composable's doc) degrades to "no match" wherever `window.matchMedia`
 * reports no match — the global `test-setup.ts` stub does this for every
 * query — so the narrow/overlay path is this suite's default, and the two
 * tests below stub `matchMedia` directly to exercise the wide path.
 */
function stubWideViewport() {
  window.matchMedia = (query: string) =>
    ({
      matches: query === '(min-width: 1024px)',
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}

describe('EventInspector', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    document.body.replaceChildren()
    vi.restoreAllMocks()
  })

  it('renders the overlay Sheet on a narrow viewport (default in this test environment)', async () => {
    const wrapper = mount(EventInspector, { props: { eventRef: null, open: true }, attachTo: document.body })
    await flushPromises()

    expect(document.body.querySelector('[data-testid="event-detail-sheet"]')).toBeTruthy()
    expect(wrapper.find('[data-testid="event-inspector-panel"]').exists()).toBe(false)
  })

  it('renders a persistent panel (not the overlay Sheet) on a wide viewport, populated with the selected event', async () => {
    stubWideViewport()
    const store = useSessionDetailStore()
    vi.spyOn(store, 'loadEvent').mockResolvedValue(getEvent200Default)

    const wrapper = mount(EventInspector, { props: { eventRef: getEvent200Default.event_ref, open: true }, attachTo: document.body })
    await flushPromises()

    expect(wrapper.find('[data-testid="event-inspector-panel"]').exists()).toBe(true)
    expect(document.body.querySelector('[data-testid="event-detail-sheet"]')).toBeFalsy()
    expect(wrapper.find('[data-testid="json-viewer"]').exists()).toBe(true)
  })

  it('the wide-viewport panel shows a placeholder, not a blank pane, when nothing is selected', async () => {
    stubWideViewport()
    const wrapper = mount(EventInspector, { props: { eventRef: null, open: false }, attachTo: document.body })
    await flushPromises()

    expect(wrapper.get('[data-testid="event-detail-empty"]')).toBeTruthy()
  })
})
