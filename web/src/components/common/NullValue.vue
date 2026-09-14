<script setup lang="ts">
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

interface Props {
  /** Why this value is null — one of `src/lib/nullReasons.ts`'s constants, or a one-off string. */
  reason?: string
  label?: string
  /**
   * Drops the dotted-underline "hint text" styling while keeping the same title/aria-label tooltip
   * contract. Default styling suits a null value sitting among real text; it reads as a rendering
   * glitch when the value is a single bare glyph with nothing beside it, e.g. a column that's always null.
   */
  plain?: boolean
}

withDefaults(defineProps<Props>(), {
  reason: undefined,
  label: '—',
  plain: false,
})
</script>

<template>
  <!-- title/aria-label duplicate the tooltip's reason since TooltipContent only mounts into the DOM while open, which a plain assertion can't easily trigger. -->
  <span
    v-if="!reason"
    class="text-muted-foreground"
  >{{ label }}</span>
  <TooltipProvider v-else>
    <Tooltip>
      <TooltipTrigger as-child>
        <span
          class="text-muted-foreground cursor-help"
          :class="plain ? '' : 'underline decoration-dotted underline-offset-2'"
          :title="reason"
          :aria-label="reason"
        >
          {{ label }}
        </span>
      </TooltipTrigger>
      <TooltipContent>{{ reason }}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
