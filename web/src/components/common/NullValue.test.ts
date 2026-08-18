import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import NullValue from './NullValue.vue'

describe('NullValue', () => {
  it('renders the em dash', () => {
    const wrapper = mount(NullValue)
    expect(wrapper.text()).toContain('—')
  })

  it('renders a custom label instead of the dash when given one', () => {
    const wrapper = mount(NullValue, { props: { label: 'n/a' } })
    expect(wrapper.text()).toContain('n/a')
  })

  it('exposes the reason via title/aria-label so a mount test can read it without hovering', () => {
    const wrapper = mount(NullValue, {
      props: { reason: 'Not attributable to a single model' },
      global: { stubs: { teleport: true } },
    })
    const trigger = wrapper.find('[title]')
    expect(trigger.attributes('title')).toBe('Not attributable to a single model')
    expect(trigger.attributes('aria-label')).toBe('Not attributable to a single model')
    expect(wrapper.text()).toContain('—')
  })

  it('renders without a tooltip wrapper when reason is absent', () => {
    const wrapper = mount(NullValue)
    expect(wrapper.find('[title]').exists()).toBe(false)
  })

  // Round-3 critic gap: a bare "—" with a dotted underline, repeated down
  // an always-null table column with nothing else beside it, read as a
  // rendering glitch ("clipped glyphs") rather than an intentional null
  // marker. `plain` keeps the same title/aria-label tooltip contract and
  // just drops the underline styling for that case.
  it('drops the dotted-underline styling (but keeps the same title/aria-label) when plain is set', () => {
    const wrapper = mount(NullValue, { props: { reason: 'Claude Code does not emit per-agent cost', plain: true } })
    const trigger = wrapper.get('[title]')
    expect(trigger.attributes('title')).toBe('Claude Code does not emit per-agent cost')
    expect(trigger.classes()).not.toContain('underline')
  })

  it('keeps the dotted-underline styling by default (plain unset)', () => {
    const wrapper = mount(NullValue, { props: { reason: 'Not measured' } })
    expect(wrapper.get('[title]').classes()).toContain('underline')
  })

  // NOT COVERED (honestly): TooltipContent's `{{ reason }}` interpolation
  // (the line inside <TooltipContent> in NullValue.vue) only renders once
  // reka-ui's Presence/PopperContent machinery actually opens the
  // tooltip. Forcing that open in jsdom needs reka-ui's real
  // Teleport+Presence path (stubbing `teleport: true` renders an empty
  // <teleport-stub> with no children at all — verified by inspecting
  // wrapper.html() after a `focus` trigger), and the real path throws
  // `ReferenceError: ResizeObserver is not defined` (reka-ui's
  // useSize()/PopperContent depend on it; jsdom does not implement it).
  // Polyfilling ResizeObserver would require editing src/test-setup.ts,
  // which is outside the test-file-only scope for this pass. This matches
  // the component's own doc comment above the template: "a mount test
  // can't drive that without simulating pointer events" — the tooltip's
  // *trigger* (title/aria-label) is fully covered by the tests above,
  // which is the whole reason that duplication exists in the component.
})
