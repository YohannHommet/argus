<script setup lang="ts">
/**
 * SPEC §6.2: ingest health, the unmapped-`event_name` inspector, and the
 * hook-latency panel — "how a new Claude Code release that adds an event
 * becomes visible in minutes". This view is a thin composition root: it
 * owns no formatting/rendering logic of its own, just wiring `useMetaStore`
 * (already fetched app-wide) and `useQualityStore` (this ticket's two
 * `/quality/*` endpoints) into `QualityTiles`/`UnknownKindTable`/
 * `HookLatencyPanel`.
 */
import { computed } from 'vue'

import { useCaptureReady } from '@/composables/useCaptureReady'
import { useMetaStore } from '@/stores/meta'
import { useQualityStore } from '@/stores/quality'
import QualityTiles from '@/components/quality/QualityTiles.vue'
import UnknownKindTable from '@/components/quality/UnknownKindTable.vue'
import HookLatencyPanel from '@/components/quality/HookLatencyPanel.vue'

const metaStore = useMetaStore()
const qualityStore = useQualityStore()

void metaStore.load()

/**
 * "Ready" = all three independent sources this view reads have settled
 * one way or another — meta's own fetch, and quality's two `/quality/*`
 * fetches (`qualityStore.settled` already requires both). Real data, an
 * empty `rows: []`, and an error banner are all legitimate first paints;
 * a still-loading skeleton on any of the three is not.
 */
useCaptureReady(
  () => !metaStore.loading && (metaStore.meta !== null || metaStore.error !== null) && qualityStore.settled,
)

const retentionDays = computed(() => metaStore.meta?.retention_days ?? null)

function retryUnknownKinds(): void {
  void qualityStore.refetchUnknownKinds()
}

function retryHookLatency(): void {
  void qualityStore.refetchHookLatency()
}
</script>

<template>
  <section
    class="flex flex-col gap-6"
    data-testid="data-quality-view"
  >
    <header>
      <h1 class="text-2xl font-semibold">
        Data quality
      </h1>
      <p class="text-muted-foreground mt-2 text-sm">
        Ingest health for this Argus deployment — how much of what Claude Code sends is landing
        correctly, and the earliest signal that a new release changed its event shape.
      </p>
    </header>

    <QualityTiles
      :unknown-events-total="qualityStore.unknownEventsTotal"
      :unknown-events-loading="qualityStore.unknownKindsLoading"
      :unknown-events-error="qualityStore.unknownKindsError"
      :retention-days="retentionDays"
      @retry="retryUnknownKinds"
    />

    <section class="flex flex-col gap-2">
      <h2 class="text-lg font-medium">
        Unmapped event names
      </h2>
      <UnknownKindTable
        :rows="qualityStore.unknownKindRows"
        :loading="qualityStore.unknownKindsLoading"
        :error="qualityStore.unknownKindsError"
        @retry="retryUnknownKinds"
      />
    </section>

    <section class="flex flex-col gap-2">
      <h2 class="text-lg font-medium">
        Hook latency
      </h2>
      <HookLatencyPanel
        :rows="qualityStore.hookLatencyRows"
        :loading="qualityStore.hookLatencyLoading"
        :error="qualityStore.hookLatencyError"
        :hooks-seen="metaStore.hooksSeen"
        @retry="retryHookLatency"
      />
    </section>
  </section>
</template>
