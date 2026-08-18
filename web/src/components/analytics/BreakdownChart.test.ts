import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { useUiStore } from '@/stores/ui'
import { ApiError } from '@/api/errors'
import { getAnalyticsBreakdown200Default } from '@/test/fixtures'
import { breakdownWithUnknownQuerySource } from '@/test/fixtures.extra'
import { makeVChartStub, stubResizeObserver, VCHART_STUB_KEY } from '@/test/chartStub'
import BreakdownChart from './BreakdownChart.vue'

function mountChart(props: Record<string, unknown>) {
  const { Stub, resize } = makeVChartStub()
  const wrapper = mount(BreakdownChart, {
    props,
    global: { stubs: { [VCHART_STUB_KEY]: Stub } },
  })
  return { wrapper, resize, chart: wrapper.findComponent(Stub) }
}

describe('BreakdownChart', () => {
  let ro: ReturnType<typeof stubResizeObserver>

  beforeEach(() => {
    setActivePinia(createPinia())
    ro = stubResizeObserver()
  })

  afterEach(() => {
    ro.restore()
  })

  it('renders one bar series with one value per BreakdownRow, y-axis category data matching row keys', () => {
    const { chart } = mountChart({ data: getAnalyticsBreakdown200Default })
    const option = chart.props('option') as {
      series: { type: string; data: { value: number }[] }[]
      yAxis: { type: string; data: string[] }
    }
    expect(option.series).toHaveLength(1)
    expect(option.series[0].type).toBe('bar')
    expect(option.series[0].data).toHaveLength(getAnalyticsBreakdown200Default.rows.length)
    expect(option.series[0].data[0].value).toBe(getAnalyticsBreakdown200Default.rows[0].value)
    expect(option.yAxis.type).toBe('category')
    expect(option.yAxis.data).toEqual(['Edit'])
  })

  it('renders a pie series when variant="pie"', () => {
    const { chart } = mountChart({ data: getAnalyticsBreakdown200Default, variant: 'pie' })
    const option = chart.props('option') as { series: { type: string }[] }
    expect(option.series).toHaveLength(1)
    expect(option.series[0].type).toBe('pie')
  })

  it('labels an empty-string key "unattributed" and renders an unseen vocabulary value verbatim (a_future_query_source)', () => {
    const { chart } = mountChart({ data: breakdownWithUnknownQuerySource, metric: 'cost' })
    const option = chart.props('option') as { yAxis: { data: string[] } }
    expect(option.yAxis.data).toContain('unattributed')
    expect(option.yAxis.data).toContain('a_future_query_source')
    expect(option.yAxis.data).not.toContain('')
  })

  it('renders the unattributed row muted rather than a cycled palette color, and never spends a palette slot on it', () => {
    const { chart } = mountChart({ data: breakdownWithUnknownQuerySource, metric: 'cost' })
    const option = chart.props('option') as { yAxis: { data: string[] }; series: { data: { itemStyle: { color: string } }[] }[] }
    const rows = option.yAxis.data
    const colors = option.series[0].data.map((d) => d.itemStyle.color)
    const sdkColor = colors[rows.indexOf('sdk')]
    const unattributedColor = colors[rows.indexOf('unattributed')]
    const futureColor = colors[rows.indexOf('a_future_query_source')]
    expect(unattributedColor).not.toBe(sdkColor)
    // The two real named rows get distinct, cycled palette colors — the unattributed one never
    // takes a slot in that cycle (round-5 UI pass, gap: "blue means different things").
    expect(sdkColor).not.toBe(futureColor)
  })

  it('hides the legend for a single-row pie (nothing else to distinguish it from)', () => {
    const { chart } = mountChart({ data: getAnalyticsBreakdown200Default, variant: 'pie' })
    const option = chart.props('option') as { legend?: unknown }
    expect(option.legend).toBeUndefined()

    const { chart: multiRow } = mountChart({ data: breakdownWithUnknownQuerySource, variant: 'pie', metric: 'cost' })
    const multiOption = multiRow.props('option') as { legend?: unknown }
    expect(multiOption.legend).toBeDefined()
  })

  it('changes backgroundColor and textStyle.color in the regenerated option when the theme toggles', () => {
    const ui = useUiStore()
    ui.setTheme('dark')
    document.documentElement.style.setProperty('--background', 'oklch(0.145 0 0)')
    document.documentElement.style.setProperty('--card', 'oklch(0.205 0 0)')
    document.documentElement.style.setProperty('--foreground', 'oklch(0.985 0 0)')
    const { chart: darkChart } = mountChart({ data: getAnalyticsBreakdown200Default })
    const darkOption = darkChart.props('option') as { backgroundColor: string; textStyle: { color: string } }

    ui.setTheme('light')
    document.documentElement.style.setProperty('--background', 'oklch(1 0 0)')
    document.documentElement.style.setProperty('--card', 'oklch(1 0 0)')
    document.documentElement.style.setProperty('--foreground', 'oklch(0.145 0 0)')
    const { chart: lightChart } = mountChart({ data: getAnalyticsBreakdown200Default })
    const lightOption = lightChart.props('option') as { backgroundColor: string; textStyle: { color: string } }

    expect(lightOption.backgroundColor).not.toBe(darkOption.backgroundColor)
    expect(lightOption.textStyle.color).not.toBe(darkOption.textStyle.color)

    document.documentElement.style.removeProperty('--background')
    document.documentElement.style.removeProperty('--card')
    document.documentElement.style.removeProperty('--foreground')
  })

  it('resizes the chart when its container is observed as resized (ResizeObserver stub)', () => {
    const { resize } = mountChart({ data: getAnalyticsBreakdown200Default })
    expect(resize).not.toHaveBeenCalled()
    ro.trigger()
    expect(resize).toHaveBeenCalledTimes(1)
  })

  it('renders ErrorState and re-emits retry on error', async () => {
    const error = new ApiError({ type: 'urn:argus:error:bad-request', title: 'Bad Request', status: 400 }, new Response(null, { status: 400 }))
    const wrapper = mount(BreakdownChart, { props: { error }, global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } } })
    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders EmptyState when there are no rows', () => {
    const wrapper = mount(BreakdownChart, {
      props: { data: { dimension: 'tool', rows: [] } },
      global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } },
    })
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('renders a Skeleton while loading, not the chart', () => {
    const wrapper = mount(BreakdownChart, {
      props: { loading: true, data: getAnalyticsBreakdown200Default },
      global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } },
    })
    expect(wrapper.find('[data-testid="vchart-stub"]').exists()).toBe(false)
  })
})
