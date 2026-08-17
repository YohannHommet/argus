import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import CopyBlock from '@/components/common/CopyBlock.vue'
import JsonViewer from '@/components/common/JsonViewer.vue'

describe('JsonViewer', () => {
  it('renders raw attrs verbatim, keys and values unchanged', () => {
    const attrs = { 'tool.name': 'Edit', nested: { count: 3 }, flag: false, missing: null }
    const wrapper = mount(JsonViewer, { props: { value: attrs } })
    const text = wrapper.get('code').text()

    expect(text).toContain('"tool.name": "Edit"')
    expect(text).toContain('"count": 3')
    expect(text).toContain('"flag": false')
    // A null in the raw payload is data, not a missing value — it must show
    // as null here rather than being em-dashed away like a UI-level null.
    expect(text).toContain('"missing": null')
  })

  it('does not throw or blank out on a circular structure', () => {
    const circular: Record<string, unknown> = { name: 'loop' }
    circular.self = circular

    const wrapper = mount(JsonViewer, { props: { value: circular } })
    expect(wrapper.get('code').text()).toContain('unserializable')
  })

  it('says so explicitly when there is no value yet', () => {
    const wrapper = mount(JsonViewer, { props: { value: undefined } })
    expect(wrapper.get('code').text()).toBe('(no value)')
  })

  it('offers the serialized JSON to the clipboard, not the displayed markup', () => {
    const wrapper = mount(JsonViewer, { props: { value: { a: 1 } } })
    expect(wrapper.getComponent(CopyBlock).props('text')).toBe('{\n  "a": 1\n}')
  })
})

describe('CopyBlock', () => {
  it('writes the exact text to the clipboard and confirms', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const wrapper = mount(CopyBlock, { props: { text: 'export OTEL_LOG_TOOL_DETAILS=1' } })
    await wrapper.get('button').trigger('click')
    await Promise.resolve()

    expect(writeText).toHaveBeenCalledWith('export OTEL_LOG_TOOL_DETAILS=1')
    expect(wrapper.text()).toContain('Copied')
    vi.unstubAllGlobals()
  })

  it('surfaces a clipboard failure instead of silently doing nothing', async () => {
    vi.stubGlobal('navigator', {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    })

    const wrapper = mount(CopyBlock, { props: { text: 'x' } })
    await wrapper.get('button').trigger('click')
    await Promise.resolve()
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Clipboard unavailable')
    vi.unstubAllGlobals()
  })
})
