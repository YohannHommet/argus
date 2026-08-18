import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import Sparkline from './Sparkline.vue'

describe('Sparkline', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('renders nothing for null values', () => {
    const wrapper = mount(Sparkline, { props: { values: null } })
    expect(wrapper.find('[data-testid="sparkline"]').exists()).toBe(false)
  })

  it('renders nothing for a single-point series (no line to draw)', () => {
    const wrapper = mount(Sparkline, { props: { values: [5] } })
    expect(wrapper.find('[data-testid="sparkline"]').exists()).toBe(false)
  })

  it('renders a path and an emphasized endpoint circle for two or more points', () => {
    const wrapper = mount(Sparkline, { props: { values: [1, 5, 3, 8] } })
    expect(wrapper.find('[data-testid="sparkline"]').exists()).toBe(true)
    const paths = wrapper.findAll('path')
    expect(paths).toHaveLength(2) // area fill + line
    expect(wrapper.find('circle').exists()).toBe(true)
  })

  it('draws a flat mid-height line for an all-equal series rather than collapsing to the bottom', () => {
    const wrapper = mount(Sparkline, { props: { values: [4, 4, 4] } })
    const line = wrapper.findAll('path')[1]
    const d = line.attributes('d') ?? ''
    const ys = [...d.matchAll(/[ML][\d.]+,([\d.]+)/g)].map((m) => Number(m[1]))
    expect(new Set(ys).size).toBe(1)
    expect(ys[0]).toBeCloseTo(14, 0) // mid-height of the 28-tall viewBox
  })
})
