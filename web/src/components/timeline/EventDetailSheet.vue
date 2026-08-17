<script setup lang="ts">
/**
 * The Timeline's detail drawer: fetches `GET /events/{ref}` (via the
 * session-detail store's per-entry cache, `loadEvent`) and renders the raw
 * `attrs` payload. Addressed by `event_ref` only — there is no lookup by
 * `id` (SPEC §4.3) — so `eventRef` is exactly what a `TimelineItem`/raw
 * `TimelineEvent`'s `event_ref` field already is.
 *
 * Open state is a controlled `v-model:open` — the host (Timeline.vue)
 * decides when to open it (on an EventRow's `open` event) and owns the
 * currently-selected `eventRef`.
 */
import { ref, watch } from 'vue'

import { ApiError } from '@/api/errors'
import { useSessionDetailStore } from '@/stores/sessionDetail'
import type { EventDetail } from '@/stores/sessionDetail'
import { formatAbsoluteTime } from '@/lib/format'
import ErrorState from '@/components/common/ErrorState.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import JsonViewer from '@/components/common/JsonViewer.vue'
import { Skeleton } from '@/components/ui/skeleton'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'

interface Props {
  /** The event to show, or null to keep the sheet closed/empty. */
  eventRef: string | null
  open: boolean
}

const props = defineProps<Props>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const store = useSessionDetailStore()

const detail = ref<EventDetail | null>(null)
const loading = ref(false)
const error = ref<ApiError | Error | null>(null)

async function fetchEvent(ref: string) {
  loading.value = true
  error.value = null
  detail.value = null
  try {
    detail.value = await store.loadEvent(ref)
  } catch (err) {
    error.value = err instanceof Error ? err : new Error(String(err))
  } finally {
    loading.value = false
  }
}

watch(
  () => props.eventRef,
  (ref) => {
    if (ref) void fetchEvent(ref)
  },
  { immediate: true },
)

function retry() {
  if (props.eventRef) void fetchEvent(props.eventRef)
}

function onOpenChange(value: boolean) {
  emit('update:open', value)
}
</script>

<template>
  <Sheet
    :open="open"
    @update:open="onOpenChange"
  >
    <SheetContent
      side="right"
      class="w-full sm:max-w-lg"
      data-testid="event-detail-sheet"
    >
      <SheetHeader>
        <SheetTitle>Event detail</SheetTitle>
      </SheetHeader>

      <div class="flex flex-col gap-4 overflow-auto px-4 pb-4">
        <ErrorState
          v-if="error"
          :error="error"
          @retry="retry"
        />
        <template v-else-if="loading">
          <Skeleton class="h-6 w-2/3" />
          <Skeleton class="h-40 w-full" />
        </template>
        <template v-else-if="detail">
          <dl class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
            <dt class="text-muted-foreground">
              event_ref
            </dt>
            <dd class="font-mono break-all">
              {{ detail.event_ref }}
            </dd>
            <dt class="text-muted-foreground">
              kind
            </dt>
            <dd class="font-mono">
              {{ detail.kind }}
            </dd>
            <dt class="text-muted-foreground">
              source
            </dt>
            <dd class="font-mono">
              {{ detail.source }}
            </dd>
            <dt class="text-muted-foreground">
              ts
            </dt>
            <dd>{{ formatAbsoluteTime(detail.ts) }}</dd>
          </dl>

          <EmptyState
            v-if="Object.keys(detail.attrs).length === 0"
            title="This event has no raw attrs"
            description="Its promoted fields above are all Argus derived from it."
          />
          <JsonViewer
            :value="detail.attrs"
            label="Raw attrs"
          />
        </template>
      </div>
    </SheetContent>
  </Sheet>
</template>
