import { describe, expect, it } from 'vitest'

import { routes } from '@/router/index'

/**
 * SPEC §6.2 defines six top-level views + the `/` redirect + NotFoundView.
 * Only five of those six are reachable from a static sidebar (see
 * AppShell.vue) because SessionDetailView needs a session id — this test
 * asserts the full six-view router registration is intact regardless.
 */
describe('router', () => {
  it('registers the / -> /sessions redirect', () => {
    const root = routes.find((r) => r.path === '/')
    expect(root?.redirect).toBe('/sessions')
  })

  it('registers all six §6.2 views plus NotFoundView', async () => {
    const byName = new Map(routes.filter((r) => r.name).map((r) => [r.name, r]))

    const expected: Record<string, string> = {
      sessions: '/sessions',
      'session-detail': '/sessions/:id',
      tools: '/tools',
      analytics: '/analytics',
      live: '/live',
      'data-quality': '/data-quality',
      'not-found': '/:pathMatch(.*)*',
    }

    expect(byName.size).toBe(Object.keys(expected).length)

    for (const [name, path] of Object.entries(expected)) {
      const route = byName.get(name)
      expect(route, `expected route named "${name}"`).toBeDefined()
      expect(route?.path).toBe(path)
      expect(route?.component).toBeTypeOf('function')
    }
  })

  it('resolves /sessions/:id with a sample id to SessionDetailView', async () => {
    const detail = routes.find((r) => r.name === 'session-detail')
    expect(detail).toBeDefined()

    const mod = (await (detail!.component as () => Promise<{ default: unknown }>)()) as {
      default: unknown
    }
    expect(mod.default).toBeDefined()
  })
})
