<script setup lang="ts">
import AppShell from '@/components/layout/AppShell.vue'
import { Toaster } from '@/components/ui/sonner'
import { useUiStore } from '@/stores/ui'

// Boot the theme store so it takes ownership of <html>'s class after the
// anti-flash inline script in index.html has done its job pre-paint.
useUiStore()
</script>

<template>
  <AppShell>
    <router-view />
  </AppShell>
  <!--
    Mounted once, app-wide — src/lib/toast.ts's notifyApiFailure() is the
    only way anything should push a toast, so there is exactly one
    Toaster to receive it. Never a substitute for a view's inline
    ErrorState (see toast.ts's doc comment): every view's primary fetch
    failure still renders in place.
  -->
  <Toaster />
</template>
