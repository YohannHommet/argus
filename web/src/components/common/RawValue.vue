<script setup lang="ts">
import NullValue from './NullValue.vue'
import { NOT_MEASURED } from '@/lib/nullReasons'

interface Props {
  /**
   * A vendor-supplied free-form value (query_source, decision_source,
   * tool_source, terminal_type, start_type, permission_mode).
   * Typed `string | null | undefined`, never a union: Argus must render a
   * value it has never seen before without a code change.
   */
  value?: string | null
  /** Dimension name, for the aria-label only (e.g. 'query_source'). Never used to branch rendering. */
  kind?: string
}

withDefaults(defineProps<Props>(), {
  value: undefined,
  kind: undefined,
})
</script>

<template>
  <!-- Empty string is a real, meaningful value (the unattributed bucket key), not missing — rendered as a visible label since '' alone would show nothing. -->
  <NullValue
    v-if="value === null || value === undefined"
    :reason="NOT_MEASURED"
  />
  <em
    v-else-if="value === ''"
    class="text-unknown font-mono italic"
    title="(empty string)"
    :aria-label="kind ? `${kind}: unattributed` : 'unattributed'"
  >unattributed</em>
  <span
    v-else
    class="text-unknown font-mono"
    :aria-label="kind ? `${kind}: ${value}` : value"
  >{{ value }}</span>
</template>
