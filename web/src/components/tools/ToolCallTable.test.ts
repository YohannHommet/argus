import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import ToolCallTable, { sortRows } from './ToolCallTable.vue'
import { listToolCalls200Default } from '@/test/fixtures'
import {
  toolCallExactRejected,
  toolCallHeuristic,
  toolCallHookOnly,
  toolCallLiveSeed42OtelOnly,
  toolCallsAllCorrelations,
} from '@/test/fixtures.extra'

async function mountTable(props: Record<string, unknown>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/sessions/:id', name: 'session-detail', component: { template: '<div/>' } }],
  })
  await router.push('/')
  await router.isReady()
  return mount(ToolCallTable, { props, global: { plugins: [router], stubs: { teleport: true } } })
}

describe('ToolCallTable', () => {
  it('renders one row per tool call, with the tool name', async () => {
    const wrapper = await mountTable({ rows: listToolCalls200Default.data })
    expect(wrapper.findAll('[data-testid="tool-call-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('Edit')
  })

  describe('AC: all four correlation values render, hook_only gets a distinct visual', () => {
    it('renders a row for each of exact, otel_only, heuristic, hook_only', async () => {
      const wrapper = await mountTable({ rows: toolCallsAllCorrelations })

      expect(wrapper.find('[data-testid="correlation-exact"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="correlation-otel_only"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="correlation-heuristic"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="correlation-hook_only"]').exists()).toBe(true)
    })

    it('hook_only renders as an outlined badge (not just a coloured dot) — the other three do not', async () => {
      const wrapper = await mountTable({ rows: toolCallsAllCorrelations })

      const hookOnlyCell = wrapper.get('[data-testid="correlation-hook_only"]')
      // Only hook_only (and the unknown-value fallback) render as an outlined Badge (the
      // `border-current` class the plain icon+text span never gets) — the other three render as
      // plain inline icon+text with no border at all.
      expect(hookOnlyCell.classes()).toContain('border-current')
      expect(hookOnlyCell.text()).toContain('Hook only')

      for (const value of ['exact', 'otel_only', 'heuristic']) {
        const cell = wrapper.get(`[data-testid="correlation-${value}"]`)
        expect(cell.classes()).not.toContain('border-current')
      }
    })

    it('an unrecognised correlation value still renders (fallback branch), not a crash', async () => {
      const unknownRow = { ...toolCallExactRejected, correlation: 'a_future_correlation' as never }
      const wrapper = await mountTable({ rows: [unknownRow] })
      expect(wrapper.text()).toContain('Unknown')
    })
  })

  describe('AC: wait_ms null renders — , never 0ms', () => {
    it('the live seed-42 row (wait_ms: null) renders an em dash with a reason, not "0ms"', async () => {
      const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly] })
      const cell = wrapper.get('[data-testid="cell-wait-ms"]')
      expect(cell.text()).toBe('—')
      expect(cell.text()).not.toContain('0ms')
      expect(cell.find('[title]').attributes('title')).toBeTruthy()
    })

    it('a non-null wait_ms renders a real duration', async () => {
      const wrapper = await mountTable({ rows: [toolCallHeuristic] })
      expect(wrapper.get('[data-testid="cell-wait-ms"]').text()).toBe('1.1s')
    })
  })

  it('duration_ms null also renders — , not 0ms (same honesty rule, same field shape)', async () => {
    const wrapper = await mountTable({ rows: [toolCallHookOnly] })
    expect(wrapper.get('[data-testid="cell-duration-ms"]').text()).toBe('—')
  })

  describe('unknown vendor values render as themselves (RawValue)', () => {
    it('an invented decision_source renders verbatim, not a fallback label', async () => {
      const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly] })
      expect(wrapper.text()).toContain('an_invented_decision_source')
    })

    it('a null tool_source renders the null-with-reason state, not "null" or blank', async () => {
      const wrapper = await mountTable({ rows: [toolCallHookOnly] })
      expect(wrapper.text()).not.toContain('null')
    })
  })

  describe('AC: sorting by wait_ms desc', () => {
    // GET /api/v1/tool-calls (and /sessions/{id}/tool-calls) have no `sort` query parameter at all
    // (verified against schema.d.ts's operations['listToolCalls'] / server/api/openapi.yaml) — unlike
    // GET /api/v1/sessions, which does. So "issues the right query params" for this endpoint means:
    // the click reorders the already-loaded rows client-side, and it does NOT fabricate a `sort`
    // param that would 400 against the real API. tools.spec.ts asserts the network side (no `sort`
    // key ever sent); this asserts the client-side reordering `sortRows`/the `sort-change` emit are
    // responsible for.
    const rows = [toolCallHeuristic, toolCallExactRejected, toolCallLiveSeed42OtelOnly]
    // wait_ms: heuristic=1100, exactRejected=4000, liveSeed42=null

    it('emits sort-change with the clicked key, and does not touch loadMore/retry', async () => {
      const wrapper = await mountTable({ rows })
      await wrapper.get('[data-testid="sort-wait_ms"]').trigger('click')
      expect(wrapper.emitted('sortChange')).toEqual([['wait_ms']])
      expect(wrapper.emitted('loadMore')).toBeUndefined()
      expect(wrapper.emitted('retry')).toBeUndefined()
    })

    it('given sort="wait_ms", displays rows ordered desc by wait_ms with nulls last (not treated as 0)', async () => {
      const wrapper = await mountTable({ rows, sort: 'wait_ms' })
      const toolNames = wrapper.findAll('[data-testid="cell-tool-name"]').map((el) => el.text())
      // exactRejected (4000) > heuristic (1100) > liveSeed42 (null, sorts last)
      expect(toolNames).toEqual(['Write', 'Bash', 'Read'])
    })

    it('exported sortRows: nulls sort after every non-null value', () => {
      const sorted = sortRows(rows, 'wait_ms')
      expect(sorted.map((r) => r.wait_ms)).toEqual([4000, 1100, null])
    })

    it('sortRows is a no-op (returns input order) when key is null/undefined', () => {
      expect(sortRows(rows, null)).toEqual(rows)
      expect(sortRows(rows, undefined)).toEqual(rows)
    })
  })

  it('loading renders skeletons, not the table or an empty state', async () => {
    const wrapper = await mountTable({ rows: [], loading: true })
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(false)
  })

  it('an error renders ErrorState and emits retry', async () => {
    const error = new Error('boom')
    const wrapper = await mountTable({ rows: [], error })
    const errorState = wrapper.get('[data-testid="error-state"]')
    await errorState.get('button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  it('zero rows (no error, not loading) renders EmptyState', async () => {
    const wrapper = await mountTable({ rows: [] })
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
  })

  describe('showSession', () => {
    it('shows a session column and link when showSession is true', async () => {
      const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly], showSession: true })
      expect(wrapper.find('[data-testid="tool-call-session-link"]').exists()).toBe(true)
    })

    it('hides the session column by default (session detail Tools tab usage)', async () => {
      const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly] })
      expect(wrapper.find('[data-testid="tool-call-session-link"]').exists()).toBe(false)
    })
  })

  it('clicking a row emits rowClick with that row', async () => {
    const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly] })
    await wrapper.get('[data-testid="tool-call-row"]').trigger('click')
    expect(wrapper.emitted('rowClick')?.[0]).toEqual([toolCallLiveSeed42OtelOnly])
  })

  it('clicking the session link does not also emit rowClick (click.stop)', async () => {
    const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly], showSession: true })
    await wrapper.get('[data-testid="tool-call-session-link"]').trigger('click')
    expect(wrapper.emitted('rowClick')).toBeUndefined()
  })

  it('hasMore renders a Load more button that emits loadMore', async () => {
    const wrapper = await mountTable({ rows: [toolCallLiveSeed42OtelOnly], hasMore: true })
    await wrapper.get('[data-testid="load-more"]').trigger('click')
    expect(wrapper.emitted('loadMore')).toHaveLength(1)
  })
})
