<script setup lang="ts">
import { Moon, Sun } from '@lucide/vue'
import { computed } from 'vue'

import { Button } from '@/components/ui/button'
import { useUiStore } from '@/stores/ui'

const ui = useUiStore()
const isDark = computed(() => ui.theme === 'dark')
</script>

<template>
  <Button
    variant="ghost"
    size="icon"
    :aria-label="isDark ? 'Switch to light theme' : 'Switch to dark theme'"
    @click="ui.toggle()"
  >
    <Moon
      v-if="isDark"
      class="size-4"
      aria-hidden="true"
    />
    <Sun
      v-else
      class="size-4"
      aria-hidden="true"
    />
  </Button>
</template>
