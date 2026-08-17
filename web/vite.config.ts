import { fileURLToPath, URL } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
      '/ingest': 'http://localhost:8080',
    },
  },
  test: {
    environment: 'jsdom',
    globals: false,
    setupFiles: ['./src/test-setup.ts'],
    // Node 25's global `localStorage` shadows jsdom's, and test-setup.ts's
    // global `afterEach(() => localStorage.clear())` (plus every store
    // test that reads/writes it) needs jsdom's implementation. This used
    // to be `unit`'s `cross-env NODE_OPTIONS=--no-experimental-webstorage`,
    // which meant any invocation that bypassed the npm script (bare
    // `vitest run`, watch mode, an editor's test runner, a single-file
    // run) failed the whole suite. `execArgv` is passed to every worker
    // process regardless of how vitest itself was invoked (default pool
    // is 'forks' — `test.poolOptions` was removed in Vitest 4, this
    // replaces it as a top-level option), so the flag now applies
    // unconditionally.
    execArgv: ['--no-experimental-webstorage'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      // What the gate is allowed to measure. Both entries below remove code
      // that was never meant to be under an Argus coverage bar; neither
      // relaxes the bar itself.
      //
      // - `src/components/ui/**` is shadcn-vue's registry output, copied in
      //   verbatim (SPEC §6.1 "copied into web/src/components/ui/, owned
      //   code"). We own it in the sense that we may edit it, but we do not
      //   write tests for reka-ui primitives — we test our own components
      //   that use them. Measuring it meant ~20 files at 0% dragging the
      //   global number purely as a function of how many primitives the
      //   phase happened to install.
      // - test scaffolding (`src/test/**` fixtures, `*.test.ts`,
      //   `__tests__/**`) is the measuring instrument, not the subject.
      exclude: [
        'src/components/ui/**',
        'src/test/**',
        'src/**/*.test.ts',
        'src/**/__tests__/**',
      ],
      // Gate, not just report: `pnpm unit --coverage` collected numbers
      // but nothing failed the build if they regressed (m23). Set at the
      // measured baseline on this commit (statements 88.88%, branches
      // 85.36%, functions 75%, lines 88.23%) so today's suite is green
      // and any drop below it is red.
      thresholds: {
        statements: 88,
        branches: 85,
        functions: 75,
        lines: 88,
      },
    },
  },
})
