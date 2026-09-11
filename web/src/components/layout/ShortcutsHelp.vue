<script setup lang="ts">
/**
 * The `?`-toggled help overlay: a static reference for the app-wide keyboard
 * shortcuts `useShortcuts.ts` wires in. Controlled open state (`v-model:open`) — `AppShell.vue` owns
 * when it's shown (the `?` handler), this component only renders the list. A plain `Dialog`
 * (centered, modal) rather than a `Sheet`: this is reference content to glance at and dismiss, not a
 * drawer with its own persistent context.
 */
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'

interface Props {
  open: boolean
}

defineProps<Props>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const SHORTCUTS = [
  { keys: '/', description: 'Focus the search field' },
  { keys: 'j', description: 'Move selection down the list' },
  { keys: 'k', description: 'Move selection up the list' },
  { keys: 'Esc', description: 'Close the open sheet, dialog, or this overlay' },
  { keys: '?', description: 'Toggle this shortcuts overlay' },
] as const
</script>

<template>
  <Dialog
    :open="open"
    @update:open="(value) => emit('update:open', value)"
  >
    <DialogContent data-testid="shortcuts-help">
      <DialogHeader>
        <DialogTitle>Keyboard shortcuts</DialogTitle>
      </DialogHeader>
      <dl class="flex flex-col gap-2 text-sm">
        <div
          v-for="shortcut in SHORTCUTS"
          :key="shortcut.keys"
          class="flex items-center justify-between gap-4"
        >
          <dt>
            <kbd class="bg-muted border-border rounded border px-1.5 py-0.5 font-mono text-xs">{{ shortcut.keys }}</kbd>
          </dt>
          <dd class="text-muted-foreground">
            {{ shortcut.description }}
          </dd>
        </div>
      </dl>
    </DialogContent>
  </Dialog>
</template>
