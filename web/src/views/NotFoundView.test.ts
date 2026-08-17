import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import NotFoundView from './NotFoundView.vue'

describe('NotFoundView', () => {
  it('names the path that was not found and links back to /sessions', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/sessions', name: 'sessions', component: { template: '<div/>' } },
        { path: '/:pathMatch(.*)*', name: 'not-found', component: NotFoundView },
      ],
    })
    await router.push('/nope/does-not-exist')
    await router.isReady()

    const wrapper = mount(NotFoundView, { global: { plugins: [router] } })

    expect(wrapper.get('[data-testid="not-found-path"]').text()).toBe('/nope/does-not-exist')

    const link = wrapper.get('[data-testid="not-found-home-link"]')
    expect(link.attributes('href')).toBe('/sessions')
  })
})
