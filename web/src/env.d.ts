/// <reference types="vite/client" />

// Vite's client types declare the module shapes for the asset imports the app
// makes (`import './assets/theme.css'` in main.ts, `?url`/`?raw` suffixes,
// import.meta.env). Without this reference, `noUncheckedSideEffectImports`
// fails the CSS side-effect import in main.ts — which nothing noticed while
// `pnpm type-check` was checking zero files.
