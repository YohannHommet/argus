<script setup lang="ts">
/**
 * Picks between the two event-detail hosts Timeline.vue no longer has to
 * choose between itself: a persistent right-side `EventDetailPanel` on wide
 * viewports (>=1024px — enough room for the timeline plus a readable
 * inspector column) and the overlay `EventDetailSheet` below that, where a
 * permanent panel would starve the timeline list of width. Round-3 critic
 * gap: "prefer a persistent right-side pane on wide viewports... over a
 * modal/overlay drawer".
 *
 * `useMediaQuery` degrades to "narrow" (the Sheet) wherever
 * `window.matchMedia` is unsupported, which is also jsdom's behaviour by
 * default (see that composable's doc) — so the existing Timeline test
 * suite, written against the Sheet, keeps passing unmodified; a dedicated
 * wide-viewport test stubs `matchMedia` to exercise the panel path.
 */
import { useMediaQuery } from '@/composables/useMediaQuery'
import EventDetailPanel from './EventDetailPanel.vue'
import EventDetailSheet from './EventDetailSheet.vue'

interface Props {
  eventRef: string | null
  open: boolean
}

defineProps<Props>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const isWide = useMediaQuery('(min-width: 1024px)')

function onOpenChange(value: boolean) {
  emit('update:open', value)
}
</script>

<template>
  <EventDetailPanel
    v-if="isWide"
    :event-ref="eventRef"
  />
  <EventDetailSheet
    v-else
    :event-ref="eventRef"
    :open="open"
    @update:open="onOpenChange"
  />
</template>
