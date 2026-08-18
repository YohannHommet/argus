<script setup lang="ts">
/**
 * The wide-viewport counterpart to `EventDetailSheet.vue`: a persistent
 * right-side pane, always in the layout (not a modal/overlay), so the
 * timeline and the inspector are visible together — the Langfuse
 * span-tree + inspector idiom the round-3 critic asked for explicitly,
 * which an overlay `Sheet` can never give since it covers the tree it's
 * explaining. `EventInspector.vue` decides when to mount this vs. the
 * `Sheet`; this component itself has no open/closed state — "nothing
 * selected yet" is just another content state of `EventDetailContent`.
 */
import EventDetailContent from './EventDetailContent.vue'

interface Props {
  eventRef: string | null
}

defineProps<Props>()
</script>

<template>
  <aside
    class="border-border bg-background flex w-96 shrink-0 flex-col border-l"
    data-testid="event-inspector-panel"
  >
    <header class="border-border border-b px-4 py-3">
      <h2 class="text-sm font-semibold">
        Event detail
      </h2>
    </header>
    <div class="flex-1 overflow-auto px-4 py-3">
      <EventDetailContent :event-ref="eventRef" />
    </div>
  </aside>
</template>
