import type { components } from './schema'

/**
 * The RFC 9457 problem+json body every Argus error response carries (SPEC
 * §4.1). Re-exported from the generated schema so ApiError can never drift
 * from the wire contract.
 */
export type Problem = components['schemas']['Problem']

/**
 * Thrown by {@link unwrap} (client.ts) whenever the server actually
 * responded with a problem+json error body. Distinct on purpose from a
 * network failure or an aborted request: those never reached a response,
 * so they are not — and must not be modeled as — an ApiError (they
 * propagate as whatever `fetch` itself threw).
 */
export class ApiError extends Error {
  readonly type: string
  readonly title: string
  readonly status: number
  readonly detail?: string
  readonly instance?: string
  /**
   * chi's per-request id (SPEC §4.1). Carried onto the error so the UI's
   * problem banner can show the one value that joins a client-visible
   * failure back to the server log line holding the real error text — the
   * entire reason the field exists on every problem body. Optional: the
   * ingest mounts' own problem+json encoder predates the field and does not
   * set it.
   */
  readonly requestId?: string
  readonly errors?: Problem['errors']
  readonly response: Response

  constructor(problem: Problem, response: Response) {
    super(problem.detail || problem.title)
    this.name = 'ApiError'
    this.type = problem.type
    this.title = problem.title
    this.status = problem.status
    this.detail = problem.detail
    this.instance = problem.instance
    this.requestId = problem.request_id
    this.errors = problem.errors
    this.response = response
  }
}

function isProblem(body: unknown): body is Problem {
  return (
    typeof body === 'object' &&
    body !== null &&
    typeof (body as Record<string, unknown>).type === 'string' &&
    typeof (body as Record<string, unknown>).title === 'string' &&
    typeof (body as Record<string, unknown>).status === 'number'
  )
}

/**
 * Builds an ApiError from whatever body openapi-fetch parsed off a non-2xx
 * response. Argus always emits problem+json (SPEC §4.1), but a body that
 * doesn't match that shape (an intermediary's HTML error page, an empty
 * body) still came from a real HTTP response — so it gets a fallback
 * Problem built from the response's own status, not a silently invented
 * empty `type`.
 */
export function toApiError(body: unknown, response: Response): ApiError {
  if (isProblem(body)) {
    return new ApiError(body, response)
  }
  return new ApiError(
    {
      type: 'urn:argus:error:unrecognized-error-body',
      title: response.statusText || 'Unknown error',
      status: response.status,
    },
    response,
  )
}
