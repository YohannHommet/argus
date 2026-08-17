import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import RawValue from './RawValue.vue'

describe('RawValue', () => {
  it('renders an unrecognized vendor value unchanged and does not throw', () => {
    expect(() => mount(RawValue, { props: { value: 'a_future_query_source' } })).not.toThrow()
    const wrapper = mount(RawValue, { props: { value: 'a_future_query_source' } })
    expect(wrapper.text()).toBe('a_future_query_source')
  })

  it('renders a known vendor value the same way — no special-casing', () => {
    const wrapper = mount(RawValue, { props: { value: 'sdk' } })
    expect(wrapper.text()).toBe('sdk')
  })

  it('renders the empty string as "unattributed"', () => {
    const wrapper = mount(RawValue, { props: { value: '' } })
    expect(wrapper.text()).toBe('unattributed')
  })

  it('delegates null to NullValue (renders the em dash)', () => {
    const wrapper = mount(RawValue, { props: { value: null } })
    expect(wrapper.text()).toContain('—')
  })

  it('delegates undefined to NullValue as well', () => {
    const wrapper = mount(RawValue, { props: { value: undefined } })
    expect(wrapper.text()).toContain('—')
  })
})
