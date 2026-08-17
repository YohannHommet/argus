import { hasInjectionContext, inject, provide } from 'vue'
import type { InjectionKey } from 'vue'

import { createApiClient } from './client'
import type { ApiClient } from './client'

export const apiClientKey: InjectionKey<ApiClient> = Symbol('apiClient')

/** Thin wrapper over `provide` — keeps the injection key private to this module. */
export function provideApiClient(client: ApiClient): void {
  provide(apiClientKey, client)
}

/**
 * Lazily created default client. Production code (a pinia store's own
 * setup(), a composable called outside a component) needs an ApiClient
 * without any app ever calling `provideApiClient` — tests are the only
 * caller that wires one explicitly, to inject a fake.
 */
let singleton: ApiClient | null = null

function getSingleton(): ApiClient {
  if (!singleton) {
    singleton = createApiClient()
  }
  return singleton
}

/**
 * Returns the provided ApiClient, or the module-level singleton when no
 * provider is in scope. `inject()` throws outside an active
 * component/app instance; a pinia setup-store's own setup() DOES have one
 * (pinia runs it via `app.runWithContext`), but a store action invoked
 * later — e.g. from a `setInterval` callback registered during setup —
 * does not, so the injection lookup is guarded by `hasInjectionContext()`
 * rather than relied upon unconditionally. (`getCurrentInstance()` alone
 * would be too strict: it's null inside a pinia setup store even though
 * `inject()` works there.)
 */
export function useApiClient(): ApiClient {
  if (hasInjectionContext()) {
    const injected = inject(apiClientKey, null)
    if (injected) return injected
  }
  return getSingleton()
}

/** Test-only: clears the module singleton so tests don't leak a fake client across cases. */
export function __resetApiClientSingleton(): void {
  singleton = null
}
