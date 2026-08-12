<script setup lang="ts">
import AppShell from '@/components/layout/AppShell.vue'
import { useUiStore } from '@/stores/ui'

// Boot the theme store so it takes ownership of <html>'s class after the
// anti-flash inline script in index.html has done its job pre-paint.
useUiStore()
</script>

<template>
  <AppShell>
    <router-view />
  </AppShell>
</template>
