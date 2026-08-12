<script setup lang="ts">
import { Moon, Sun } from '@lucide/vue'
import { computed } from 'vue'

import { Switch } from '@/components/ui/switch'
import { useUiStore } from '@/stores/ui'

const ui = useUiStore()
const isDark = computed(() => ui.theme === 'dark')
</script>

<template>
  <label class="flex items-center gap-2 text-sm text-muted-foreground">
    <Sun
      class="size-4"
      aria-hidden="true"
    />
    <Switch
      :model-value="isDark"
      aria-label="Toggle dark mode"
      @update:model-value="ui.toggle()"
    />
    <Moon
      class="size-4"
      aria-hidden="true"
    />
  </label>
</template>
