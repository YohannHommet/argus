#!/usr/bin/env node
// Generates web/src/test/fixtures.ts from the OpenAPI examples in
// ../server/api/openapi.yaml (see PLAN.md P4-01), so component/store tests
// consume real, spec-committed response shapes instead of hand-rolling
// them. Deterministic: walks paths/methods/status codes in the order
// they're written in the YAML (`yaml`'s parser preserves document order,
// and object keys are emitted in that same order), and there is exactly
// one operationId/status/example-name combination per export, so re-runs
// produce byte-identical output — a future CI drift check can diff it.

import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { parse } from 'yaml'

const __dirname = dirname(fileURLToPath(import.meta.url))
const OPENAPI_PATH = resolve(__dirname, '../../server/api/openapi.yaml')
const OUT_PATH = resolve(__dirname, '../src/test/fixtures.ts')

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'options', 'head', 'trace']

function pascalCase(name) {
  return name
    .replace(/[^a-zA-Z0-9]+(.)/g, (_, c) => c.toUpperCase())
    .replace(/^[a-z]/, (c) => c.toUpperCase())
}

/** Resolves a `{ $ref: '#/components/x/Y' }` pointer against the parsed document. */
function resolveRef(doc, ref) {
  const segments = ref.replace(/^#\//, '').split('/')
  let node = doc
  for (const segment of segments) {
    node = node[segment]
  }
  return node
}

/** Resolves an example object that may itself be a `$ref` to `components.examples.*`. */
function resolveExampleValue(doc, example) {
  if (example && typeof example === 'object' && '$ref' in example) {
    const resolved = resolveRef(doc, example.$ref)
    return resolved.value
  }
  return example.value
}

/** The bare schema name if `schema` is a direct `$ref` to `#/components/schemas/X`, else null. */
function schemaRefName(schema) {
  if (schema && typeof schema === 'object' && typeof schema.$ref === 'string') {
    const match = schema.$ref.match(/^#\/components\/schemas\/(.+)$/)
    if (match) return match[1]
  }
  return null
}

/** Serializes a JS value as a TS object/array literal, preserving key order. */
function serialize(value, indent = 0) {
  const pad = '  '.repeat(indent)
  const childPad = '  '.repeat(indent + 1)

  if (value === null) return 'null'
  if (value === undefined) return 'undefined'
  if (typeof value === 'string') return JSON.stringify(value)
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)

  if (Array.isArray(value)) {
    if (value.length === 0) return '[]'
    const items = value.map((item) => `${childPad}${serialize(item, indent + 1)}`)
    return `[\n${items.join(',\n')},\n${pad}]`
  }

  if (typeof value === 'object') {
    const keys = Object.keys(value)
    if (keys.length === 0) return '{}'
    const isValidIdentifier = (k) => /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(k)
    const entries = keys.map((key) => {
      const propKey = isValidIdentifier(key) ? key : JSON.stringify(key)
      return `${childPad}${propKey}: ${serialize(value[key], indent + 1)}`
    })
    return `{\n${entries.join(',\n')},\n${pad}}`
  }

  throw new Error(`cannot serialize value of type ${typeof value}`)
}

function main() {
  const source = readFileSync(OPENAPI_PATH, 'utf8')
  const doc = parse(source)

  /** @type {{ exportName: string, typeExpr: string, value: unknown }[]} */
  const fixtures = []
  const seenNames = new Set()

  for (const [pathKey, pathItem] of Object.entries(doc.paths ?? {})) {
    for (const method of HTTP_METHODS) {
      const operation = pathItem[method]
      if (!operation || !operation.operationId) continue
      const { operationId, responses } = operation
      if (!responses) continue

      for (const [status, response] of Object.entries(responses)) {
        // $ref'd shared responses (BadRequest, Unauthorized, ...) carry no
        // per-operation example worth generating a fixture for.
        if (!response || typeof response !== 'object' || '$ref' in response) continue
        const content = response.content
        if (!content) continue

        for (const contentType of ['application/json', 'application/problem+json']) {
          const media = content[contentType]
          if (!media || !media.examples) continue

          for (const [exampleName, example] of Object.entries(media.examples)) {
            const value = resolveExampleValue(doc, example)
            const refName = schemaRefName(media.schema)
            const typeExpr = refName
              ? `components['schemas']['${refName}']`
              : `paths['${pathKey}']['${method}']['responses']['${status}']['content']['${contentType}']`

            const exportName = `${operationId}${status}${pascalCase(exampleName)}`
            if (seenNames.has(exportName)) {
              throw new Error(`duplicate fixture export name: ${exportName}`)
            }
            seenNames.add(exportName)

            fixtures.push({ exportName, typeExpr, value })
          }
        }
      }
    }
  }

  const banner = `// Generated by \`pnpm gen:fixtures\` — do not edit.
// Source: server/api/openapi.yaml's response examples (PLAN.md P4-01).
// Regenerate with \`pnpm gen:fixtures\` after editing openapi.yaml.

import type { components, paths } from '@/api/schema'

`

  const body = fixtures
    .map(({ exportName, typeExpr, value }) => `export const ${exportName} = ${serialize(value)} satisfies ${typeExpr}\n`)
    .join('\n')

  writeFileSync(OUT_PATH, banner + body)
  console.log(`Wrote ${fixtures.length} fixtures to ${OUT_PATH}`)
}

main()
