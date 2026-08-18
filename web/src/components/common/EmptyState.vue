<script setup lang="ts">
interface Props {
  /** One line saying what is not here. */
  title: string
  /** Optional second line saying what would put something here. */
  description?: string
}

withDefaults(defineProps<Props>(), { description: undefined })
</script>

<template>
  <!--
    An empty state is a measured fact ("this query matched nothing"), not a
    failure, so it is styled neutrally rather than as a warning. The default
    slot is where a caller puts the action that would fix it — clearing a
    filter, or the SetupCard on a genuinely empty database.
  -->
  <div
    class="border-border text-muted-foreground flex flex-col items-center gap-2 rounded-lg border border-dashed px-6 py-12 text-center"
    data-testid="empty-state"
  >
    <p class="text-foreground text-sm font-medium">
      {{ title }}
    </p>
    <p
      v-if="description"
      class="max-w-prose text-sm"
    >
      {{ description }}
    </p>
    <div
      v-if="$slots.default"
      class="mt-2"
    >
      <slot />
    </div>
  </div>
</template>
