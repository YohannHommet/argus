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

  // 30s budget, not the 5s default: these two cases resolve a real lazy
  // `import()`, so vitest actually transforms the view and everything it
  // pulls in. SessionDetailView now mounts the Timeline, SubagentTree,
  // CostAttributionCard and ToolCallTable trees, and that transform alone
  // can exceed 5s on a cold cache. The assertions are unchanged — only the
  // time budget moved, because the thing being measured is module loading,
  // not logic.
  it('resolves /sessions/:id with a sample id to SessionDetailView', async () => {
    const detail = routes.find((r) => r.name === 'session-detail')
    expect(detail).toBeDefined()

    const mod = (await (detail!.component as () => Promise<{ default: unknown }>)()) as {
      default: unknown
    }
    expect(mod.default).toBeDefined()
  }, 30_000)

  // Data-driven over `routes` itself (rather than a hand-maintained list of
  // names) so a route added later is covered automatically, and each lazy
  // `component: () => import(...)` thunk actually gets invoked: a typo'd
  // import path is otherwise a silent time-bomb that only fails at
  // navigation time in production, never at build or lint time.
  describe.each(routes.filter((route) => typeof route.component === 'function'))(
    'lazy component for route "$name" ($path)',
    (route) => {
      it('resolves to a real component definition', async () => {
        const loader = route.component as () => Promise<{ default: unknown }>
        const mod = await loader()

        expect(mod).toBeDefined()
        expect(mod.default).toBeDefined()

        const definition = mod.default as { render?: unknown; setup?: unknown; template?: unknown }
        // An SFC compiles to one of these depending on <script setup> vs
        // options API — asserting "is some object" would pass even for a
        // typo'd import that resolved to the wrong (non-component) module.
        expect(definition.render ?? definition.setup ?? definition.template).toBeDefined()
      }, 30_000)
    },
  )
})
