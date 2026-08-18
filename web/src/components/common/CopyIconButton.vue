<script setup lang="ts">
/**
 * Icon-only clipboard affordance for a monospace id sitting inline in a row
 * or a `dl` (event_ref, tool_use_id, agent_id, session_id — SPEC's ids are
 * long UUID/ULID-shaped strings nobody can usefully read digit-by-digit,
 * only copy-paste elsewhere). `CopyBlock.vue` already exists but always
 * renders a labeled button ("Copy"/"Copied") — too wide for a single cell
 * or badge; this is the same clipboard contract, deliberately smaller.
 */
import { ref } from 'vue'
import { Check, Copy } from '@lucide/vue'

import { Button } from '@/components/ui/button'

interface Props {
  /** The exact text placed on the clipboard — never a re-formatted/truncated version of what is displayed. */
  text: string
  label?: string
}

const props = withDefaults(defineProps<Props>(), { label: 'Copy' })

const copied = ref(false)
const failed = ref(false)

/** See CopyBlock.vue: `navigator.clipboard` is absent in jsdom/insecure origins, so a failure is surfaced, not swallowed. */
async function copy() {
  failed.value = false
  try {
    await navigator.clipboard.writeText(props.text)
    copied.value = true
    window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    failed.value = true
  }
}
</script>

<template>
  <Button
    variant="ghost"
    size="icon-xs"
    class="shrink-0"
    :aria-label="failed ? `${label} failed` : copied ? `${label}: copied` : label"
    :title="failed ? 'Clipboard unavailable' : label"
    data-testid="copy-icon-button"
    @click.stop="copy"
  >
    <component
      :is="copied ? Check : Copy"
      :class="failed ? 'text-warn' : ''"
      aria-hidden="true"
    />
  </Button>
</template>
