<script setup lang="ts">
import { ref } from 'vue'
import {
  Activity,
  BarChart3,
  Gauge,
  ListTree,
  Wrench,
} from '@lucide/vue'

import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import ShortcutsHelp from '@/components/layout/ShortcutsHelp.vue'
import { useShortcuts } from '@/composables/useShortcuts'

/**
 * Five navigable destinations. The sixth top-level route (`/sessions/:id`)
 * requires a session id and cannot be a fixed sidebar link, so it has no
 * nav entry — a deliberate gap, not an omission. The router at
 * src/router/index.ts still registers all six views + the `/` redirect +
 * NotFoundView.
 */
const navItems = [
  { to: '/sessions', label: 'Sessions', icon: ListTree },
  { to: '/tools', label: 'Tools', icon: Wrench },
  { to: '/analytics', label: 'Analytics', icon: BarChart3 },
  { to: '/live', label: 'Live', icon: Activity },
  { to: '/data-quality', label: 'Data quality', icon: Gauge },
] as const

/**
 * `?` toggles the app-wide shortcuts help overlay, mounted here (not per-view) since
 * `AppShell.vue` wraps every route for the lifetime of the app (`App.vue`). `Esc` closing it is the
 * one case `useShortcuts.ts`'s own `onEscape` needs to actually do something with, rather than just
 * relying on the `Dialog`'s native Escape handling, so a stray Esc elsewhere in the app never has to
 * guess whether this overlay happens to be open.
 */
const shortcutsHelpOpen = ref(false)

useShortcuts({
  onToggleHelp: () => {
    shortcutsHelpOpen.value = !shortcutsHelpOpen.value
  },
  onEscape: () => {
    if (shortcutsHelpOpen.value) shortcutsHelpOpen.value = false
  },
})
</script>

<template>
  <div class="flex min-h-screen">
    <aside class="flex w-56 shrink-0 flex-col border-r border-border bg-sidebar text-sidebar-foreground">
      <div class="px-4 py-4 text-lg font-semibold">
        Argus
      </div>
      <nav
        class="flex flex-1 flex-col gap-1 px-2"
        aria-label="Primary"
      >
        <router-link
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="focus-visible:ring-ring flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium text-sidebar-foreground/80 outline-none hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2"
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
      <header class="flex h-14 shrink-0 items-center justify-end gap-2 border-b border-border px-4">
        <button
          type="button"
          class="text-muted-foreground hover:text-foreground focus-visible:ring-ring rounded-md px-2 py-1 text-xs outline-none focus-visible:ring-2"
          data-testid="shortcuts-help-trigger"
          aria-label="Show keyboard shortcuts"
          @click="shortcutsHelpOpen = true"
        >
          <kbd class="bg-muted border-border rounded border px-1 py-0.5 font-mono">?</kbd>
          Shortcuts
        </button>
        <ThemeToggle />
      </header>

      <main class="flex-1 overflow-auto p-6">
        <slot />
      </main>
    </div>

    <ShortcutsHelp v-model:open="shortcutsHelpOpen" />
  </div>
</template>
