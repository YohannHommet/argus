/**
 * One column-per-cell grid, shared verbatim by `SessionRow.vue` (its 'grid' layout mode) and
 * `SessionTable.vue`'s virtualized header, so the two can never drift out of alignment with each
 * other. 12 columns: status, project, vendor, started, last event, duration, turns, events, tools,
 * reject rate, tokens, cost. Lives in its own module rather than as an export from SessionRow.vue's
 * `<script setup>` because `<script setup>` cannot itself export a plain binding (Vue SFC compiler
 * constraint) — only `defineExpose`d template refs, which this constant is not.
 */
export const SESSION_ROW_GRID_COLS =
  'grid-cols-[76px_minmax(110px,1.3fr)_90px_92px_92px_84px_60px_70px_64px_84px_84px_96px]'

/**
 * Above this many rows, `SessionTable.vue` switches its body from a plain `<table>` to
 * `@vueuse/core`'s `useVirtualList` (a row *count* threshold, not a pixel/viewport heuristic —
 * deliberately simple, and exactly what the AC asks for). Lives here rather than as a `<script
 * setup>` export for the same SFC-compiler reason as `SESSION_ROW_GRID_COLS` above.
 */
export const VIRTUALIZATION_THRESHOLD = 200
