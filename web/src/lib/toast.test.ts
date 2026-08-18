import { describe, expect, it, vi } from 'vitest'

// `vi.mock` is hoisted above every import in this file, including a `const` above it — the
// factory itself only runs lazily on first import of 'vue-sonner', but `toast.ts` is imported
// statically below, which forces that resolution before a plain `const toastError = vi.fn()`
// would have run. `vi.hoisted` is hoisted together with `vi.mock`, so it runs first instead.
const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast: { error: toastError } }))

import { ApiError } from '@/api/errors'
import { notifyApiFailure } from './toast'

describe('notifyApiFailure', () => {
  it('uses an ApiError\'s detail (falling back to its title) as the toast description', () => {
    const problem = { type: 'urn:argus:error:boom', title: 'Boom', status: 500, detail: 'the database is on fire' }
    notifyApiFailure(new ApiError(problem, new Response(null, { status: 500 })))

    expect(toastError).toHaveBeenCalledWith('Request failed', { description: 'the database is on fire' })
  })

  it('falls back to the title when an ApiError has no detail', () => {
    const problem = { type: 'urn:argus:error:boom', title: 'Boom', status: 500 }
    notifyApiFailure(new ApiError(problem, new Response(null, { status: 500 })))

    expect(toastError).toHaveBeenCalledWith('Request failed', { description: 'Boom' })
  })

  it('uses a plain Error\'s message for a transport failure', () => {
    notifyApiFailure(new Error('network request failed'))
    expect(toastError).toHaveBeenCalledWith('Request failed', { description: 'network request failed' })
  })

  it('accepts a custom title', () => {
    notifyApiFailure(new Error('boom'), { title: 'Background refresh failed' })
    expect(toastError).toHaveBeenCalledWith('Background refresh failed', { description: 'boom' })
  })
})
