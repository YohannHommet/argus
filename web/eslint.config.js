import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    name: 'app/files-to-lint',
    files: ['**/*.{ts,mts,tsx,vue}'],
  },
  {
    name: 'app/files-to-ignore',
    ignores: ['**/dist/**', '**/coverage/**', '**/node_modules/**'],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/recommended'],
  {
    name: 'app/vue-typescript-parser',
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
      },
    },
  },
  {
    name: 'app/languageOptions',
    languageOptions: {
      globals: {
        ...globals.browser,
      },
    },
  },
  {
    name: 'app/rules',
    rules: {
      'vue/multi-word-component-names': 'off',
    },
  },
  {
    // shadcn-vue's generated primitives (owned code, copied verbatim into
    // src/components/ui/) type optional props via a TS interface rather
    // than Vue's runtime prop-default system; vue/require-default-prop
    // doesn't understand TS-optional props and flags every one of them.
    // vue/max-attributes-per-line is off for the same reason: the registry
    // ships these files pre-formatted, and reformatting them by hand turns
    // every future `shadcn-vue add --overwrite` into a conflict.
    // Argus-authored components are held to both rules.
    name: 'app/shadcn-ui-overrides',
    files: ['src/components/ui/**/*.vue'],
    rules: {
      'vue/require-default-prop': 'off',
      'vue/max-attributes-per-line': 'off',
    },
  },
)
