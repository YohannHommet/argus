import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import AppShell from '@/components/layout/AppShell.vue'
import { routes } from '@/router/index'

/**
 * The sidebar exposes five navigable destinations, not six: SPEC §6.2 lists
 * six top-level routes, but the sixth (`/sessions/:id`) requires a session
 * id and cannot be a static sidebar link. SPEC §6.3 and P1-03 both ask for
 * six nav items, but no sixth navigable route exists — tracked as Phase-1
 * deviation D-1. This test asserts what is actually true, not the
 * unsatisfiable literal "six nav links".
 */
const EXPECTED_NAV_DESTINATIONS = [
  '/sessions',
  '/tools',
  '/analytics',
  '/live',
  '/data-quality',
]

describe('AppShell', () => {
  it('renders five nav links, each resolving to a real registered route', async () => {
    setActivePinia(createPinia())

    const router = createRouter({
      history: createMemoryHistory(),
      routes,
    })
    router.push('/sessions')
    await router.isReady()

    const wrapper = mount(AppShell, {
      global: {
        plugins: [router],
      },
    })

    const links = wrapper.findAll('nav a')
    expect(links).toHaveLength(EXPECTED_NAV_DESTINATIONS.length)

    const hrefs = links.map((link) => link.attributes('href'))
    expect(hrefs).toEqual(EXPECTED_NAV_DESTINATIONS)

    for (const path of EXPECTED_NAV_DESTINATIONS) {
      const resolved = router.resolve(path)
      expect(resolved.matched.length, `route for ${path} should resolve`).toBeGreaterThan(0)
    }
  })
})
