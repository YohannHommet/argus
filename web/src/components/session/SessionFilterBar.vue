<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Calendar as CalendarIcon } from '@lucide/vue'

import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useMetaStore } from '@/stores/meta'
import { useSessionsStore, SESSION_STATUSES } from '@/stores/sessions'
import type { SessionStatus } from '@/stores/sessions'

/**
 * Owns none of the filter state itself — every control here reads `sessionsStore.filters` and
 * writes back through `setFilters`/`setSearch`, which is what keeps the URL and the debounced
 * refetch honest (see sessions.ts's own doc comment). This component's whole job is translating
 * user input into that one call.
 */
const meta = useMetaStore()
const sessions = useSessionsStore()

const STATUS_OPTIONS = SESSION_STATUSES

function toStringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : []
}

function toStatusArray(value: unknown): SessionStatus[] {
  return toStringArray(value).filter((v): v is SessionStatus => (SESSION_STATUSES as readonly string[]).includes(v))
}

const search = computed({
  get: () => sessions.filters.q,
  set: (value: string | number) => sessions.setSearch(String(value)),
})

/**
 * round-4 UI gap: bare native `<input type="date">` pairs read as off-palette UA chrome next to
 * five custom-styled selects. Replaced with a segmented relative-range control (matching the
 * reference bar's 24H/7D/1M pill group) plus a "Custom…" popover — but the wire contract is
 * unchanged: every preset here just writes one of the API's own relative shorthands (SPEC §4.1,
 * already exercised by sessions.spec.ts's `from: '-7d'` round-trip) into `filters.from`, so the
 * `from`/`to` URL params and their parse/serialise round-trip in stores/sessions.ts need no changes
 * at all — only this component's presentation of them does.
 */
const RANGE_PRESETS = [
  { key: '24h', label: '24h', from: '-24h' },
  { key: '7d', label: '7d', from: '-7d' },
  { key: '30d', label: '30d', from: '-30d' },
  { key: 'all', label: 'All', from: null },
] as const

/** The preset whose shorthand matches the live filters exactly, or `undefined` when the current
 * from/to pair is a custom range no preset produces (including one typed by hand into the URL). */
const activePresetKey = computed<string | undefined>(() => {
  if (sessions.filters.to !== null) return undefined
  return RANGE_PRESETS.find((p) => p.from === sessions.filters.from)?.key
})

const isCustomActive = computed(() => activePresetKey.value === undefined && (sessions.filters.from !== null || sessions.filters.to !== null))

function onPresetChange(value: unknown): void {
  const preset = RANGE_PRESETS.find((p) => p.key === value)
  if (!preset) return
  customOpen.value = false
  sessions.setFilters({ from: preset.from, to: null })
}

const customOpen = ref(false)
const customFromDraft = ref('')
const customToDraft = ref('')

// Seed the draft fields from the live filters every time the popover opens, rather than binding
// them directly — so opening the popover to look, then dismissing it (Escape/outside click)
// without hitting "Apply", never mutates the URL/refetch.
watch(customOpen, (open) => {
  if (!open) return
  customFromDraft.value = sessions.filters.from ?? ''
  customToDraft.value = sessions.filters.to ?? ''
})

const customLabel = computed(() => {
  if (!isCustomActive.value) return 'Custom…'
  const from = sessions.filters.from ?? '…'
  const to = sessions.filters.to ?? 'now'
  return `${from} → ${to}`
})

function onApplyCustom(): void {
  sessions.setFilters({ from: customFromDraft.value.trim() || null, to: customToDraft.value.trim() || null })
  customOpen.value = false
}

function onClearCustom(): void {
  customFromDraft.value = ''
  customToDraft.value = ''
  sessions.setFilters({ from: null, to: null })
  customOpen.value = false
}

function hasActiveFilters(): boolean {
  const f = sessions.filters
  return (
    f.project.length > 0 ||
    f.vendor.length > 0 ||
    f.model.length > 0 ||
    f.status.length > 0 ||
    f.tool.length > 0 ||
    f.decisionSource.length > 0 ||
    f.from !== null ||
    f.to !== null ||
    f.q !== ''
  )
}
</script>

<template>
  <div
    class="border-border bg-muted/30 flex flex-wrap items-end gap-x-4 gap-y-3 rounded-xl border p-3"
    data-testid="session-filter-bar"
  >
    <div class="flex min-w-48 flex-col gap-1">
      <label
        for="session-search"
        class="text-muted-foreground text-xs"
      >Search</label>
      <Input
        id="session-search"
        v-model="search"
        type="search"
        placeholder="id, project, cwd…"
        data-testid="filter-search"
      />
    </div>

    <div class="flex min-w-36 flex-col gap-1">
      <label class="text-muted-foreground text-xs">Project</label>
      <Select
        multiple
        :model-value="sessions.filters.project"
        data-testid="filter-project"
        @update:model-value="(v) => sessions.setFilters({ project: toStringArray(v) })"
      >
        <SelectTrigger class="w-full">
          <SelectValue placeholder="All projects" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="project in meta.projects"
            :key="project"
            :value="project"
          >
            {{ project }}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>

    <div class="flex min-w-36 flex-col gap-1">
      <label class="text-muted-foreground text-xs">Vendor</label>
      <Select
        multiple
        :model-value="sessions.filters.vendor"
        data-testid="filter-vendor"
        @update:model-value="(v) => sessions.setFilters({ vendor: toStringArray(v) })"
      >
        <SelectTrigger class="w-full">
          <SelectValue placeholder="All vendors" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="vendor in meta.vendors"
            :key="vendor"
            :value="vendor"
          >
            {{ vendor }}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>

    <div class="flex min-w-36 flex-col gap-1">
      <label class="text-muted-foreground text-xs">Model</label>
      <Select
        multiple
        :model-value="sessions.filters.model"
        data-testid="filter-model"
        @update:model-value="(v) => sessions.setFilters({ model: toStringArray(v) })"
      >
        <SelectTrigger class="w-full">
          <SelectValue placeholder="All models" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="model in meta.models"
            :key="model"
            :value="model"
          >
            {{ model }}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>

    <div class="flex min-w-36 flex-col gap-1">
      <label class="text-muted-foreground text-xs">Status</label>
      <Select
        multiple
        :model-value="sessions.filters.status"
        data-testid="filter-status"
        @update:model-value="(v) => sessions.setFilters({ status: toStatusArray(v) })"
      >
        <SelectTrigger class="w-full">
          <SelectValue placeholder="All statuses" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="status in STATUS_OPTIONS"
            :key="status"
            :value="status"
          >
            {{ status }}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>

    <!--
      Segmented relative-range presets + a "Custom…" popover, replacing the pair of bare
      `<input type="date">` fields (round-4 UI gap). Grouped as one flex child so it wraps atomically
      when the toolbar runs out of width, same reasoning as every other grouped control here.
    -->
    <div class="flex flex-col gap-1">
      <label class="text-muted-foreground text-xs">Time range</label>
      <div class="flex items-center gap-1.5">
        <Tabs
          :model-value="activePresetKey"
          data-testid="filter-range-presets"
          @update:model-value="onPresetChange"
        >
          <TabsList>
            <TabsTrigger
              v-for="preset in RANGE_PRESETS"
              :key="preset.key"
              :value="preset.key"
              :data-testid="`filter-range-${preset.key}`"
            >
              {{ preset.label }}
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <Popover v-model:open="customOpen">
          <PopoverTrigger as-child>
            <Button
              variant="outline"
              size="sm"
              class="gap-1.5 font-normal"
              :class="isCustomActive ? 'border-ring bg-background text-foreground shadow-sm' : 'text-muted-foreground'"
              data-testid="filter-range-custom"
            >
              <CalendarIcon class="size-3.5" />
              {{ customLabel }}
            </Button>
          </PopoverTrigger>
          <PopoverContent
            class="w-64"
            data-testid="filter-range-custom-popover"
          >
            <div class="flex flex-col gap-1">
              <label
                for="session-from"
                class="text-muted-foreground text-xs"
              >From</label>
              <div class="relative">
                <CalendarIcon class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
                <Input
                  id="session-from"
                  v-model="customFromDraft"
                  type="text"
                  inputmode="numeric"
                  placeholder="2026-08-01"
                  class="pl-8"
                  data-testid="filter-from"
                  @keydown.enter="onApplyCustom"
                />
              </div>
            </div>

            <div class="flex flex-col gap-1">
              <label
                for="session-to"
                class="text-muted-foreground text-xs"
              >To</label>
              <div class="relative">
                <CalendarIcon class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
                <Input
                  id="session-to"
                  v-model="customToDraft"
                  type="text"
                  inputmode="numeric"
                  placeholder="2026-08-17"
                  class="pl-8"
                  data-testid="filter-to"
                  @keydown.enter="onApplyCustom"
                />
              </div>
            </div>

            <div class="flex justify-between pt-1">
              <Button
                variant="ghost"
                size="sm"
                data-testid="filter-range-clear"
                @click="onClearCustom"
              >
                Clear
              </Button>
              <Button
                size="sm"
                data-testid="filter-range-apply"
                @click="onApplyCustom"
              >
                Apply
              </Button>
            </div>
          </PopoverContent>
        </Popover>
      </div>
    </div>

    <Button
      v-if="hasActiveFilters()"
      variant="ghost"
      size="sm"
      data-testid="filter-clear"
      @click="sessions.clearFilters()"
    >
      Clear filters
    </Button>
  </div>
</template>
