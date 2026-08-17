import { describe, expect, it, vi } from 'vitest'

import { createApiClient, unwrap } from '@/api/client'
import { ApiError } from '@/api/errors'
import type { components } from '@/api/schema'

// A real browser resolves the client's default relative `baseUrl: ''`
// against `document.baseURI`; Node's global `Request` (what jsdom's `fetch`
// delegates to under Vitest) has no such document and rejects a relative
// URL outright. Give tests an absolute base so they exercise `unwrap()` and
// the fake fetch, not this environment gap — `client.ts`'s own default
// stays relative for the app.
const TEST_BASE_URL = 'http://localhost'

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

// openapi-fetch always calls the injected `fetch` with a fully-built
// `Request`, but the DOM/undici `typeof fetch` signature it must satisfy is
// broader (`RequestInfo | URL`). requestFetch keeps the mocks honest about
// that gap instead of narrowing the parameter and relying on a cast: the
// assertion fires loudly if openapi-fetch ever starts passing a bare URL.
function requestFetch(
  handler: (request: Request) => Promise<Response>,
): (input: RequestInfo | URL, init?: RequestInit) => Promise<Response> {
  return (input) => {
    if (!(input instanceof Request)) {
      throw new TypeError(`expected openapi-fetch to call fetch with a Request, got ${typeof input}`)
    }
    return handler(input)
  }
}

function problemResponse(problem: Record<string, unknown>, status: number): Response {
  return new Response(JSON.stringify(problem), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  })
}

describe('unwrap()', () => {
  it('maps a problem+json 400 response to a typed ApiError', async () => {
    const fetchMock = vi.fn(async () =>
      problemResponse(
        {
          type: 'urn:argus:error:invalid-cursor',
          title: 'Bad Request',
          status: 400,
          detail: 'cursor is not valid base64',
          instance: '/api/v1/sessions',
        },
        400,
      ),
    )
    const client = createApiClient({ fetch: fetchMock, baseUrl: TEST_BASE_URL })

    const call = unwrap(client.GET('/api/v1/sessions', {}))

    await expect(call).rejects.toBeInstanceOf(ApiError)
    await expect(call).rejects.toMatchObject({
      type: 'urn:argus:error:invalid-cursor',
      title: 'Bad Request',
      detail: 'cursor is not valid base64',
      status: 400,
    })
  })

  it('returns typed success data', async () => {
    const page: components['schemas']['SessionsListResponse'] = {
      data: [],
      page: { next_cursor: null, has_more: false },
    }
    const fetchMock = vi.fn(async () => jsonResponse(page))
    const client = createApiClient({ fetch: fetchMock, baseUrl: TEST_BASE_URL })

    const result = await unwrap(client.GET('/api/v1/sessions', {}))

    // Typed, not `any`: `result.data` is a SessionSummary[], exercised here
    // rather than merely asserted so a schema.d.ts regression that removes
    // `data`/`page` fails type-check, not just this assertion.
    expect(result.data).toEqual([])
    expect(result.page).toEqual({ next_cursor: null, has_more: false })
  })

  it('rejects with the underlying abort error, not an ApiError, when the request is aborted', async () => {
    const controller = new AbortController()
    const fetchMock = vi.fn(
      requestFetch(
        (request) =>
          new Promise<Response>((_resolve, reject) => {
            request.signal.addEventListener('abort', () => {
              reject(new DOMException('The operation was aborted.', 'AbortError'))
            })
          }),
      ),
    )
    const client = createApiClient({ fetch: fetchMock, baseUrl: TEST_BASE_URL })

    const call = unwrap(
      client.GET('/api/v1/sessions', { signal: controller.signal }),
    )
    controller.abort()

    await expect(call).rejects.toMatchObject({ name: 'AbortError' })
    await expect(call).rejects.not.toBeInstanceOf(ApiError)
  })

  it('maps a bodyless 502 (no Content-Length, empty body) to a status-based ApiError', async () => {
    // openapi-fetch 0.17.0 resolves `{ error: undefined, response }` when a
    // non-2xx response has no parseable body — a proxy/LB 502 is the
    // realistic shape (SPEC §4.1 error bodies are all argusd-authored
    // problem+json; a 502 never reaches argusd). Regression coverage for
    // m20: branching on `error !== undefined` let this resolve as if it
    // had succeeded.
    const fetchMock = vi.fn(
      async () => new Response(null, { status: 502, headers: { 'Content-Length': '0' } }),
    )
    const client = createApiClient({ fetch: fetchMock, baseUrl: TEST_BASE_URL })

    const call = unwrap(client.GET('/api/v1/sessions', {}))

    await expect(call).rejects.toBeInstanceOf(ApiError)
    await expect(call).rejects.toMatchObject({ status: 502 })
  })

  it('attaches the configured bearer token, never a hardcoded one', async () => {
    let seenAuthHeader: string | null = null
    const fetchMock = vi.fn(
      requestFetch(async (request) => {
        seenAuthHeader = request.headers.get('Authorization')
        return jsonResponse({ data: [], page: { next_cursor: null, has_more: false } })
      }),
    )
    const client = createApiClient({ fetch: fetchMock, token: 'injected-token', baseUrl: TEST_BASE_URL })

    await unwrap(client.GET('/api/v1/sessions', {}))

    expect(seenAuthHeader).toBe('Bearer injected-token')
  })
})

describe('vendor vocabulary stays open (SPEC §0/§4.4)', () => {
  it('accepts a query_source value Argus has never seen — it is typed string, not a union', () => {
    // The value of this test is compile-time: if openapi-typescript ever
    // narrowed `query_sources` to a literal union (e.g. because someone
    // reintroduced `enum:` on query_source in openapi.yaml), this object
    // literal would stop type-checking under `pnpm type-check` (vue-tsc
    // sees this file via tsconfig.vitest.json).
    const facets: components['schemas']['Facets'] = {
      projects: [],
      models: [],
      vendors: [],
      tools: [],
      decision_sources: [],
      query_sources: ['a_future_query_source_never_seen_before'],
    }

    expect(facets.query_sources).toContain('a_future_query_source_never_seen_before')
  })
})
