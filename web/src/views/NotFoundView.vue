<script setup lang="ts">
/**
 * The router's catch-all (`src/router/index.ts`'s `/:pathMatch(.*)*`).
 * Says what wasn't found (the path actually requested, not a generic
 * "page") and gives a way back — a dead end with no link out is worse
 * than the blank table this whole ticket exists to replace elsewhere.
 */
import { useRoute } from 'vue-router'

const route = useRoute()
</script>

<template>
  <section
    class="flex flex-col items-center gap-3 py-24 text-center"
    data-testid="not-found-view"
  >
    <h1 class="text-2xl font-semibold">
      Page not found
    </h1>
    <p class="text-muted-foreground max-w-prose text-sm">
      There's no route at <code
        class="font-mono"
        data-testid="not-found-path"
      >{{ route.fullPath }}</code>.
    </p>
    <RouterLink
      to="/sessions"
      class="text-primary text-sm hover:underline"
      data-testid="not-found-home-link"
    >
      Back to sessions
    </RouterLink>
  </section>
</template>
