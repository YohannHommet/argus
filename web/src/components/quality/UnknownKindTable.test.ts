import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import { ApiError } from '@/api/errors'
import { getQualityUnknownKinds200Default } from '@/test/fixtures'
import { emptyQualityUnknownKinds, multiRowQualityUnknownKinds } from '@/test/fixtures.extra'
import UnknownKindTable from './UnknownKindTable.vue'

// The sample dialog is teleported (reka-ui's DialogPortal renders into
// document.body, per the established pattern in
// components/timeline/EventDetailSheet.test.ts), so every mount is
// attached there and torn down afterwards.
function mountTable(props: ConstructorParameters<typeof UnknownKindTable>[0] = {}) {
  return mount(UnknownKindTable, { props, attachTo: document.body })
}

describe('UnknownKindTable', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('lists an unmapped event_name with its count', () => {
    const wrapper = mountTable({ rows: getQualityUnknownKinds200Default.rows })

    expect(wrapper.text()).toContain('some_new_event')
    expect(wrapper.get('[data-testid="unknown-kind-count"]').text()).toBe('41')
  })

  it('lists every row when there are several unmapped event names', () => {
    const wrapper = mountTable({ rows: multiRowQualityUnknownKinds.rows })

    expect(wrapper.findAll('[data-testid="unknown-kind-count"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('another_future_event')
  })

  it('the default, clean-data empty state says Argus recognises every event, not just "No data"', () => {
    const wrapper = mountTable({ rows: emptyQualityUnknownKinds.rows })

    const empty = wrapper.get('[data-testid="empty-state"]')
    expect(empty.text()).toContain('No unmapped event names')
    expect(empty.text().toLowerCase()).toContain('recognises every event')
  })

  it('opens the raw sample in the JsonViewer when "View sample" is clicked', async () => {
    const wrapper = mountTable({ rows: getQualityUnknownKinds200Default.rows })

    expect(document.body.querySelector('[data-testid="json-viewer"]')).toBeFalsy()

    await wrapper.get('[data-testid="unknown-kind-view-sample"]').trigger('click')

    const viewer = document.body.querySelector('[data-testid="json-viewer"]')
    expect(viewer).toBeTruthy()
    expect(viewer?.textContent).toContain('raw.attr')
    expect(viewer?.textContent).toContain('value')
  })

  it('renders a loading skeleton, not the table, while loading', () => {
    const wrapper = mountTable({ rows: [], loading: true })

    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(false)
  })

  it('renders ErrorState and emits retry on failure', async () => {
    const error = new ApiError({ type: 'urn:argus:error:boom', title: 'Boom', status: 500 }, new Response(null, { status: 500 }))
    const wrapper = mountTable({ rows: [], error })

    expect(wrapper.find('[data-testid="error-state"]').exists()).toBe(true)
    await wrapper.find('[data-testid="error-state"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('renders an unrecognised source/event_name verbatim via RawValue, never a mapped/undefined label', () => {
    const wrapper = mountTable({
      rows: [
        {
          event_name: 'totally_novel_event_type',
          source: 'sim',
          count: 1,
          first_seen: '2026-08-10T10:00:00Z',
          last_seen: '2026-08-10T10:00:00Z',
          sample: {},
        },
      ],
    })

    expect(wrapper.text()).toContain('totally_novel_event_type')
    expect(wrapper.text()).not.toContain('undefined')
  })
})
