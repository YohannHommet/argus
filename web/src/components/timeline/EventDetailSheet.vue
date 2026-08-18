<script setup lang="ts">
/**
 * The Timeline's detail drawer for narrow viewports: an overlay `Sheet`
 * hosting `EventDetailContent.vue` (the actual fetch + structured-summary +
 * raw-attrs rendering — see that file's doc for why it was extracted).
 * `EventInspector.vue` is what decides between this and the wide-viewport
 * persistent panel (`EventDetailPanel.vue`); this component's own
 * data-testid (`event-detail-sheet`) and prop contract are unchanged from
 * before that split, so it can also still be mounted and tested standalone.
 *
 * Open state is a controlled `v-model:open` — the host decides when to open
 * it (on an EventRow's `open` event) and owns the currently-selected
 * `eventRef`.
 */
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import EventDetailContent from './EventDetailContent.vue'

interface Props {
  /** The event to show, or null to keep the sheet closed/empty. */
  eventRef: string | null
  open: boolean
}

defineProps<Props>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

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

      <div class="overflow-auto px-4 pb-4">
        <EventDetailContent :event-ref="eventRef" />
      </div>
    </SheetContent>
  </Sheet>
</template>
