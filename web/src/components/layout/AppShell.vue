<script setup lang="ts">
import {
  Activity,
  BarChart3,
  Gauge,
  ListTree,
  Wrench,
} from '@lucide/vue'

import ThemeToggle from '@/components/layout/ThemeToggle.vue'

/**
 * Five navigable destinations. SPEC §6.2 lists six top-level routes, but the
 * sixth (`/sessions/:id`) requires a session id and cannot be a fixed
 * sidebar link. The router at src/router/index.ts still registers all six
 * views + the `/` redirect + NotFoundView. SPEC §6.3 and P1-03 both ask for
 * six nav items, but no sixth navigable route exists; this gap is deviation
 * D-1, raised for the owner at the Phase-1 review, not an omission.
 */
const navItems = [
  { to: '/sessions', label: 'Sessions', icon: ListTree },
  { to: '/tools', label: 'Tools', icon: Wrench },
  { to: '/analytics', label: 'Analytics', icon: BarChart3 },
  { to: '/live', label: 'Live', icon: Activity },
  { to: '/data-quality', label: 'Data quality', icon: Gauge },
] as const
</script>

<template>
  <div class="flex min-h-screen">
    <aside class="flex w-56 shrink-0 flex-col border-r border-border bg-sidebar text-sidebar-foreground">
      <div class="px-4 py-4 text-lg font-semibold">
        Argus
      </div>
      <nav class="flex flex-1 flex-col gap-1 px-2">
        <router-link
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium text-sidebar-foreground/80 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
          active-class="bg-sidebar-accent text-sidebar-accent-foreground"
        >
          <component
            :is="item.icon"
            class="size-4"
            aria-hidden="true"
          />
          {{ item.label }}
        </router-link>
      </nav>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <header class="flex h-14 shrink-0 items-center justify-end border-b border-border px-4">
        <ThemeToggle />
      </header>

      <main class="flex-1 overflow-auto p-6">
        <slot />
      </main>
    </div>
  </div>
</template>
