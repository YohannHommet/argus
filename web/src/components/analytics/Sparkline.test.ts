import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { buildChartTheme, metricColor } from '@/lib/echartsTheme'
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

  it('defaults to the neutral/primary hue when metricKey is omitted', () => {
    const theme = buildChartTheme()
    const wrapper = mount(Sparkline, { props: { values: [1, 5, 3] } })
    expect(wrapper.findAll('path')[1].attributes('stroke')).toBe(theme.primary)
  })

  it("colors the stroke and endpoint by the metric's own semantic hue (round-3 UI pass: sparklines were all one undifferentiated blue/gray)", () => {
    const theme = buildChartTheme()
    const wrapper = mount(Sparkline, { props: { values: [1, 5, 3], metricKey: 'api_errors' } })
    const destructive = metricColor(theme, 'api_errors')
    expect(destructive).toBe(theme.reject)
    expect(wrapper.findAll('path')[1].attributes('stroke')).toBe(destructive)
    expect(wrapper.get('circle').attributes('fill')).toBe(destructive)
  })

  it('uses the secondary hue for a muted-class metric like sessions, distinct from cost/tokens', () => {
    const theme = buildChartTheme()
    const costWrapper = mount(Sparkline, { props: { values: [1, 2, 3], metricKey: 'cost' } })
    const sessionsWrapper = mount(Sparkline, { props: { values: [1, 2, 3], metricKey: 'sessions' } })
    const costColor = costWrapper.findAll('path')[1].attributes('stroke')
    const sessionsColor = sessionsWrapper.findAll('path')[1].attributes('stroke')
    expect(sessionsColor).toBe(theme.categoricalPalette[1])
    expect(sessionsColor).not.toBe(costColor)
  })
})
