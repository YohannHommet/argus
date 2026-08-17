import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import { ApiError } from '@/api/errors'
import QualityTiles from './QualityTiles.vue'

describe('QualityTiles', () => {
  it('renders all six tiles PLAN.md P4-09 names', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 0 } })

    expect(wrapper.find('[data-testid="quality-tile-unknown-events"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-tile-dropped-total"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-tile-partial-sessions"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-tile-clock-skewed"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-tile-heuristic-share"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-tile-oldest-raw-event"]').exists()).toBe(true)
  })

  it('every tile carries an explanation of what it means and what to do about it', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 5 } })

    const explanations = wrapper.findAll('[data-testid="quality-tile-explanation"]')
    // 2 built-in explanation paragraphs (unknown-events, dropped-total) +
    // 4 more sitting alongside the StatTile-based tiles.
    expect(explanations.length).toBe(6)
    for (const explanation of explanations) {
      expect(explanation.text().length).toBeGreaterThan(20)
    }
  })

  it('the five tiles with no backing API field render — with an honest "not exposed" reason, never a fabricated 0', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 0 } })

    for (const testId of [
      'quality-tile-partial-sessions',
      'quality-tile-clock-skewed',
      'quality-tile-heuristic-share',
      'quality-tile-oldest-raw-event',
    ]) {
      const tile = wrapper.get(`[data-testid="${testId}"]`)
      expect(tile.text()).toContain('—')
      expect(tile.text()).not.toMatch(/\b0\b/)
    }

    const dropped = wrapper.get('[data-testid="quality-tile-dropped-total-value"]')
    expect(dropped.text()).toContain('—')
  })

  it('unknown-kind events: a real measured 0 renders as "0", not a dash, and no warn colour', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 0 } })

    const value = wrapper.get('[data-testid="quality-tile-unknown-events-value"]')
    expect(value.text()).toBe('0')
    expect(value.classes()).not.toContain('text-warn')
  })

  it('unknown-kind events: a non-zero count renders in the warn colour', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 41 } })

    const value = wrapper.get('[data-testid="quality-tile-unknown-events-value"]')
    expect(value.text()).toBe('41')
    expect(value.classes()).toContain('text-warn')
  })

  it('unknown-kind events: null (not yet loaded) renders — via NullValue, not 0', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: null } })

    const value = wrapper.get('[data-testid="quality-tile-unknown-events-value"]')
    expect(value.text()).toBe('—')
  })

  it('shows a Skeleton (not a value) while the unknown-events fetch is loading', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: null, unknownEventsLoading: true } })

    expect(wrapper.find('[data-testid="quality-tile-unknown-events-value"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-tile-unknown-events"]').text()).not.toContain('0')
  })

  it('shows ErrorState and emits retry on the unknown-events tile when it errors', async () => {
    const error = new ApiError({ type: 'urn:argus:error:boom', title: 'Boom', status: 500 }, new Response(null, { status: 500 }))
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: null, unknownEventsError: error } })

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('[data-testid="error-state"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('dropped total: a hypothetical non-zero value (future-proofing, always null today) also renders in the warn colour', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 0, droppedTotal: 7 } })

    const value = wrapper.get('[data-testid="quality-tile-dropped-total-value"]')
    expect(value.text()).toBe('7')
    expect(value.classes()).toContain('text-warn')
  })

  it('oldest raw event reason mentions retention_days when supplied by the meta store', () => {
    const wrapper = mount(QualityTiles, { props: { unknownEventsTotal: 0, retentionDays: 90 } })

    const tile = wrapper.get('[data-testid="quality-tile-oldest-raw-event"]')
    expect(tile.html()).toContain('90 days')
  })
})
