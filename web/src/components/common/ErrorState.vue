<script setup lang="ts">
import { computed } from 'vue'

import { Button } from '@/components/ui/button'
import { ApiError } from '@/api/errors'

interface Props {
  /**
   * Whatever the failed call threw. An {@link ApiError} means the server
   * answered with an RFC 9457 problem+json body (SPEC §4.1) and every field
   * below is real; anything else is a transport failure with only a message.
   */
  error?: ApiError | Error | null
  /** Overrides the heading; defaults to the problem's own `title`. */
  title?: string
  /** Hides the retry button for panels whose parent owns the refetch. */
  retryable?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  error: null,
  title: undefined,
  retryable: true,
})

const emit = defineEmits<{ retry: [] }>()

const problem = computed(() => (props.error instanceof ApiError ? props.error : null))

const heading = computed(() => props.title ?? problem.value?.title ?? 'Request failed')

// A transport failure's `message` is all there is; a problem+json body's
// `detail` is the human-readable half and its `title` is already the heading,
// so showing `message` too would just repeat it.
const detail = computed(() => problem.value?.detail ?? props.error?.message ?? null)

/** One `k=v, k=v` line per `Problem.errors` entry, keys and values verbatim. */
const validationErrors = computed(() =>
  (problem.value?.errors ?? []).map((entry) =>
    Object.entries(entry)
      .map(([key, value]) => `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}`)
      .join(', '),
  ),
)
</script>

<template>
  <div
    role="alert"
    class="border-destructive/40 bg-destructive/10 flex flex-col gap-3 rounded-lg border p-4"
    data-testid="error-state"
  >
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <p class="text-destructive text-sm font-semibold">
          <span
            v-if="problem"
            class="font-mono"
          >{{ problem.status }}</span>
          {{ heading }}
        </p>
        <p
          v-if="detail"
          class="text-foreground/80 mt-1 text-sm break-words"
        >
          {{ detail }}
        </p>
        <!--
          The `type` URN is the stable, greppable identity of the failure
          (SPEC §4.1: `urn:argus:error:invalid-cursor`). It is the field an
          operator quotes in a bug report, so it is shown rather than hidden
          behind a console log.
        -->
        <p
          v-if="problem"
          class="text-muted-foreground mt-2 font-mono text-xs break-all"
        >
          {{ problem.type }}
        </p>
      </div>

      <Button
        v-if="retryable"
        variant="outline"
        size="sm"
        class="shrink-0"
        @click="emit('retry')"
      >
        Retry
      </Button>
    </div>

    <!--
      Field-level validation errors, when the problem carries them.
      `Problem.errors` is an array of free-form objects in openapi.yaml, so
      each entry's keys are rendered raw rather than prettified into
      something that no longer matches the request that produced them.
    -->
    <ul
      v-if="validationErrors.length > 0"
      class="text-muted-foreground space-y-1 text-xs"
    >
      <li
        v-for="(entry, index) in validationErrors"
        :key="index"
        class="font-mono break-words"
      >
        {{ entry }}
      </li>
    </ul>

    <!--
      SPEC §4.1's `request_id` is on every problem body precisely so an
      operator can join a client-visible failure to the server log line
      carrying the real error text. Showing it here is the whole point of
      the field.
    -->
    <p
      v-if="problem?.requestId"
      class="text-muted-foreground font-mono text-xs"
    >
      request_id: {{ problem.requestId }}
    </p>
  </div>
</template>
