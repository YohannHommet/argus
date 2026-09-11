import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ChartDataTable from './ChartDataTable.vue'

describe('ChartDataTable', () => {
  it('renders a closed <details> disclosure with the toggle label as its summary', () => {
    const wrapper = mount(ChartDataTable, {
      props: {
        caption: 'Cost by model',
        summary: 'Show data table',
        columns: ['Model', 'Cost'],
        rows: [['claude-opus', '$1.23']],
      },
    })

    const details = wrapper.get('[data-testid="chart-data-table-toggle"]')
    expect(details.element.tagName).toBe('DETAILS')
    expect((details.element as HTMLDetailsElement).open).toBe(false)
    expect(details.get('summary').text()).toBe('Show data table')
  })

  it('renders a real <table> with scoped column headers and one row per data point', () => {
    const wrapper = mount(ChartDataTable, {
      props: {
        caption: 'Cost by model',
        summary: 'Show data table',
        columns: ['Model', 'Cost'],
        rows: [
          ['claude-opus', '$1.23'],
          ['claude-sonnet', '$0.45'],
        ],
      },
    })

    const table = wrapper.get('[data-testid="chart-data-table"]')
    expect(table.element.tagName).toBe('TABLE')

    const headers = table.findAll('th')
    expect(headers.map((h) => h.text())).toEqual(['Model', 'Cost'])
    expect(headers.every((h) => h.attributes('scope') === 'col')).toBe(true)

    const rows = table.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.findAll('td').map((c) => c.text())).toEqual(['claude-opus', '$1.23'])
    expect(rows[1]!.findAll('td').map((c) => c.text())).toEqual(['claude-sonnet', '$0.45'])
  })

  it('carries a screen-reader-only caption naming what the table is a view of', () => {
    const wrapper = mount(ChartDataTable, {
      props: { caption: 'Cost by model', summary: 'Show data table', columns: [], rows: [] },
    })

    expect(wrapper.get('caption').text()).toBe('Cost by model')
    expect(wrapper.get('caption').classes()).toContain('sr-only')
  })
})
