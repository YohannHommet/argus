# Live capture — Claude Code OTel log events (2026-08-11)

Purpose: resolve the two assumptions that `docs/review/spec-review-1.md` flagged as unverified but
load-bearing (M10 `tool_decision.tool_use_id`, M11 `event.sequence` semantics) and confirm B3
(`api_request` has no subagent attribution). Facts only; the design consequences live in `SPEC.md`.

**Method.** Claude Code v2.1.x, console log exporter, two `-p` runs:

```bash
CLAUDE_CODE_ENABLE_TELEMETRY=1 \
OTEL_LOGS_EXPORTER=console \
OTEL_METRICS_EXPORTER=none \
OTEL_LOGS_EXPORT_INTERVAL=500 \
OTEL_LOG_TOOL_DETAILS=1 \
claude -p "<task>" --allowedTools <tools> --permission-mode acceptEdits --model haiku
```

⚠ **Without `--permission-mode acceptEdits`, no `tool_decision` / `tool_result` events fire in
`-p` mode** — the run never reaches a tool call. Any future capture must include it.

Raw logs: `~/.claude/jobs/bba8bd46/tmp/otel-capture/capture{1,2}.log`. Counts below are from
re-parsing those files (ANSI-stripped), not from the capture session's own summary.

Two sessions observed: 30 log records (`event.sequence` 0–29) and 36 records (0–35).

---

## 1. `event.sequence` — per-session monotonic, dense, zero-based, one shared counter

| Capture | records | sequence min/max | distinct values |
|---|---|---|---|
| 1 (`0d7f3a8f…`) | 30 | 0 / 29 | 30 |
| 2 (`4e76a706…`) | 36 | 0 / 35 | 36 |

No duplicates, no gaps, **starts at 0**, one counter shared across *all* event kinds
(`api_request`, `tool_decision`, `hook_registered`, … interleave on a single sequence), and it
resets for a new `session.id`. **M11 resolved: the property is verified.** Two sessions is a small
sample for a claim about a counter's global behaviour, so the content-hash suffix in the Argus
dedup key stays as cheap insurance against a future regression.

## 2. `claude_code.tool_decision` — carries `tool_use_id`

Observed attribute keys (both captures identical):

```
user.id, session.id, organization.id, user.email, user.account_uuid, user.account_id,
terminal.type, event.name, event.timestamp, event.sequence, prompt.id,
decision, source, tool_name, tool_use_id, tool_source, tool_parameters
```

**M10 resolved, Branch A**: the decision-provenance join on `tool_use_id` is exact, and Argus's
"decision provenance never depends on the correlation heuristic" guarantee holds.

## 3. `claude_code.api_request` — no agent attribution; `query_source` vocabulary differs from docs

Observed keys: `user.id`, `session.id`, `organization.id`, `user.email`, `user.account_uuid`,
`user.account_id`, `terminal.type`, `event.name`, `event.timestamp`, `event.sequence`, `prompt.id`,
`model`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `cost_usd`,
`cost_usd_micros`, `duration_ms`, `request_id`, `client_request_id`, `speed`, `query_source`.

- **No `agent_id` / `parent_agent_id` / subagent attribute.** B3 confirmed: per-subagent cost is
  not derivable from OTel log events in v1.
- **`query_source` observed values: `sdk` (14×), `generate_session_title` (4×)** — *not* the
  documented `main|subagent|auxiliary`. Treat the documented vocabulary as unverified and the
  column as unconstrained text.
- `prompt.id` present on 5 of 6 `api_request` records; the exception is the
  `generate_session_title` call, which happens outside any turn. Turn-level cost attribution works;
  out-of-turn auxiliary calls legitimately have no turn.

## 4. Findings beyond the three questions asked

**4.1 `event.name` is unprefixed.** The record `body` is `"claude_code.api_request"` but the
`event.name` *attribute* is `"api_request"`. A normalizer keyed only on `claude_code.*` matches
nothing. Accept both forms.

**4.2 Three log events not in the research doc's list of 15**, both captures, with useful payloads:

| `event.name` | attributes beyond the standard identity set |
|---|---|
| `hook_registered` | `hook_event`, `hook_matcher`, `hook_type`, `hook_source`, `plugin.name`, `plugin_id_hash`, `safe_mode` (no `prompt.id`) |
| `hook_execution_start` | `hook_event`, `hook_name`, `hook_source`, `num_hooks`, `managed_only`, `safe_mode`, `prompt.id` |
| `hook_execution_complete` | same + `total_duration_ms`, `num_success`, `num_blocking`, `num_cancelled`, `num_non_blocking_error` |

This is directly useful: it lets Argus measure the latency its *own* HTTP hook adds to the agent.

**4.3 `tool_result` populates the size fields.** Observed: `tool_name`, `tool_use_id`, `success`,
`duration_ms`, `error`, `error_type`, `tool_input`, `tool_input_size_bytes`,
`tool_result_size_bytes`, `tool_parameters`, `prompt.id`. Note `error` *and* `error_type`.

**4.4 `user_prompt`** carries `prompt_length` and `message.uuid` (and `prompt` itself only under
`OTEL_LOG_USER_PROMPTS`).

**4.5 Standard attributes documented but absent in this capture**: `app.version`,
`app.entrypoint`, `identity.source`, `user.groups`. `terminal.type` was `"wsl-Ubuntu"`, outside the
documented `iTerm.app|vscode|cursor|tmux` enum. Resource attributes present:
`host.arch`, `os.type`, `os.version`, `service.name`, `service.version`, `wsl.version`, `user.id`,
`session.id`. Consequence: no attribute vocabulary may be constrained by a CHECK constraint or a
Go enum, and `sessions.app_version` should fall back to resource `service.version`.

**Caveat on scope.** This is an SDK / `claude -p` capture (`query_source=sdk`), not an interactive
session, and it used no subagents. Attribute *presence* on the events observed is solid; absence of
`app.entrypoint` may be entrypoint-specific rather than universal.
