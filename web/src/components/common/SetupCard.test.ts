import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import SetupCard from './SetupCard.vue'

describe('SetupCard (PLAN.md P4-10)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('substitutes the given endpointUrl into both the env block and the hook JSON (an AC)', () => {
    const wrapper = mount(SetupCard, { props: { endpointUrl: 'https://argus.example.com' } })

    const envText = wrapper.get('[data-testid="setup-step-env"]').text()
    expect(envText).toContain('OTEL_EXPORTER_OTLP_ENDPOINT=https://argus.example.com')

    const hookText = wrapper.get('[data-testid="setup-step-hook"]').text()
    expect(hookText).toContain('https://argus.example.com/ingest/hook')
    // Both hook entries point at the substituted origin, not the literal spec example.
    expect(hookText).not.toContain('http://localhost:8080/ingest/hook')
  })

  it('includes OTEL_LOG_TOOL_DETAILS=1 in the copied env block and explains what it exposes and that Argus works without it (an AC)', () => {
    const wrapper = mount(SetupCard, { props: { endpointUrl: 'http://localhost:8080' } })
    const step = wrapper.get('[data-testid="setup-step-env"]')

    expect(step.text()).toContain('OTEL_LOG_TOOL_DETAILS=1')
    expect(step.text()).toContain('Bash commands')
    expect(step.text()).toContain('Argus works without it')
  })

  it('keeps the SessionEnd hook timeout at 1 with its explanatory comment, never "improved" to 5', () => {
    const wrapper = mount(SetupCard, { props: { endpointUrl: 'http://localhost:8080' } })
    const step = wrapper.get('[data-testid="setup-step-hook"]')

    expect(step.text()).toContain('"timeout": 1')
    expect(step.text()).toContain('shares one hard 1.5 s budget')
  })

  it('shows the sim command and does not promise 25 sessions', () => {
    const wrapper = mount(SetupCard, { props: { endpointUrl: 'http://localhost:8080' } })
    const step = wrapper.get('[data-testid="setup-step-sim"]')

    expect(step.text()).toContain('argusd sim --mode=demo --seed=42 --target http://localhost:8080')
    expect(step.text()).toContain('20 session rows')
    expect(step.text()).not.toContain('25 session')
  })

  it("substitutes the endpointUrl into the sim command's --target, like steps 1 and 2", () => {
    const wrapper = mount(SetupCard, { props: { endpointUrl: 'https://argus.example.com' } })
    const step = wrapper.get('[data-testid="setup-step-sim"]')

    expect(step.text()).toContain('--target https://argus.example.com')
    // A copied command must not seed a different Argus than the one on screen.
    expect(step.text()).not.toContain('http://localhost:8080')
  })

  it('has a working copy button per step, backed by CopyBlock', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })

    const wrapper = mount(SetupCard, { props: { endpointUrl: 'http://localhost:8080' } })
    const buttons = wrapper.findAll('button')
    expect(buttons.length).toBe(3)

    await buttons[0]!.trigger('click')
    await flushPromises()

    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText.mock.calls[0]![0]).toContain('OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:8080')
  })

  it('surfaces a visible failure if the clipboard is unavailable, via CopyBlock', async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    })

    const wrapper = mount(SetupCard, { props: { endpointUrl: 'http://localhost:8080' } })
    await wrapper.findAll('button')[0]!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Clipboard unavailable')
  })
})
