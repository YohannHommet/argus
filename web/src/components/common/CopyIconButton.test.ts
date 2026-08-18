import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import CopyIconButton from './CopyIconButton.vue'

describe('CopyIconButton', () => {
  const originalClipboard = navigator.clipboard

  beforeEach(() => {
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
  })

  afterEach(() => {
    Object.assign(navigator, { clipboard: originalClipboard })
    vi.restoreAllMocks()
  })

  it('copies the exact text prop to the clipboard, not a re-formatted version', async () => {
    const wrapper = mount(CopyIconButton, { props: { text: 'agent-107d2cba-explore-1' } })
    await wrapper.get('[data-testid="copy-icon-button"]').trigger('click')
    await flushPromises()

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('agent-107d2cba-explore-1')
  })

  it('surfaces a clipboard failure instead of silently doing nothing', async () => {
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    const wrapper = mount(CopyIconButton, { props: { text: 'x' } })
    await wrapper.get('[data-testid="copy-icon-button"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="copy-icon-button"]').attributes('title')).toBe('Clipboard unavailable')
  })

  it('does not propagate the click to an ancestor row (copy inside a clickable row must not also open it)', async () => {
    const rowClick = vi.fn()
    const wrapper = mount(
      { components: { CopyIconButton }, template: `<div @click="onClick"><CopyIconButton text="x" /></div>`, methods: { onClick: rowClick } },
      {},
    )
    await wrapper.get('[data-testid="copy-icon-button"]').trigger('click')
    expect(rowClick).not.toHaveBeenCalled()
  })
})
