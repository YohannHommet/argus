import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import { ApiError } from '@/api/errors'
import { NOT_ATTRIBUTABLE_TO_MODEL, NOT_MEASURED } from '@/lib/nullReasons'
import StatTile from './StatTile.vue'

describe('StatTile', () => {
  it('renders a measured zero as "0", not "—" (null vs. zero is the whole point)', () => {
    const wrapper = mount(StatTile, { props: { label: 'Tool rejects', value: 0 } })
    const value = wrapper.get('[data-testid="stat-tile-value"]')
    expect(value.text()).toBe('0')
    expect(value.text()).not.toContain('—')
  })

  it('renders "—" plus the default reason for a null value, and no delta', () => {
    const wrapper = mount(StatTile, {
      props: { label: 'Sessions', value: null, delta: 5 },
      global: { stubs: { teleport: true } },
    })
    const value = wrapper.get('[data-testid="stat-tile-value"]')
    expect(value.text()).toContain('—')
    const trigger = wrapper.find('[title]')
    expect(trigger.attributes('title')).toBe(NOT_ATTRIBUTABLE_TO_MODEL)
    expect(wrapper.find('[data-testid="stat-tile-delta"]').exists()).toBe(false)
  })

  it('accepts an explicit reason overriding the default', () => {
    const wrapper = mount(StatTile, {
      props: { label: 'Cost', value: null, reason: NOT_MEASURED },
      global: { stubs: { teleport: true } },
    })
    expect(wrapper.find('[title]').attributes('title')).toBe(NOT_MEASURED)
  })

  it('renders a formatted value and a signed delta vs. the previous window', () => {
    const wrapper = mount(StatTile, { props: { label: 'Cost', value: 71.44, metric: 'cost', delta: 12.3 } })
    expect(wrapper.get('[data-testid="stat-tile-value"]').text()).toBe('$71.44')
    expect(wrapper.get('[data-testid="stat-tile-delta"]').text()).toContain('+$12.30')
  })

  it('renders a negative delta with a minus sign', () => {
    const wrapper = mount(StatTile, { props: { label: 'Cost', value: 10, metric: 'cost', delta: -2 } })
    expect(wrapper.get('[data-testid="stat-tile-delta"]').text()).toContain('−$2.00')
  })

  it('renders no delta section when delta is omitted', () => {
    const wrapper = mount(StatTile, { props: { label: 'Turns', value: 42 } })
    expect(wrapper.find('[data-testid="stat-tile-delta"]').exists()).toBe(false)
  })

  it('renders ErrorState and re-emits retry on error', async () => {
    const error = new ApiError({ type: 'urn:argus:error:bad-request', title: 'Bad Request', status: 400 }, new Response(null, { status: 400 }))
    const wrapper = mount(StatTile, { props: { label: 'Cost', error } })
    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders a Skeleton while loading, not the value', () => {
    const wrapper = mount(StatTile, { props: { label: 'Cost', value: 71.44, loading: true } })
    expect(wrapper.find('[data-testid="stat-tile-value"]').exists()).toBe(false)
  })
})
