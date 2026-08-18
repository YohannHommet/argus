import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { ApiError } from '@/api/errors'

function apiError(overrides: Partial<ConstructorParameters<typeof ApiError>[0]> = {}) {
  return new ApiError(
    {
      type: 'urn:argus:error:invalid-cursor',
      title: 'Invalid cursor',
      status: 400,
      detail: 'cursor failed to decode',
      request_id: 'req-abc123',
      ...overrides,
    },
    new Response(null, { status: 400, statusText: 'Bad Request' }),
  )
}

describe('ErrorState', () => {
  it('renders an ApiError as a problem banner, not a blank page', () => {
    const wrapper = mount(ErrorState, { props: { error: apiError() } })
    const text = wrapper.text()

    expect(wrapper.attributes('role')).toBe('alert')
    expect(text).toContain('400')
    expect(text).toContain('Invalid cursor')
    expect(text).toContain('cursor failed to decode')
    // The stable URN and the request id are the two fields an operator
    // quotes in a bug report; both must be on screen.
    expect(text).toContain('urn:argus:error:invalid-cursor')
    expect(text).toContain('req-abc123')
  })

  it('renders Problem.errors entries verbatim as key=value lines', () => {
    const wrapper = mount(ErrorState, {
      props: { error: apiError({ errors: [{ field: 'limit', reason: 'must be <= 500' }] }) },
    })
    expect(wrapper.text()).toContain('field=limit')
    expect(wrapper.text()).toContain('reason=must be <= 500')
  })

  it('falls back to a transport error message when the server never answered', () => {
    const wrapper = mount(ErrorState, { props: { error: new TypeError('Failed to fetch') } })
    expect(wrapper.text()).toContain('Request failed')
    expect(wrapper.text()).toContain('Failed to fetch')
    // No problem+json body means no status/URN to invent.
    expect(wrapper.text()).not.toContain('urn:argus')
  })

  it('emits retry when the retry button is clicked', async () => {
    const wrapper = mount(ErrorState, { props: { error: apiError() } })
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('hides the retry button when the parent owns the refetch', () => {
    const wrapper = mount(ErrorState, { props: { error: apiError(), retryable: false } })
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('renders with no error at all: default heading, no detail paragraph, no problem fields', () => {
    const wrapper = mount(ErrorState)

    expect(wrapper.text()).toContain('Request failed')
    // Neither the problem status/type/request_id (no ApiError) nor a
    // detail paragraph (no `detail`, no `message`) should render.
    expect(wrapper.findAll('p')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('urn:argus')
    expect(wrapper.text()).not.toContain('request_id')
  })

  it('falls back to the problem title as detail when an ApiError carries no detail', () => {
    const wrapper = mount(ErrorState, {
      props: { error: apiError({ detail: undefined, title: 'Gateway timeout' }) },
    })

    // `detail` computed falls through problem.detail (absent) to
    // `props.error.message`, which ApiError sets to `detail || title` —
    // so the title text appears as the detail line too.
    expect(wrapper.text()).toContain('Gateway timeout')
  })

  it('renders no detail paragraph for a transport error with an empty message', () => {
    const wrapper = mount(ErrorState, { props: { error: new Error('') } })

    expect(wrapper.text()).toContain('Request failed')
    // Only the heading paragraph — no detail line, since both
    // `problem?.detail` and `error.message` are falsy.
    expect(wrapper.findAll('p')).toHaveLength(1)
  })

  it('renders Problem.errors entries with non-string values via JSON.stringify', () => {
    const wrapper = mount(ErrorState, {
      props: { error: apiError({ errors: [{ field: 'limit', max: 500 }] }) },
    })
    expect(wrapper.text()).toContain('field=limit')
    expect(wrapper.text()).toContain('max=500')
  })

  it('an explicit title prop overrides the problem title', () => {
    const wrapper = mount(ErrorState, {
      props: { error: apiError({ title: 'Invalid cursor' }), title: 'Custom heading' },
    })
    expect(wrapper.text()).toContain('Custom heading')
    expect(wrapper.text()).not.toContain('Invalid cursor')
  })
})

describe('EmptyState', () => {
  it('renders the title, the description and an action slot', () => {
    const wrapper = mount(EmptyState, {
      props: { title: 'No sessions match these filters', description: 'Try clearing the project filter.' },
      slots: { default: '<button type="button">Clear filters</button>' },
    })
    expect(wrapper.text()).toContain('No sessions match these filters')
    expect(wrapper.text()).toContain('Try clearing the project filter.')
    expect(wrapper.get('button').text()).toBe('Clear filters')
  })

  it('renders without a description or a slot', () => {
    const wrapper = mount(EmptyState, { props: { title: 'Nothing here' } })
    expect(wrapper.text()).toContain('Nothing here')
  })
})
