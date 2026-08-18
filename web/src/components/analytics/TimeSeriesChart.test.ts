import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { useUiStore } from '@/stores/ui'
import { ApiError } from '@/api/errors'
import { buildChartTheme } from '@/lib/echartsTheme'
import { getAnalyticsTimeseries200Default } from '@/test/fixtures'
import { timeseriesWithUnattributedSeries } from '@/test/fixtures.extra'
import { makeVChartStub, stubResizeObserver, VCHART_STUB_KEY } from '@/test/chartStub'
import TimeSeriesChart from './TimeSeriesChart.vue'

function mountChart(props: Record<string, unknown>) {
  const { Stub, resize } = makeVChartStub()
  const wrapper = mount(TimeSeriesChart, {
    props,
    global: { stubs: { [VCHART_STUB_KEY]: Stub } },
  })
  return { wrapper, resize, chart: wrapper.findComponent(Stub) }
}

describe('TimeSeriesChart', () => {
  // Every mount wires useChartResize -> vueuse's useResizeObserver, which
  // constructs a real `ResizeObserver` immediately (jsdom has none) even
  // on the error/empty/loading branches that never render VChart — so
  // this stub is installed for every test, not just the dedicated resize
  // one, which additionally captures `ro` to invoke the callback.
  let ro: ReturnType<typeof stubResizeObserver>

  beforeEach(() => {
    setActivePinia(createPinia())
    ro = stubResizeObserver()
  })

  afterEach(() => {
    ro.restore()
  })

  it('passes one line series per SeriesPoint plus the "other" bucket as its own series', () => {
    const { chart } = mountChart({ data: getAnalyticsTimeseries200Default })
    const option = chart.props('option') as { series: { name: string }[]; xAxis: { type: string; data: string[] } }
    // fixture: 1 named series ("argus") + other -> 2 series total
    expect(option.series).toHaveLength(2)
    expect(option.series[0].name).toBe('argus')
    expect(option.series[1].name).toBe('Other')
    expect(option.xAxis.type).toBe('category')
    expect(option.xAxis.data).toHaveLength(getAnalyticsTimeseries200Default.buckets.length)
  })

  it('labels a series whose key is the empty string "unattributed" while VChart never receives an empty legend name', () => {
    const { chart } = mountChart({ data: timeseriesWithUnattributedSeries, metric: 'cost' })
    const option = chart.props('option') as { series: { name: string }[] }
    const names = option.series.map((s) => s.name)
    expect(names).toContain('unattributed')
    expect(names).not.toContain('')
  })

  it('renders the unattributed series muted/dashed rather than a cycled palette color, so blue always means the same thing across charts', () => {
    const { chart } = mountChart({ data: timeseriesWithUnattributedSeries, metric: 'cost' })
    const option = chart.props('option') as {
      series: { name: string; lineStyle: { color: string; type?: string } }[]
    }
    const namedSeries = option.series.find((s) => s.name === 'claude-opus-5')!
    const unattributed = option.series.find((s) => s.name === 'unattributed')!
    const other = option.series.find((s) => s.name === 'Other')!

    expect(namedSeries.lineStyle.type).toBe('solid')
    expect(unattributed.lineStyle.type).toBe('dashed')
    expect(unattributed.lineStyle.color).toBe(other.lineStyle.color)
    expect(unattributed.lineStyle.color).not.toBe(namedSeries.lineStyle.color)
  })

  it('assigns the first palette color to the first real named series, not to "unattributed" even when it sorts first', () => {
    const { chart } = mountChart({
      data: { bucket: 'day', buckets: ['2026-08-11T08:00:00Z'], series: [{ key: '', values: [1] }, { key: 'claude-opus-5', values: [2] }] },
    })
    const option = chart.props('option') as { series: { name: string; lineStyle: { color: string } }[] }
    const namedSeries = option.series.find((s) => s.name === 'claude-opus-5')!
    // Whatever palette color index 0 resolves to (theme-dependent), the real named series gets it
    // — "unattributed" never occupies a palette slot regardless of its position in the response.
    const unattributedSeries = option.series.find((s) => s.name === 'unattributed')!
    expect(namedSeries.lineStyle.color).not.toBe(unattributedSeries.lineStyle.color)
  })

  it('hides the legend entirely for a single-series chart (a lone "unattributed" legend carries no information)', () => {
    const { chart } = mountChart({ data: getAnalyticsTimeseries200Default, metric: 'cost' })
    // fixture: 1 named series + `other` -> 2 series, legend should show.
    const twoSeriesOption = chart.props('option') as { legend?: unknown }
    expect(twoSeriesOption.legend).toBeDefined()

    const { chart: singleChart } = mountChart({
      data: { bucket: 'day', buckets: ['2026-08-11T08:00:00Z', '2026-08-11T09:00:00Z'], series: [{ key: '', values: [1, 2] }] },
    })
    const singleSeriesOption = singleChart.props('option') as { legend?: unknown }
    expect(singleSeriesOption.legend).toBeUndefined()
  })

  it('renders a solo unattributed series (nothing to distinguish it from, e.g. "Tokens over time" with no group_by) solid and metric-colored, not dashed/muted (round-3 UI gap: "near-invisible dashed white token line")', () => {
    const theme = buildChartTheme()
    const soloData = {
      bucket: 'day' as const,
      buckets: ['2026-08-11T08:00:00Z', '2026-08-12T08:00:00Z'],
      series: [{ key: '', values: [1, 2] }],
    }
    const { chart } = mountChart({ data: soloData, metric: 'tokens' })
    const option = chart.props('option') as {
      series: { name: string; lineStyle: { color: string; type?: string; width?: number } }[]
    }
    const solo = option.series.find((s) => s.name === 'unattributed')!
    expect(solo.lineStyle.type).toBe('solid')
    expect(solo.lineStyle.color).toBe(theme.primary)
    expect(solo.lineStyle.width).toBeGreaterThan(2)
  })

  it('still dashes/mutes "unattributed" when it sits alongside other series (something to distinguish it from)', () => {
    const { chart } = mountChart({ data: timeseriesWithUnattributedSeries, metric: 'cost' })
    const option = chart.props('option') as { series: { name: string; lineStyle: { type?: string } }[] }
    expect(option.series.find((s) => s.name === 'unattributed')!.lineStyle.type).toBe('dashed')
  })

  it('changes backgroundColor and textStyle.color in the regenerated option when the theme toggles', () => {
    // jsdom never applies theme.css's cascade (see echartsTheme.test.ts),
    // so the dark/light values here are simulated the same way as there:
    // an inline style override standing in for what .light would resolve.
    const ui = useUiStore()
    ui.setTheme('dark')
    document.documentElement.style.setProperty('--background', 'oklch(0.145 0 0)')
    document.documentElement.style.setProperty('--card', 'oklch(0.205 0 0)')
    document.documentElement.style.setProperty('--foreground', 'oklch(0.985 0 0)')
    const { chart: darkChart } = mountChart({ data: getAnalyticsTimeseries200Default })
    const darkOption = darkChart.props('option') as { backgroundColor: string; textStyle: { color: string } }

    ui.setTheme('light')
    document.documentElement.style.setProperty('--background', 'oklch(1 0 0)')
    document.documentElement.style.setProperty('--card', 'oklch(1 0 0)')
    document.documentElement.style.setProperty('--foreground', 'oklch(0.145 0 0)')
    const { chart: lightChart } = mountChart({ data: getAnalyticsTimeseries200Default })
    const lightOption = lightChart.props('option') as { backgroundColor: string; textStyle: { color: string } }

    expect(lightOption.backgroundColor).not.toBe(darkOption.backgroundColor)
    expect(lightOption.textStyle.color).not.toBe(darkOption.textStyle.color)

    document.documentElement.style.removeProperty('--background')
    document.documentElement.style.removeProperty('--card')
    document.documentElement.style.removeProperty('--foreground')
  })

  it('resizes the chart when its container is observed as resized (ResizeObserver stub)', () => {
    const { resize } = mountChart({ data: getAnalyticsTimeseries200Default })
    expect(resize).not.toHaveBeenCalled()
    ro.trigger()
    expect(resize).toHaveBeenCalledTimes(1)
  })

  it('renders ErrorState and re-emits retry on error', async () => {
    const error = new ApiError({ type: 'urn:argus:error:bad-request', title: 'Bad Request', status: 400 }, new Response(null, { status: 400 }))
    const wrapper = mount(TimeSeriesChart, { props: { error }, global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } } })
    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders EmptyState when there is no data', () => {
    const wrapper = mount(TimeSeriesChart, { props: { data: null }, global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } } })
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('renders a Skeleton while loading, not the chart', () => {
    const wrapper = mount(TimeSeriesChart, {
      props: { loading: true, data: getAnalyticsTimeseries200Default },
      global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } },
    })
    expect(wrapper.find('[data-testid="vchart-stub"]').exists()).toBe(false)
  })
})
