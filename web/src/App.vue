<script setup lang="ts">
import AppShell from '@/components/layout/AppShell.vue'
import { Toaster } from '@/components/ui/sonner'
import { useUiStore } from '@/stores/ui'

// Boot the theme store so it takes ownership of <html>'s class after index.html's anti-flash inline script has done its job pre-paint.
useUiStore()
</script>

<template>
  <AppShell>
    <router-view />
  </AppShell>
  <!-- Mounted once, app-wide — toast.ts's notifyApiFailure() is the only way anything pushes a toast; never a substitute for a view's inline ErrorState. -->
  <Toaster />
</template>
