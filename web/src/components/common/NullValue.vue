<script setup lang="ts">
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

interface Props {
  /** Why this value is null (SPEC §6.1) — one of `src/lib/nullReasons.ts`'s constants, or a one-off string. */
  reason?: string
  label?: string
  /**
   * Drops the dotted-underline "hint text" styling while keeping the same
   * title/aria-label tooltip contract. Default styling is right for a
   * null value sitting among real text (the underline is the only cue
   * there's a reason to hover); it reads as a rendering glitch when the
   * value is a single bare glyph with nothing beside it — e.g. every row
   * of a table column that's *always* null (round-3 critic gap: subagent
   * tree cost column read as "clipped glyphs" at a glance).
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
  <!--
    reka-ui's TooltipContent renders into a portal that only exists in the
    DOM while the tooltip is open (hover/focus) — a mount test can't drive
    that without simulating pointer events. `title`/`aria-label` put the
    same reason directly on the trigger element so both an assistive
    technology and a plain `wrapper.text()`/`getByLabelText` assertion can
    read it without opening anything.
  -->
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
