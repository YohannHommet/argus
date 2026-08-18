<script setup lang="ts">
/**
 * The first thing a brand-new Argus deployment shows: `/sessions` with an
 * empty database (`useMetaStore`'s `hasNoData`) renders this instead of a
 * blank table (Phase-4 exit criterion 9). Three copyable blocks — env
 * vars, the Claude Code hook config, and a demo-data command — each built
 * from the real deployment's own origin (`endpointUrl`, SPEC §4.4: ops/
 * read/ingest share one origin) so what a user copies actually works
 * against *this* Argus, not a `localhost` example that only works in dev.
 *
 * SPEC §8.2 / §1.5.2 verbatim, not paraphrased: the env block and hook
 * JSON below are the literal README quickstart and hook config, with only
 * the endpoint substituted in. `OTEL_LOG_TOOL_DETAILS=1` and the
 * `SessionEnd` hook's `timeout: 1` are deliberately not "simplified" —
 * see the inline notes next to each for why.
 */
import { computed } from 'vue'

import CopyBlock from './CopyBlock.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

interface Props {
  /** `useMetaStore().endpointUrl` — the origin this Argus deployment actually serves ops/read/ingest from. */
  endpointUrl: string
}

const props = defineProps<Props>()

const envBlock = computed(
  () =>
    `export CLAUDE_CODE_ENABLE_TELEMETRY=1 \\
       OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=otlp \\
       OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \\
       OTEL_EXPORTER_OTLP_ENDPOINT=${props.endpointUrl} \\
       OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta \\
       OTEL_LOG_TOOL_DETAILS=1`,
)

const hookBlock = computed(
  () =>
    `{ "hooks": {
  "PostToolUse": [ { "hooks": [
    { "type": "http", "url": "${props.endpointUrl}/ingest/hook", "timeout": 5 } ] } ],
  // SessionEnd hooks share a hard 1.5 s budget — keep this at 1.
  "SessionEnd":  [ { "hooks": [
    { "type": "http", "url": "${props.endpointUrl}/ingest/hook", "timeout": 1 } ] } ]
} }`,
)

// `--target` gets the same substituted origin as steps 1 and 2: a copied sim
// command that points at a `localhost` example seeds a *different* Argus than
// the one the reader is looking at, and silently appears to do nothing here.
const simBlock = computed(
  () =>
    `docker compose -f deploy/docker-compose.yml up -d
argusd sim --mode=demo --seed=42 --target ${props.endpointUrl}`,
)
</script>

<template>
  <Card data-testid="setup-card">
    <CardHeader>
      <CardTitle>No data yet — send Argus your telemetry</CardTitle>
    </CardHeader>
    <CardContent class="flex flex-col gap-6 text-sm">
      <p class="text-muted-foreground">
        Argus hasn't seen a single project yet. Run the steps below against a real Claude Code
        session, or generate demo data, and this page fills in on its own.
      </p>

      <section
        class="flex flex-col gap-2"
        data-testid="setup-step-env"
      >
        <h3 class="text-foreground font-medium">
          1. Export the OpenTelemetry env vars
        </h3>
        <pre class="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs"><code>{{ envBlock }}</code></pre>
        <CopyBlock
          :text="envBlock"
          label="Copy env vars"
        />
        <p class="text-muted-foreground text-xs">
          <code>OTEL_LOG_TOOL_DETAILS=1</code> is required for the file-path column, the
          <code>subagent_type</code> linkage, and the file-touch view — without it those fields stay
          permanently empty. It also makes Claude Code log full tool parameters, including the text
          of Bash commands, to your OTel collector — so it's not free, and it's your call. Argus
          works without it; you'll just lose those three views.
        </p>
      </section>

      <section
        class="flex flex-col gap-2"
        data-testid="setup-step-hook"
      >
        <h3 class="text-foreground font-medium">
          2. Add the hook to <code>~/.claude/settings.json</code>
        </h3>
        <pre class="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs"><code>{{ hookBlock }}</code></pre>
        <CopyBlock
          :text="hookBlock"
          label="Copy hook config"
        />
        <p class="text-muted-foreground text-xs">
          The <code>SessionEnd</code> timeout is 1 second on purpose, not 5 — every
          <code>SessionEnd</code> hook shares one hard 1.5 s budget across all of them, and Argus
          itself acks in milliseconds, so a larger value would only eat into other hooks' share of
          that budget for no benefit.
        </p>
      </section>

      <section
        class="flex flex-col gap-2"
        data-testid="setup-step-sim"
      >
        <h3 class="text-foreground font-medium">
          3. No real traffic yet? Generate demo data
        </h3>
        <pre class="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs"><code>{{ simBlock }}</code></pre>
        <CopyBlock
          :text="simBlock"
          label="Copy sim command"
        />
        <p class="text-muted-foreground text-xs">
          The default <code>--sessions=25</code> produces 20 session rows here — five of the
          simulated sessions are metrics-only and correctly produce no session row.
        </p>
      </section>
    </CardContent>
  </Card>
</template>
