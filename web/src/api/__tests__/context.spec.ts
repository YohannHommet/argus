import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import {
  __resetApiClientSingleton,
  apiClientKey,
  provideApiClient,
  useApiClient,
} from '@/api/context'
import { createApiClient, type ApiClient } from '@/api/client'

afterEach(() => {
  __resetApiClientSingleton()
})

/** A client identity we can assert on without caring about its behaviour. */
function fakeClient(): ApiClient {
  return createApiClient({ fetch: () => Promise.reject(new Error('not called')) })
}

describe('useApiClient', () => {
  it('returns the module singleton when nothing is provided, outside any component', () => {
    // The production path: a pinia store action invoked from a setInterval
    // callback has no injection context at all.
    const client = useApiClient()
    expect(client).toBeDefined()
    expect(typeof client.GET).toBe('function')
  })

  it('returns the same singleton instance on repeated calls', () => {
    expect(useApiClient()).toBe(useApiClient())
  })

  it('hands out a fresh singleton after __resetApiClientSingleton', () => {
    const first = useApiClient()
    __resetApiClientSingleton()
    expect(useApiClient()).not.toBe(first)
  })

  it('prefers a provided client over the singleton inside a component', () => {
    const injected = fakeClient()
    let seen: ApiClient | null = null

    const Child = defineComponent({
      setup() {
        seen = useApiClient()
        return () => h('div')
      },
    })

    mount(
      defineComponent({
        setup() {
          provideApiClient(injected)
          return () => h(Child)
        },
      }),
    )

    expect(seen).toBe(injected)
    expect(seen).not.toBe(useApiClient())
  })

  it('falls back to the singleton inside a component when no provider is in scope', () => {
    let seen: ApiClient | null = null

    mount(
      defineComponent({
        setup() {
          seen = useApiClient()
          return () => h('div')
        },
      }),
    )

    // hasInjectionContext() is true here, but inject() finds nothing — the
    // `injected` guard is what stops a null from being returned as a client.
    expect(seen).toBe(useApiClient())
  })

  it('exposes an injection key usable directly via mount provide', () => {
    const injected = fakeClient()
    let seen: ApiClient | null = null

    mount(
      defineComponent({
        setup() {
          seen = useApiClient()
          return () => h('div')
        },
      }),
      { global: { provide: { [apiClientKey as symbol]: injected } } },
    )

    expect(seen).toBe(injected)
  })
})
