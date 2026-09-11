<script setup lang="ts">
interface Props {
  /**
   * `status` is Argus's own stored column with a documented, closed 4-value vocabulary (SPEC §1.7:
   * active/ended/abandoned/unknown) — like `StatusDot.vue`, a `switch`/lookup on it by name is
   * legitimate here, unlike a vendor-supplied string (SPEC §0). Only `active` means "this session's
   * row can still change under the viewer without a refresh" (PLAN.md P5-06 / SPEC §6.2's "live badge
   * for active sessions"), so every other value — including one outside the documented vocabulary —
   * renders nothing rather than guessing.
   */
  status?: string | null
}

const props = withDefaults(defineProps<Props>(), { status: undefined })
</script>

<template>
  <span
    v-if="props.status === 'active'"
    class="bg-primary inline-block size-1.5 shrink-0 animate-pulse rounded-full"
    data-testid="live-dot"
    role="status"
    aria-label="Live"
  />
</template>
