import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SetupCard from './SetupCard.vue'

// P6-01 AC: the README's copy-paste env block must be byte-identical to the one
// SetupCard renders, so a self-hoster copying from either source gets the same
// working config. This test reads BOTH and compares — if SetupCard's envBlock
// or the README drifts, this fails with the diff. The endpoint is the README's
// literal http://localhost:8080, so SetupCard is rendered with that same origin.
const README_ENDPOINT = 'http://localhost:8080'

// web/src/components/common -> repo root is four levels up.
const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../..')

function readmeEnvBlock(): string {
  const readme = readFileSync(resolve(repoRoot, 'README.md'), 'utf8')
  // The fenced block that starts with the export line — README has several
  // other bash fences (clone, compose up, sim), so key off the first line.
  const match = readme.match(/```(?:bash|sh)\n(export CLAUDE_CODE_ENABLE_TELEMETRY=1[\s\S]*?)\n```/)
  if (!match) {
    throw new Error('README.md has no ```bash block starting with "export CLAUDE_CODE_ENABLE_TELEMETRY=1"')
  }
  return match[1]
}

function setupCardEnvBlock(): string {
  const wrapper = mount(SetupCard, { props: { endpointUrl: README_ENDPOINT } })
  // The <pre> holds only the env block (its <code>{{ envBlock }}</code>);
  // .text() preserves the internal newlines/indentation and only trims the ends.
  return wrapper.get('[data-testid="setup-step-env"] pre').text()
}

describe('README quickstart env block (P6-01)', () => {
  it('is byte-identical to the block SetupCard.vue renders', () => {
    expect(readmeEnvBlock()).toBe(setupCardEnvBlock())
  })
})
