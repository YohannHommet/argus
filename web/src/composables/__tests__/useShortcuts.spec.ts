import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useShortcuts } from '@/composables/useShortcuts'
import type { ShortcutHandlers } from '@/composables/useShortcuts'

function harness(handlers: ShortcutHandlers) {
  return defineComponent({
    setup() {
      useShortcuts(handlers)
      return () => h('div', [h('input', { type: 'text', 'data-testid': 'a-field' })])
    },
  })
}

function press(key: string, target: EventTarget = window, init: KeyboardEventInit = {}) {
  target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }))
}

describe('useShortcuts', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('calls onFocusSearch on "/" and prevents the default (so "/" is never typed anywhere)', () => {
    const onFocusSearch = vi.fn()
    mount(harness({ onFocusSearch }))

    press('/')

    expect(onFocusSearch).toHaveBeenCalledTimes(1)
  })

  it('calls onMoveNext on "j" and onMovePrev on "k"', () => {
    const onMoveNext = vi.fn()
    const onMovePrev = vi.fn()
    mount(harness({ onMoveNext, onMovePrev }))

    press('j')
    press('k')

    expect(onMoveNext).toHaveBeenCalledTimes(1)
    expect(onMovePrev).toHaveBeenCalledTimes(1)
  })

  it('calls onToggleHelp on "?"', () => {
    const onToggleHelp = vi.fn()
    mount(harness({ onToggleHelp }))

    press('?')

    expect(onToggleHelp).toHaveBeenCalledTimes(1)
  })

  it('calls onEscape on "Escape" even while a field is focused', () => {
    const onEscape = vi.fn()
    const wrapper = mount(harness({ onEscape }), { attachTo: document.body })
    const input = wrapper.get('[data-testid="a-field"]').element as HTMLInputElement
    input.focus()

    press('Escape', input)

    expect(onEscape).toHaveBeenCalledTimes(1)
  })

  it('ignores "/", "j", "k" and "?" while typing in an input, textarea or contenteditable', () => {
    const handlers = {
      onFocusSearch: vi.fn(),
      onMoveNext: vi.fn(),
      onMovePrev: vi.fn(),
      onToggleHelp: vi.fn(),
    }
    const wrapper = mount(harness(handlers), { attachTo: document.body })
    const input = wrapper.get('[data-testid="a-field"]').element as HTMLInputElement
    input.focus()

    press('/', input)
    press('j', input)
    press('k', input)
    press('?', input)

    expect(handlers.onFocusSearch).not.toHaveBeenCalled()
    expect(handlers.onMoveNext).not.toHaveBeenCalled()
    expect(handlers.onMovePrev).not.toHaveBeenCalled()
    expect(handlers.onToggleHelp).not.toHaveBeenCalled()
  })

  it('ignores a modified key (Ctrl/Cmd/Alt) so it never shadows a native or browser shortcut', () => {
    const onFocusSearch = vi.fn()
    mount(harness({ onFocusSearch }))

    press('/', window, { ctrlKey: true })
    press('/', window, { metaKey: true })
    press('/', window, { altKey: true })

    expect(onFocusSearch).not.toHaveBeenCalled()
  })

  it('removes its listener on unmount', () => {
    const onToggleHelp = vi.fn()
    const wrapper = mount(harness({ onToggleHelp }))
    wrapper.unmount()

    press('?')

    expect(onToggleHelp).not.toHaveBeenCalled()
  })
})
