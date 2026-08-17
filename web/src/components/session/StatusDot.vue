<script setup lang="ts">
import { computed } from 'vue'

interface Props {
  /**
   * `status` is a stored Argus column with a documented, closed 4-value vocabulary (SPEC §1.7:
   * active/ended/abandoned/unknown) — unlike a vendor field, it's reasonable to switch on those four
   * values by name. But it's still typed as a plain string, not the union: a live capture has been
   * observed returning statuses outside a "closed" enum elsewhere in Argus, so an out-of-vocabulary
   * value must fall through to the neutral branch rather than throw or render nothing.
   */
  status?: string | null
}

const props = withDefaults(defineProps<Props>(), { status: undefined })

const STATUS_DOT_CLASS: Record<string, string> = {
  active: 'bg-pending',
  ended: 'bg-accept',
  abandoned: 'bg-reject',
  unknown: 'bg-unknown',
}

const dotClass = computed(() => STATUS_DOT_CLASS[props.status ?? ''] ?? 'bg-unknown')
const label = computed(() => props.status ?? 'unknown')
</script>

<template>
  <span
    class="inline-flex items-center gap-1.5"
    :title="label"
    data-testid="status-dot"
  >
    <span
      class="size-2 shrink-0 rounded-full"
      :class="dotClass"
      aria-hidden="true"
    />
    <span class="text-muted-foreground text-xs capitalize">{{ label }}</span>
  </span>
</template>
