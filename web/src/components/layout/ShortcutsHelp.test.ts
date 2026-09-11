import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import ShortcutsHelp from './ShortcutsHelp.vue'

// Dialog content is teleported (reka-ui's DialogPortal renders into document.body, same as
// EventDetailSheet.test.ts) — every mount is attached there and torn down afterwards.
async function mountHelp(open: boolean) {
  const wrapper = mount(ShortcutsHelp, { props: { open }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

describe('ShortcutsHelp', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('renders nothing when closed', async () => {
    await mountHelp(false)
    expect(document.body.querySelector('[data-testid="shortcuts-help"]')).toBeFalsy()
  })

  it('lists every shortcut useShortcuts.ts wires in when open', async () => {
    await mountHelp(true)
    const text = document.body.querySelector('[data-testid="shortcuts-help"]')?.textContent ?? ''

    expect(text).toContain('Focus the search field')
    expect(text).toContain('Move selection down the list')
    expect(text).toContain('Move selection up the list')
    expect(text).toContain('Close the open sheet, dialog, or this overlay')
    expect(text).toContain('Toggle this shortcuts overlay')
  })

  it('emits update:open(false) when dismissed via its close button', async () => {
    const wrapper = await mountHelp(true)
    const closeButton = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.includes('Close'))
    closeButton?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('update:open')?.at(-1)).toEqual([false])
  })
})
