/**
 * Canonical reason strings for `NullValue`'s tooltip. Centralised (rather
 * than inlined in every table cell) so stores/composables that decide
 * *why* a value is null can share the exact same wording a `NullValue`
 * consumer renders — SPEC §6.1 requires a reason, not just a dash, and a
 * typo'd duplicate string would silently drift from the canonical one.
 */
export const NOT_ATTRIBUTABLE_TO_MODEL = 'Not attributable to a single model'
export const NO_PER_AGENT_COST = 'Claude Code does not emit per-agent cost'
export const NO_HOOK_COVERAGE = 'No hook coverage for this session'
export const NOT_MEASURED = 'Not measured'
/**
 * SPEC §6.2's data-quality tiles: a value the view knows how to show but
 * that no endpoint this view reads (`/meta`, `/quality/unknown-kinds`,
 * `/quality/hook-latency`) currently returns as an aggregate — as opposed
 * to `NOT_MEASURED` (this session/window simply has none) or
 * `NO_HOOK_COVERAGE` (a coverage gap). Distinguishing the two matters here
 * specifically: SPEC §4.1 forbids a fabricated zero standing in for "the
 * API doesn't expose this yet".
 */
export const NOT_EXPOSED_BY_API = "Not exposed by Argus's read API yet"
