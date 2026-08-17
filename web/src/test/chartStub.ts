// Test-only helpers shared by the P4-07 analytics chart specs
// (TimeSeriesChart/BreakdownChart/DecisionMatrix). Not part of the app
// bundle — canvas never renders under jsdom, so every chart mount test
// stubs `vue-echarts`'s component and asserts on the `option` object it
// received (per the ticket's AC), plus jsdom's missing `ResizeObserver`.
import { defineComponent, h } from 'vue'
import { vi } from 'vitest'

/**
 * The key a mount test must use in `global.stubs` to replace
 * `vue-echarts`'s component: Vue Test Utils matches a `<script setup>`
 * child by the component definition's own `name` (vue-echarts's dist
 * hardcodes `name: "Echarts"`), not by the local import alias
 * (`VChart`) the SFC's template happens to use for it — verified against
 * the installed `vue-echarts@8.1.0` build, not assumed.
 */
export const VCHART_STUB_KEY = 'Echarts'

/**
 * A stand-in for `vue-echarts`'s default export: renders nothing
 * interesting, but exposes a `resize` spy the way the real component
 * exposes `EChartsType["resize"]` (vue-echarts's `PublicMethods`), so a
 * test can assert `useChartResize`'s ResizeObserver callback actually
 * calls through rather than merely existing.
 */
export function makeVChartStub() {
  const resize = vi.fn()
  const Stub = defineComponent({
    name: 'VChartStub',
    props: {
      option: { type: Object, default: () => ({}) },
      autoresize: { type: [Boolean, Object], default: false },
    },
    setup(_props, { expose }) {
      expose({ resize })
      return () => h('div', { 'data-testid': 'vchart-stub' })
    },
  })
  return { Stub, resize }
}

/**
 * jsdom has no `ResizeObserver`. Installs a fake that records the last
 * registered callback so a test can invoke it directly to simulate a
 * container resize, and restores whatever was there before (nothing, in
 * every real run) on `restore()`.
 */
export function stubResizeObserver() {
  let lastCallback: ResizeObserverCallback | null = null

  class FakeResizeObserver implements ResizeObserver {
    constructor(callback: ResizeObserverCallback) {
      lastCallback = callback
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  }

  const original = globalThis.ResizeObserver
  globalThis.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver

  return {
    trigger: () => lastCallback?.([], {} as ResizeObserver),
    restore: () => {
      globalThis.ResizeObserver = original
    },
  }
}
