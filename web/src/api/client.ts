import createClient from 'openapi-fetch'
import type { Middleware } from 'openapi-fetch'

import { toApiError } from './errors'
import type { paths } from './schema'

export interface ApiClientOptions {
  /**
   * Root URL prepended to every request. Defaults to `''` (same origin):
   * `server/api/openapi.yaml`'s own `servers` entry is `/`, because Argus
   * serves ops/read/ingest from one origin (SPEC §4.4). That default
   * covers both the embedded-SPA deployment (the server serves the built
   * assets itself) and `pnpm dev` (vite.config.ts proxies `/api`, `/v1`
   * and `/ingest` to the backend) — override only for a cross-origin
   * deployment.
   */
  baseUrl?: string
  /**
   * Bearer token for `ARGUS_API_TOKEN` (SPEC §3.5/§3.6). Must come from
   * runtime configuration/injection — never hardcode it here, and never
   * log it.
   */
  token?: string
  /** Custom fetch implementation, mainly for tests. */
  fetch?: typeof fetch
}

export type ApiClient = ReturnType<typeof createClient<paths>>

/** Builds the typed openapi-fetch client used to talk to Argus's read API. */
export function createApiClient(options: ApiClientOptions = {}): ApiClient {
  const { baseUrl = '', token, fetch: fetchImpl } = options
  const client = createClient<paths>({ baseUrl, fetch: fetchImpl })

  if (token) {
    const authMiddleware: Middleware = {
      onRequest({ request }) {
        request.headers.set('Authorization', `Bearer ${token}`)
        return request
      },
    }
    client.use(authMiddleware)
  }

  return client
}

/**
 * Unwraps an openapi-fetch response into its typed success payload, or
 * throws a typed {@link ApiError} built from the problem+json body (SPEC
 * §4.1). Pass `{ signal }` through to the `GET`/`POST`/… call to support
 * cancellation: an aborted request rejects with the underlying
 * `AbortError` — it never reaches this function's error branch, since
 * `fetch` itself throws before a response exists to build an ApiError
 * from.
 */
export async function unwrap<T>(
  promise: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await promise
  if (error !== undefined) {
    throw toApiError(error, response)
  }
  return data as T
}
