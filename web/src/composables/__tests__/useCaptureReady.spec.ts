import { mount } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import { CAPTURE_READY_ATTR, useCaptureReady } from '@/composables/useCaptureReady'

function harness(isReady: () => boolean) {
  return defineComponent({
    setup() {
      useCaptureReady(isReady)
      return () => h('div', 'view')
    },
  })
}

afterEach(() => {
  document.documentElement.removeAttribute(CAPTURE_READY_ATTR)
})

describe('useCaptureReady', () => {
  it('does not mark the document ready while the view is still loading', () => {
    mount(harness(() => false))
    expect(document.documentElement.hasAttribute(CAPTURE_READY_ATTR)).toBe(false)
  })

  it('marks the document ready synchronously when it mounts already loaded', () => {
    mount(harness(() => true))
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  it('flips the attribute when the view finishes loading', async () => {
    const ready = ref(false)
    const wrapper = mount(harness(() => ready.value))
    expect(document.documentElement.hasAttribute(CAPTURE_READY_ATTR)).toBe(false)

    ready.value = true
    await wrapper.vm.$nextTick()
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')
  })

  it('clears the attribute on unmount so the next route cannot inherit it', async () => {
    const wrapper = mount(harness(() => true))
    expect(document.documentElement.getAttribute(CAPTURE_READY_ATTR)).toBe('true')

    wrapper.unmount()
    expect(document.documentElement.hasAttribute(CAPTURE_READY_ATTR)).toBe(false)
  })
})
