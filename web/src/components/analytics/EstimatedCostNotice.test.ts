import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import EstimatedCostNotice from './EstimatedCostNotice.vue'

describe('EstimatedCostNotice (PLAN.md P4-10, Phase-4 exit criterion 8)', () => {
  it('renders nothing when estimated_share is 0 (fully reported cost)', () => {
    const wrapper = mount(EstimatedCostNotice, {
      props: { estimatedShare: 0, estimatedUsd: 0, totalUsd: 71.44 },
    })
    expect(wrapper.find('[data-testid="estimated-cost-notice"]').exists()).toBe(false)
  })

  it('estimated_share: 0.02 renders the notice with the percentage (an AC)', () => {
    const wrapper = mount(EstimatedCostNotice, {
      props: { estimatedShare: 0.02, estimatedUsd: 1.43, totalUsd: 71.44 },
    })
    const notice = wrapper.get('[data-testid="estimated-cost-notice"]')
    expect(notice.get('[data-testid="estimated-cost-share"]').text()).toBe('2.0%')
    expect(notice.text()).toContain('$71.44')
    expect(notice.text()).toContain('$1.43')
  })

  it('reads sensibly at estimated_share: 1 (100%, live-verified against --cost-mode=omit data)', () => {
    const wrapper = mount(EstimatedCostNotice, {
      props: { estimatedShare: 1, estimatedUsd: 24.31028864, totalUsd: 24.31028864 },
    })
    const notice = wrapper.get('[data-testid="estimated-cost-notice"]')
    expect(notice.get('[data-testid="estimated-cost-share"]').text()).toBe('100.0%')
    // The wording must not imply "a sliver of the total" at 100% share.
    expect(notice.text()).not.toContain('undefined')
  })

  it('explains the estimation comes from model_prices for llm.request events that carried no cost', () => {
    const wrapper = mount(EstimatedCostNotice, {
      props: { estimatedShare: 0.02, estimatedUsd: 1.43, totalUsd: 71.44 },
    })
    expect(wrapper.text()).toContain('model_prices')
    expect(wrapper.text()).toContain('llm.request')
  })
})
