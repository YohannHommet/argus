import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { useUiStore } from '@/stores/ui'
import { ApiError } from '@/api/errors'
import { getAnalyticsDecisions200Default } from '@/test/fixtures'
import { decisionsWithUnknownSource } from '@/test/fixtures.extra'
import { makeVChartStub, stubResizeObserver, VCHART_STUB_KEY } from '@/test/chartStub'
import DecisionMatrix from './DecisionMatrix.vue'

function mountMatrix(props: Record<string, unknown>) {
  const { Stub, resize } = makeVChartStub()
  const wrapper = mount(DecisionMatrix, {
    props,
    global: { stubs: { [VCHART_STUB_KEY]: Stub } },
  })
  return { wrapper, resize, chart: wrapper.findComponent(Stub) }
}

describe('DecisionMatrix', () => {
  let ro: ReturnType<typeof stubResizeObserver>

  beforeEach(() => {
    setActivePinia(createPinia())
    ro = stubResizeObserver()
  })

  afterEach(() => {
    ro.restore()
  })

  it('builds the x-axis as the union of every row\'s by_source keys, including a source Argus has never seen', () => {
    const { chart } = mountMatrix({ data: decisionsWithUnknownSource })
    const option = chart.props('option') as { xAxis: { data: string[] }; yAxis: { data: string[] } }
    expect(option.xAxis.data).toEqual(
      expect.arrayContaining(['config', 'hook', 'user_permanent', 'user_temporary', 'user_reject', 'user_abort', 'an_invented_decision_source']),
    )
    // Bash's row never mentions user_reject etc., but the column still exists (union, not per-row).
    expect(option.xAxis.data).toHaveLength(7)
    expect(option.yAxis.data).toEqual(['Edit', 'Bash'])
  })

  it('backfills a missing tool/source combination as a zero-valued cell rather than a ragged grid', () => {
    const { chart } = mountMatrix({ data: decisionsWithUnknownSource })
    const option = chart.props('option') as { series: { data: [number, number, number][] }[] }
    const cells = option.series[0].data
    // rows x cols cartesian product: 2 rows * 7 cols = 14 cells, no gaps.
    expect(cells).toHaveLength(14)
    // Bash (row index 1) has no "an_invented_decision_source" entry -> 0.
    const invented = option.series[0].data.find(([, rowIndex]) => rowIndex === 1)
    expect(invented).toBeDefined()
  })

  it('emits {tool_name, decision_source} on a cell click', () => {
    const { wrapper, chart } = mountMatrix({ data: decisionsWithUnknownSource })
    // [colIndex, rowIndex, value] — column 6 is "an_invented_decision_source" (first-seen order), row 0 is "Edit".
    chart.vm.$emit('click', { value: [6, 0, 6] })
    expect(wrapper.emitted('filter')).toEqual([[{ tool_name: 'Edit', decision_source: 'an_invented_decision_source' }]])
  })

  it('renders every by_source key through RawValue, including the unseen one, verbatim', () => {
    const { wrapper } = mountMatrix({ data: decisionsWithUnknownSource })
    const legend = wrapper.find('[aria-label="decision sources"]')
    expect(legend.text()).toContain('an_invented_decision_source')
  })

  it('renders exact_share and accept/reject totals per row in the legible summary table', () => {
    const { wrapper } = mountMatrix({ data: getAnalyticsDecisions200Default })
    const table = wrapper.find('table')
    expect(table.text()).toContain('300') // accept
    expect(table.text()).toContain('41') // reject
    expect(table.text()).toContain('100.0%') // exact_share: 1
  })

  it('changes backgroundColor and textStyle.color in the regenerated option when the theme toggles', () => {
    const ui = useUiStore()
    ui.setTheme('dark')
    document.documentElement.style.setProperty('--background', 'oklch(0.145 0 0)')
    document.documentElement.style.setProperty('--foreground', 'oklch(0.985 0 0)')
    const { chart: darkChart } = mountMatrix({ data: getAnalyticsDecisions200Default })
    const darkOption = darkChart.props('option') as { backgroundColor: string; textStyle: { color: string } }

    ui.setTheme('light')
    document.documentElement.style.setProperty('--background', 'oklch(1 0 0)')
    document.documentElement.style.setProperty('--foreground', 'oklch(0.145 0 0)')
    const { chart: lightChart } = mountMatrix({ data: getAnalyticsDecisions200Default })
    const lightOption = lightChart.props('option') as { backgroundColor: string; textStyle: { color: string } }

    expect(lightOption.backgroundColor).not.toBe(darkOption.backgroundColor)
    expect(lightOption.textStyle.color).not.toBe(darkOption.textStyle.color)

    document.documentElement.style.removeProperty('--background')
    document.documentElement.style.removeProperty('--foreground')
  })

  it('resizes the chart when its container is observed as resized (ResizeObserver stub)', () => {
    const { resize } = mountMatrix({ data: getAnalyticsDecisions200Default })
    expect(resize).not.toHaveBeenCalled()
    ro.trigger()
    expect(resize).toHaveBeenCalledTimes(1)
  })

  it('renders ErrorState and re-emits retry on error', async () => {
    const error = new ApiError({ type: 'urn:argus:error:bad-request', title: 'Bad Request', status: 400 }, new Response(null, { status: 400 }))
    const wrapper = mount(DecisionMatrix, { props: { error }, global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } } })
    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders EmptyState when there are no rows', () => {
    const wrapper = mount(DecisionMatrix, {
      props: { data: { rows: [] } },
      global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } },
    })
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  it('renders a Skeleton while loading, not the chart', () => {
    const wrapper = mount(DecisionMatrix, {
      props: { loading: true, data: getAnalyticsDecisions200Default },
      global: { stubs: { [VCHART_STUB_KEY]: makeVChartStub().Stub } },
    })
    expect(wrapper.find('[data-testid="vchart-stub"]').exists()).toBe(false)
  })
})
