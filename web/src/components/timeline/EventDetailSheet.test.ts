import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/errors'
import { getEvent200Default } from '@/test/fixtures'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import EventDetailSheet from './EventDetailSheet.vue'

// Sheet content is teleported (reka-ui's DialogPortal renders into
// document.body), so assertions read `document.body`, and every mount is
// attached there and torn down afterwards.
function mountSheet(props: { eventRef: string | null; open: boolean }) {
  return mount(EventDetailSheet, { props, attachTo: document.body })
}

describe('EventDetailSheet', () => {
  let loadEventSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    document.body.replaceChildren()
    vi.restoreAllMocks()
  })

  it('fetches the event by event_ref (never by id) and renders its raw attrs in a JsonViewer', async () => {
    const store = useSessionDetailStore()
    loadEventSpy = vi.spyOn(store, 'loadEvent').mockResolvedValue(getEvent200Default)

    mountSheet({ eventRef: getEvent200Default.event_ref, open: true })
    await flushPromises()

    expect(loadEventSpy).toHaveBeenCalledWith(getEvent200Default.event_ref)
    expect(document.body.querySelector('[data-testid="json-viewer"]')?.textContent).toContain('tool_decision.decision')
  })

  it('shows a loading state before the fetch resolves', async () => {
    const store = useSessionDetailStore()
    let resolve!: (value: typeof getEvent200Default) => void
    vi.spyOn(store, 'loadEvent').mockReturnValue(new Promise((r) => (resolve = r)))

    mountSheet({ eventRef: getEvent200Default.event_ref, open: true })
    await flushPromises()
    expect(document.body.querySelector('[data-testid="event-detail-sheet"]')?.textContent).not.toContain('tool_decision')

    resolve(getEvent200Default)
    await flushPromises()
    expect(document.body.querySelector('[data-testid="json-viewer"]')).toBeTruthy()
  })

  it('shows an ErrorState (never a blank sheet) on an ApiError, and retry refetches', async () => {
    const store = useSessionDetailStore()
    const problem = { type: 'urn:argus:error:boom', title: 'Boom', status: 500 }
    const err = new ApiError(problem, new Response(null, { status: 500 }))
    const spy = vi.spyOn(store, 'loadEvent').mockRejectedValueOnce(err).mockResolvedValueOnce(getEvent200Default)

    mountSheet({ eventRef: getEvent200Default.event_ref, open: true })
    await flushPromises()
    expect(document.body.querySelector('[data-testid="error-state"]')).toBeTruthy()

    const retryButton = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.includes('Retry'))
    retryButton?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    expect(spy).toHaveBeenCalledTimes(2)
    expect(document.body.querySelector('[data-testid="json-viewer"]')).toBeTruthy()
  })

  it('shows an EmptyState note (not a crash) when attrs is {}', async () => {
    const store = useSessionDetailStore()
    vi.spyOn(store, 'loadEvent').mockResolvedValue({ ...getEvent200Default, attrs: {} })

    mountSheet({ eventRef: getEvent200Default.event_ref, open: true })
    await flushPromises()

    expect(document.body.querySelector('[data-testid="empty-state"]')).toBeTruthy()
    // Still shows the (empty) JsonViewer — reversible/inspectable, not hidden.
    expect(document.body.querySelector('[data-testid="json-viewer"]')).toBeTruthy()
  })

  it('refetches when eventRef changes to a different ref', async () => {
    const store = useSessionDetailStore()
    const spy = vi.spyOn(store, 'loadEvent').mockResolvedValue(getEvent200Default)

    const wrapper = mountSheet({ eventRef: 'ref-a', open: true })
    await flushPromises()
    await wrapper.setProps({ eventRef: 'ref-b', open: true })
    await flushPromises()

    expect(spy).toHaveBeenNthCalledWith(1, 'ref-a')
    expect(spy).toHaveBeenNthCalledWith(2, 'ref-b')
  })
})
