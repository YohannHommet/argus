<script setup lang="ts">
import NullValue from './NullValue.vue'
import { NOT_MEASURED } from '@/lib/nullReasons'

interface Props {
  /**
   * A vendor-supplied free-form value (query_source, decision_source,
   * tool_source, terminal_type, start_type, permission_mode — SPEC §4.4).
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
  <!--
    No switch/mapping over vendor vocabulary here, by design (SPEC §4.4,
    §6.1): whatever string the vendor sends renders verbatim. The empty
    string is the one deliberate exception — it's a real, meaningful value
    (the `by_query_source` "unattributed" bucket key, SPEC §4.3), not a
    missing one, but rendering '' produces literally nothing on screen, so
    it gets a visible label while the raw ('') value stays inspectable via
    `title`.
  -->
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
