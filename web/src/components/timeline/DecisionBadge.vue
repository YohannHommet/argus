<script setup lang="ts">
/**
 * A tool-call decision, with its provenance (`decision_source`) and, when
 * available, its correlation confidence (`ToolCall.correlation`). Used by
 * the Timeline (P4-04, one badge per collapsed item that carries a
 * decision) and reusable wherever else a decision needs the same
 * treatment (e.g. the Tools tab, P4-06).
 *
 * `decision` and `decision_source` are vendor-supplied, unconstrained
 * strings (SPEC §0/§1.9/§4.4) — never switched over exhaustively. The 6
 * documented `decision_source` values (SPEC §1.5: config, hook,
 * user_permanent, user_temporary, user_reject, user_abort) get a friendlier
 * label; anything else — including a value Argus has never seen — renders
 * verbatim through `RawValue` (SPEC §6.1).
 *
 * The `accept`/`reject` icon (round-3 critic gap: "accept=green check,
 * reject=red x") is purely additive to the existing color/text convention
 * `decisionColorClass` already encodes — it never replaces the raw,
 * verbatim decision text, and a decision value that is neither literal
 * string renders with no icon at all rather than guessing.
 */
import { CheckCircle2, XCircle } from '@lucide/vue'
import { computed } from 'vue'

import { Badge } from '@/components/ui/badge'
import NullValue from '@/components/common/NullValue.vue'
import RawValue from '@/components/common/RawValue.vue'
import { NOT_MEASURED } from '@/lib/nullReasons'

export type Correlation = 'exact' | 'otel_only' | 'hook_only' | 'heuristic'

interface Props {
  /** Vendor-supplied, unconstrained (e.g. "accept", "reject", or something Argus has never seen). */
  decision?: string | null
  /** Vendor-supplied, unconstrained — the badge's provenance label. */
  decisionSource?: string | null
  /** ToolCall.correlation (SPEC §2.3/§4.2) — non-exact renders a heuristic-match caveat. Undefined means "unknown/not applicable", treated the same as 'exact' (no caveat). */
  correlation?: Correlation | null
}

const props = withDefaults(defineProps<Props>(), {
  decision: null,
  decisionSource: null,
  correlation: null,
})

/** SPEC §1.5's 6 documented decision_source values — everything else falls through to RawValue verbatim. */
const KNOWN_DECISION_SOURCE_LABELS: Record<string, string> = {
  config: 'Config',
  hook: 'Hook',
  user_permanent: 'User (always)',
  user_temporary: 'User (once)',
  user_reject: 'User (reject)',
  user_abort: 'User (abort)',
}

const knownSourceLabel = computed(() =>
  props.decisionSource !== null ? (KNOWN_DECISION_SOURCE_LABELS[props.decisionSource] ?? null) : null,
)

const decisionColorClass = computed(() => {
  if (props.decision === 'accept') return 'text-accept border-accept/40'
  if (props.decision === 'reject') return 'text-reject border-reject/40'
  return 'text-unknown border-border'
})

/** null for anything but the two literal, already-color-coded values above — see file doc. */
const decisionIcon = computed(() => {
  if (props.decision === 'accept') return CheckCircle2
  if (props.decision === 'reject') return XCircle
  return null
})

const isNonExact = computed(() => props.correlation !== null && props.correlation !== 'exact')

const provenanceText = computed(() => {
  const source = props.decisionSource ?? 'unknown'
  return `decision_source: ${source}`
})

const caveatText = 'This decision was matched heuristically, not by an exact correlation key.'

const tooltipText = computed(() => (isNonExact.value ? `${provenanceText.value} — ${caveatText}` : provenanceText.value))
</script>

<template>
  <NullValue
    v-if="decision === null"
    :reason="NOT_MEASURED"
  />
  <span
    v-else
    class="inline-flex items-center gap-1.5"
    data-testid="decision-badge"
  >
    <Badge
      variant="outline"
      class="gap-1"
      :class="decisionColorClass"
    >
      <component
        :is="decisionIcon"
        v-if="decisionIcon"
        class="size-3"
        aria-hidden="true"
      />
      <RawValue
        :value="decision"
        kind="decision"
      />
    </Badge>

    <span
      class="text-muted-foreground cursor-help text-xs underline decoration-dotted underline-offset-2"
      :title="tooltipText"
      :aria-label="tooltipText"
      data-testid="decision-badge-source"
    >
      <span v-if="knownSourceLabel">{{ knownSourceLabel }}</span>
      <RawValue
        v-else
        :value="decisionSource"
        kind="decision_source"
      />
    </span>

    <span
      v-if="isNonExact"
      class="text-warn text-xs"
      :title="caveatText"
      :aria-label="caveatText"
      data-testid="decision-badge-caveat"
    >⚠</span>
  </span>
</template>
