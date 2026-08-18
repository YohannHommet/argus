<script setup lang="ts">
import { ref } from 'vue'
import { Check, Copy } from '@lucide/vue'

import { Button } from '@/components/ui/button'

interface Props {
  /** The exact text placed on the clipboard — never a re-formatted version of what is displayed. */
  text: string
  label?: string
}

const props = withDefaults(defineProps<Props>(), { label: 'Copy' })

const copied = ref(false)
const failed = ref(false)

/**
 * `navigator.clipboard` is absent in jsdom and unavailable on a non-secure
 * origin, so a failure is surfaced in the UI rather than swallowed — a copy
 * button that silently does nothing is worse than one that admits it could
 * not copy.
 */
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
  <div class="flex items-center gap-2">
    <Button
      variant="outline"
      size="sm"
      :aria-label="props.label"
      @click="copy"
    >
      <component
        :is="copied ? Check : Copy"
        class="size-3.5"
        aria-hidden="true"
      />
      {{ copied ? 'Copied' : props.label }}
    </Button>
    <span
      v-if="failed"
      class="text-warn text-xs"
    >Clipboard unavailable</span>
  </div>
</template>
