import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SkeletonCard from './SkeletonCard.vue'
import SkeletonChart from './SkeletonChart.vue'
import SkeletonTable from './SkeletonTable.vue'

describe('Skeleton*.vue compositions', () => {
  it('SkeletonTable renders `rows` shimmer bars (default 8)', () => {
    const wrapper = mount(SkeletonTable)
    expect(wrapper.get('[data-testid="skeleton-table"]').findAll('[data-slot="skeleton"]')).toHaveLength(8)
  })

  it('SkeletonTable respects a custom row count', () => {
    const wrapper = mount(SkeletonTable, { props: { rows: 3 } })
    expect(wrapper.get('[data-testid="skeleton-table"]').findAll('[data-slot="skeleton"]')).toHaveLength(3)
  })

  it('SkeletonCard renders a title bar and a value bar inside a Card shell', () => {
    const wrapper = mount(SkeletonCard)
    expect(wrapper.get('[data-testid="skeleton-card"]').findAll('[data-slot="skeleton"]')).toHaveLength(2)
  })

  it('SkeletonChart renders one full-width shimmer block sized like the real charts', () => {
    const wrapper = mount(SkeletonChart)
    const el = wrapper.get('[data-testid="skeleton-chart"]')
    expect(el.classes()).toContain('h-64')
  })

  it('SkeletonChart accepts a custom height class', () => {
    const wrapper = mount(SkeletonChart, { props: { heightClass: 'h-32' } })
    expect(wrapper.get('[data-testid="skeleton-chart"]').classes()).toContain('h-32')
  })
})
