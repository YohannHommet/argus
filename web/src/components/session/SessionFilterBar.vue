<script setup lang="ts">
import { computed } from 'vue'

import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button } from '@/components/ui/button'
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

function onFromChange(event: Event): void {
  const value = (event.target as HTMLInputElement).value
  sessions.setFilters({ from: value || null })
}

function onToChange(event: Event): void {
  const value = (event.target as HTMLInputElement).value
  sessions.setFilters({ to: value || null })
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
    class="flex flex-wrap items-end gap-3"
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

    <div class="flex flex-col gap-1">
      <label
        for="session-from"
        class="text-muted-foreground text-xs"
      >From</label>
      <Input
        id="session-from"
        type="date"
        :model-value="sessions.filters.from ?? ''"
        data-testid="filter-from"
        @change="onFromChange"
      />
    </div>

    <div class="flex flex-col gap-1">
      <label
        for="session-to"
        class="text-muted-foreground text-xs"
      >To</label>
      <Input
        id="session-to"
        type="date"
        :model-value="sessions.filters.to ?? ''"
        data-testid="filter-to"
        @change="onToChange"
      />
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
