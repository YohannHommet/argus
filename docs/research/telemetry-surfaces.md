# Coding-Agent Telemetry Surfaces — Research Report

*Compiled 2026-08-11 (Opus research agent). Facts and source URLs only; no design decisions. Confidence flags where evidence is thin.*

---

## (a) Claude Code ingestion surfaces, ranked

Claude Code is by far the richest-instrumented agent surveyed. Five distinct surfaces, ranked by richness × reliability:

### 1. OpenTelemetry (metrics + logs GA, traces beta) — **richest, most reliable, officially supported**

**Enable:** `CLAUDE_CODE_ENABLE_TELEMETRY=1` plus at least one exporter. Off by default; fails *silently* on export errors unless `CLAUDE_CODE_OTEL_DIAG_STDERR=1`.

| Signal | Enable var | Default export interval |
|---|---|---|
| Metrics | `OTEL_METRICS_EXPORTER` (`otlp`\|`prometheus`\|`console`\|`none`) | `OTEL_METRIC_EXPORT_INTERVAL=60000` |
| Log events | `OTEL_LOGS_EXPORTER` (`otlp`\|`console`\|`none`) | `OTEL_LOGS_EXPORT_INTERVAL=5000` |
| Traces (beta) | `OTEL_TRACES_EXPORTER` + `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` | `OTEL_TRACES_EXPORT_INTERVAL=5000` |

**Transport:** standard OTLP — `OTEL_EXPORTER_OTLP_PROTOCOL` ∈ `grpc`, `http/json`, `http/protobuf`; `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, plus per-signal overrides. mTLS via `OTEL_EXPORTER_OTLP_CLIENT_KEY`/`_CERTIFICATE` (gRPC) or `CLAUDE_CODE_CLIENT_CERT`/`_KEY`/`_KEY_PASSPHRASE` + `NODE_EXTRA_CA_CERTS` (HTTP). Temporality via `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta|cumulative`.

**Rotating credentials:** `otelHeadersHelper` in `settings.json` points at a script returning JSON headers; refresh debounce `CLAUDE_CODE_OTEL_HEADERS_HELPER_DEBOUNCE_MS` (default 1740000 ms ≈ 29 min). Since **v2.1.217**, managed settings *lock* endpoint/protocol/credentials and strip conflicting developer-set vars at startup — relevant for enforced enterprise ingestion.

**7 metrics:**
- `claude_code.session.count` — attr `start_type` ∈ fresh|resume|continue|agents_view
- `claude_code.lines_of_code.count` — `type` ∈ added|removed, `model`
- `claude_code.commit.count`, `claude_code.pull_request.count` — **git/SDLC outcome counters emitted by the agent itself**
- `claude_code.cost.usage` (unit `USD`) — `model`, `query_source` ∈ main|subagent|auxiliary, `speed`, `effort` ∈ low|medium|high|xhigh|max, `agent.name`, `skill.name`, `plugin.name`, `marketplace.name`, `mcp_server.name`, `mcp_tool.name`
- `claude_code.token.usage` (unit `tokens`) — `type` ∈ input|output|cacheRead|cacheCreation + same attribution set
- `claude_code.code_edit_tool.decision` — `tool_name` ∈ Edit|Write|NotebookEdit, `decision` ∈ accept|reject, `source` ∈ config|hook|user_permanent|user_temporary|user_abort|user_reject, `language`
- `claude_code.active_time.total` (unit `s`) — `type` ∈ user|cli

**16 log events** (all prefixed `claude_code.`, all carrying `event.name`, `event.timestamp` ISO 8601, `event.sequence`):
`user_prompt`, `assistant_response`, `tool_result`, `tool_decision`, `api_request`, `api_error`, `api_refusal`, `api_request_body`, `api_response_body`, `permission_mode_changed`, `auth`, `mcp_server_connection`, `internal_error`, `plugin_installed`, `plugin_loaded`.

High-value fields:
- `api_request`: `model`, `cost_usd`, `cost_usd_micros`, `duration_ms`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `request_id`, `client_request_id`, `attempt`, `speed`, `effort`, `query_source`
- `tool_result`: `tool_name`, `tool_use_id`, `success`, `duration_ms`, `error_type`, `tool_input_size_bytes`, `tool_result_size_bytes`, `decision_source`, `mcp_server_scope`; with `OTEL_LOG_TOOL_DETAILS=1` also `tool_parameters` (incl. `bash_command`, `full_command`, `git_commit_id`, `subagent_type`, `skill_name`) and `tool_input` (~4 KB cap)
- `tool_decision`: `decision` ∈ accept|reject, `tool_source` ∈ builtin|mcp|sdk_host_builtin_mcp, `source` (6 values) — **the permission-decision audit trail**
- `permission_mode_changed`: `from_mode`/`to_mode` ∈ default|plan|acceptEdits|auto|dontAsk|bypassPermissions, `trigger`
- `api_refusal`: `category` ∈ cyber|bio|frontier_llm|reasoning_extraction, `server_fallback_hop`

**Standard attributes on everything:** `session.id`, `organization.id`, `user.id`, `user.account_uuid`, `user.account_id`, `user.email`, `user.groups`, `terminal.type` (iTerm.app|vscode|cursor|tmux), `identity.source`, `app.version`, `app.entrypoint` (cli|sdk-cli|sdk-ts|sdk-py|claude-vscode). Event-only: `prompt.id` (correlates a prompt to every downstream event until the next prompt — **the natural turn-grouping key**), `message.uuid` (joins telemetry to the local transcript), `client_request_id`, `workspace.host_paths`, `workflow.run_id`, `workflow.name`.

**Cardinality/PII controls:** `OTEL_METRICS_INCLUDE_SESSION_ID` (default true), `_INCLUDE_VERSION` (false), `_INCLUDE_ACCOUNT_UUID` (true), `_INCLUDE_ENTRYPOINT` (false), `_INCLUDE_RESOURCE_ATTRIBUTES` (true). Content is opt-in only: `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT` (needs tracing), `OTEL_LOG_RAW_API_BODIES=1|file:<dir>`. Truncation `CLAUDE_CODE_OTEL_CONTENT_MAX_LENGTH` (default 61440 UTF-16 units).

**Traces (beta) span tree** — the only surface giving true causal structure:

```
claude_code.interaction
├── claude_code.llm_request
├── claude_code.hook                 (detailed beta only)
└── claude_code.tool
    ├── claude_code.tool.blocked_on_user   ← human decision latency
    ├── claude_code.tool.execution
    └── (Agent tool) nested subagent spans
```

Notable attrs: `interaction.sequence`, `ttft_ms`, `agent_id`/`parent_agent_id`, `stop_reason`, `file_path`, `full_command`, `result_tokens`; partial gen_ai alignment (`gen_ai.system="anthropic"`, `gen_ai.request.model`, `gen_ai.response.id`, `gen_ai.response.finish_reasons`, `gen_ai.tool.call.id`). **W3C context propagation:** `TRACEPARENT` is injected into every Bash/PowerShell subprocess and into outbound HTTP MCP calls, and `claude -p`/Agent SDK runs honour an *inbound* `TRACEPARENT` — so a CI job's trace and the agent's trace can be one tree. Interactive CLI ignores inbound traceparent. `CLAUDE_CODE_PROPAGATE_TRACEPARENT=1` extends propagation to custom `ANTHROPIC_BASE_URL` proxies.

Caveats: traces are beta ("span names and attributes may change between releases"); short `-p` runs can drop batched spans on kill — mitigate by lowering export intervals; `console` exporter is unusable under the Agent SDK (collides with the stdout message channel). Default `service.name` is `claude-code`.

### 2. Hooks — **only real-time, blocking, and push-capable surface**

30 hook events, grouped: session (`SessionStart`, `SessionEnd`, `Setup`), turn (`UserPromptSubmit`, `Stop`, `StopFailure`), tool loop (`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`, `PermissionDenied`, `PostToolBatch`), agentic (`SubagentStart`, `SubagentStop`, `TaskCreated`, `TaskCompleted`, `TeammateIdle`), filesystem/config (`ConfigChange`, `CwdChanged`, `DirectoryAdded`, `FileChanged`, `InstructionsLoaded`, `WorktreeCreate`, `WorktreeRemove`), compaction (`PreCompact`, `PostCompact`), MCP (`UserPromptExpansion`, `Elicitation`, `ElicitationResult`), display (`MessageDisplay`, `Notification`).

Common stdin payload: `session_id`, `prompt_id`, `transcript_path`, `cwd`, `permission_mode`, `effort.level`, `hook_event_name`; subagents add `agent_id`, `agent_type`.

**Decisive fact for ingestion: `"type": "http"` is a first-class hook type** — no shell wrapper needed:

```json
{ "type": "http", "url": "http://localhost:8080/hook",
  "headers": { "Authorization": "Bearer $MY_TOKEN" },
  "allowedEnvVars": ["MY_TOKEN"] }
```

Other types: `command` (with `async: true` / `asyncRewake: true` for non-blocking), `mcp_tool`, `prompt`, `agent`. Timeouts default 600 s (30 s `UserPromptSubmit`, 10 s `MessageDisplay`, **1.5 s shared budget for `SessionEnd`** — a hard constraint on end-of-session flushes). Output capped at 10 000 chars. Config scopes: `~/.claude/settings.json`, `.claude/settings.json`, `.claude/settings.local.json`, managed policy settings (org-wide, admin-controlled), plugin `hooks/hooks.json`, skill/agent frontmatter. Kill switch: `disableAllHooks`.

Hooks are the only way to observe things OTel does not emit as events (`FileChanged`, `CwdChanged`, `WorktreeCreate`, `InstructionsLoaded`, `TaskCreated/Completed`) — and the only way to *intervene*.

### 3. Local JSONL transcripts — **richest content, least stable**

Path: `~/.claude/projects/<project>/<session-id>.jsonl`, `<project>` = cwd with non-alphanumerics → `-`. Also reachable per-event via the hook/statusline `transcript_path` field.

Verified against 1 639 local transcripts on this machine (Claude Code v2.1.x). Line `type` values observed, by frequency in a large session: `assistant` (969), `user` (551), `attachment` (240), `last-prompt` (167), `mode` (153), `custom-title` (153), `system` (93), `agent-setting` (87), `permission-mode` (86), `file-history-snapshot` (44), `frame-link` (39), `queue-operation` (30), `file-history-delta` (19).

Message envelope: `uuid`, `parentUuid`, `timestamp`, `sessionId`, `session_id`, `sessionKind`, `userType`, `entrypoint`, `cwd`, `version`, `gitBranch`, `isSidechain` (subagent marker), `promptId`, `slug`, `effort`, `requestId`, `attributionSkill`, `permissionMode`, `origin`, `promptSource`, `isMeta`, `isApiErrorMessage`, `isCompactSummary`, `sourceToolUseID`, `sourceToolAssistantUUID`.

`assistant.message` = full Anthropic API response (`id`, `model`, `content`, `stop_reason`, `stop_details`, `usage`, `context_management`, `diagnostics`). `assistant.message.usage` keys: `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`, `cache_creation`, `server_tool_use`, `service_tier`, `inference_geo`, `iterations`, `speed`.

`toolUseResult` is **shape-per-tool** — the highest-fidelity edit data available anywhere. Observed shapes: Bash → `{stdout, stderr, interrupted, isImage, noOutputExpected[, backgroundTaskId]}`; Edit → `{filePath, oldString, newString, originalFile, structuredPatch, userModified, replaceAll}`; Write → `{type, filePath, content, structuredPatch, originalFile, userModified}`; Read → `{type, file}`; WebFetch → `{url, path, title, updated, version, liveSubscription}`; ToolSearch → `{matches, query, total_deferred_tools}`. `system` subtypes seen: `compact_boundary`, `turn_duration`, `local_command`, `away_summary`.

**Reliability caveat, stated in the docs:** "The entry format is internal to Claude Code and changes between versions, so scripts that parse these files directly can break on any release." Retention defaults to 30 days (`cleanupPeriodDays`); location moves with `CLAUDE_CONFIG_DIR`; writes suppressible via `CLAUDE_CODE_SKIP_PROMPT_HISTORY` or `--no-session-persistence`. `message.uuid` in OTel events joins telemetry ↔ transcript. Officially sanctioned alternatives: `/export`, `claude -p --output-format json|stream-json`, `claude -p --resume <id>`, the Agent SDK message stream.

### 4. Claude Code Analytics API (Admin) — **server-side, aggregated, zero client config**

`GET https://api.anthropic.com/v1/organizations/usage_report/claude_code`, header `x-api-key: $ADMIN_API_KEY` + `anthropic-version: 2023-06-01`. Params: `starting_at` (YYYY-MM-DD, **single UTC day only**), `limit` (default 20, max 1000), `page`. One record per user per day.

Fields: `date`, `actor` (`user_actor.email_address` | `api_actor.api_key_name`), `organization_id`, `customer_type` ∈ api|subscription, `terminal_type`; `core_metrics{num_sessions, lines_of_code{added,removed}, commits_by_claude_code, pull_requests_by_claude_code}`; `tool_actions{edit_tool, multi_edit_tool, write_tool, notebook_edit_tool}` each `{accepted, rejected}`; `model_breakdown[]{model, tokens{input,output,cache_read,cache_creation}, estimated_cost{currency, amount /* cents USD */}}`.

Limits: daily aggregation only (no real-time), ~1 h freshness lag, **Claude API deployments only** — Bedrock / Microsoft Foundry / Vertex / Claude Platform on AWS excluded. Free. Unavailable to individual accounts. Claude Enterprise (claude.ai) orgs use the separate Claude Enterprise Analytics API with an Analytics API key instead.

Sibling: `GET /v1/organizations/usage_report/messages` — bucket_width `1m`/`1h`/`1d`, `group_by` ∈ account_id, api_key_id, context_window, inference_geo, model, service_account_id, service_tier, speed, workspace_id; returns `uncached_input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`, `server_tool_use.web_search_requests`. Limits: 1d → 31 max, 1h → 168, 1m → 1440. Not Claude-Code-specific (all API traffic).

### 5. Compliance API (Claude Enterprise) — **server-retained transcripts, no client agent**

Under-documented but significant: Anthropic retains Claude Code local session transcripts server-side and exposes them.
- `GET /v1/compliance/apps/sessions/local`
- `GET /v1/compliance/apps/sessions/local/{session_id}`
- `GET /v1/compliance/apps/sessions/local/{session_id}/messages`
- Activity feed: `GET /v1/compliance/activities`

Object types `compliance_local_session`, `compliance_local_session_message`. Requires a **Compliance Access Key** with `read:compliance_user_data` (the Activity Feed alone works with an Admin API key + `read:compliance_activities`). Retention: local session transcripts **6 years** by default. Rate limit 600 req/min per parent org. Claude Enterprise only; no user filter on the local-session list. Covers Claude Code sessions run on users' machines while signed in with an Enterprise account.

**Ranking summary:** OTel logs+metrics (breadth, real-time, stable, org-enforceable) > hooks (real-time, HTTP-native, blocking, filesystem/task events OTel lacks) > OTel traces beta (causal structure, cross-process joins — but unstable) > local JSONL (maximum content fidelity, explicitly unstable, client-side only) > Analytics API (zero-config org rollout, but daily/aggregate) > Compliance API (Enterprise-only retro retrieval).

---

## (b) Other coding agents — survey

*Note: this section is thinner than (a). Items marked ⚠ rest on search-result summaries — verify before designing against them.*

| Agent | OTel? | Config surface | Local structured logs | Hooks / push | Server usage API |
|---|---|---|---|---|---|
| **Gemini CLI** | **Yes — traces, metrics, structured logs**, closest peer to Claude Code | `.gemini/settings.json` `telemetry.*` (`telemetry.otlpProtocol`) + env vars + CLI flags (`--telemetry-otlp-protocol`); **OTLP/gRPC and OTLP/HTTP** | yes (via OTel logs) | ⚠ unconfirmed | ⚠ unconfirmed |
| **OpenAI Codex CLI** | **Yes — native, opt-in, off by default**; log events for outbound API requests, streaming responses, user input, **tool-approval decisions**, tool results | `[otel]` table in `~/.codex/config.toml` | **Rollout JSONL**: `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`. Known pathology: unbounded growth — reports of 700 MB–2 GB single files, ~91 GB dirs (openai/codex#24948) | ⚠ unconfirmed | ⚠ unconfirmed |
| | | **Gap:** `[otel]` config is honoured fully only by the interactive CLI. `codex exec` emits **no OTel metrics**; `codex mcp-server` emits **no OTel telemetry at all** (openai/codex#12913) | | | |
| **Cursor** | Not natively ⚠ | — | — | — | Team/AI-Code-Tracking analytics API exists ⚠ (Faros/DX ingest it); Faros also captures IDE-side accept-time tagging |
| **opencode** | Covered by the third-party `opentelemetry-hooks` shim, not confirmed native ⚠ | — | ⚠ unconfirmed | ⚠ unconfirmed | n/a |
| **Aider** | No native OTel found ⚠ | — | writes `.aider.chat.history.md` / `.aider.input.history` ⚠ | no | n/a |
| **GitHub Copilot** | No client OTel; **Copilot Metrics API** (org/enterprise, daily aggregates) | — | — | — | yes |

**Cross-agent normalizer that already exists:** `o11y-dev/opentelemetry-hooks` — a single OTel integration layer claiming coverage of Antigravity, Claude Code, Codex, Cursor IDE / Cursor CLI, Gemini CLI, GitHub Copilot, OpenCode and Windsurf, emitting under OTel GenAI semantic conventions, with custom extensions beyond the spec because the spec models nothing code-specific. Closest thing to a de-facto multi-agent ingestion contract.

**Structural conclusion:** Claude Code, Codex CLI and Gemini CLI all converge on *OTLP-out + local JSONL sessions*, with per-agent attribute vocabularies that do not match each other. Codex and Gemini both surface tool-approval decisions, so the accept/reject axis is portable across at least three agents.

---

## (c) Existing-tools landscape and the gap

### Generic LLM-observability platforms

Langfuse, LangSmith, Helicone, Traceloop/OpenLLMetry, Braintrust, Arize Phoenix, W&B Weave, Datadog LLM Observability, Honeycomb, Logfire. All accept OTLP (Langfuse is named in Anthropic's own Agent SDK observability docs as a valid OTLP target), and Langfuse specifically is used for Claude Code with full prompt+response capture, tool-call tracing and session grouping across turns.

Their model is **prompt → completion → evaluation**. What they lack, structurally:
- No notion of a *repository*, *branch*, *worktree*, or *file*
- No notion of a *permission decision* (accept/reject, and *who or what* decided: config vs hook vs human)
- No notion of *human-in-the-loop wait time* (Claude Code's `claude_code.tool.blocked_on_user` has no equivalent concept in an LLM-app tracer)
- Their unit of quality is an eval score, not "did the change survive code review / CI / a revert"
- They ingest via SDK instrumentation of *your* app; a CLI agent on a developer laptop is an awkward fit (no server to instrument)

### Claude-Code-specific tooling

- **ccusage** (ryoppippi) — parses local JSONL for token/cost accounting. Local-only, single-user, cost-focused. Companion `jeremyeder/ccusage-graphs` adds an HTML dashboard, SQLite historical backend, and a Prometheus+Grafana Docker stack.
- **ColeMurray/claude-code-otel** — packaged OTel Collector → Prometheus (metrics) + Loki (events) → Grafana stack for Claude Code.
- **Grafana Cloud has a first-party Claude Code integration** and a published dashboard (grafana.com/grafana/dashboards/25052). Plus a long tail of blog-grade stacks (Quesma/Grafana Cloud, VictoriaMetrics, Pigsty).
- Coverage of this whole cluster is essentially identical: **sessions, cost, tokens, lines-of-code, sometimes rate-limit headroom.** Dashboards over the metric surface. None model the tool-decision graph, subagent trees, or MCP topology, and none join to git.

### Engineering-intelligence platforms — the only ones attempting the join

- **Faros AI** ingests telemetry from GitHub Copilot, Claude Code, Amazon Q Developer and Cursor, and correlates it with downstream PR throughput, cycle time, incident rate and bug counts across 100+ SDLC connectors. It tags AI-generated suggestions at accept-time in the IDE and follows those lines through the PR into the repo, yielding %-AI-code per PR/commit. Its April 2026 report (2 years, 22 000 developers, 4 000+ teams) reported high AI adoption correlating with **+54 % bugs per developer** alongside **+34 % tasks completed**.
- **DX**, **Exceeds AI**, and (per the search surface) Jellyfish / LinearB / Swarmia occupy adjacent ground. Anthropic's own Analytics API is a documented DX connector (docs.getdx.com/connectors/claude-code).

### The gap

1. **Agent-native semantics are unmodelled anywhere.** No tool — generic or specific — treats the *permission decision* as a first-class object with its provenance (`source` ∈ config|hook|user_permanent|user_temporary|user_abort|user_reject) even though Claude Code emits exactly that on every tool call, and Codex and Gemini emit an analogue.
2. **The two camps sit on opposite sides of a seam.** Grafana/Langfuse-class tools have per-tool-call resolution but no SDLC outcome; Faros/DX-class tools have SDLC outcome but ingest aggregate vendor APIs (daily, user-level) rather than the per-turn stream. Nothing correlates *this specific tool call / this specific edit* with *this commit → this MR → this CI result → this revert*. Claude Code hands you the join keys for free (`session.id`, `prompt.id`, `tool_use_id`, `message.uuid`, `gitBranch` in the transcript, `git_commit_id` in Bash `tool_parameters`, and `commit.count`/`pull_request.count` metrics) and no tool consumes them that way.
3. **Subagent/delegation trees and MCP topology are dark.** `agent_id`/`parent_agent_id`, `isSidechain`, nested-subagent spans, `mcp_server_connection` — emitted, ingested by nobody.
4. **Cost is per-request but not per-outcome.** Everyone reports $/day and $/model. Nobody reports $/merged-MR or $/reverted-commit.
5. **Cross-agent normalization is unsolved except by one small OSS shim** (`opentelemetry-hooks`), which has to invent extensions because the standard has no vocabulary for it — see (d).
6. **Reliability of the richest surface is unowned.** The JSONL transcript is the only place `structuredPatch`, `oldString`/`newString`, and `userModified` exist, and Anthropic explicitly disclaims its stability. Anyone depending on it needs version-pinned parsers; no existing tool advertises that discipline.

---

## (d) OTel GenAI semantic conventions — state as of Aug 2026

**Governance/stability.** GenAI conventions have moved out of `open-telemetry/semantic-conventions` into a dedicated repo, **`open-telemetry/semantic-conventions-genai`**, with **no versioned releases yet** — the spec lives on `main`. Every signal (spans, metrics, events, exceptions, agent spans, MCP) is at status **Development**. Last mainline release carrying GenAI specs was **semconv v1.37.0**. ⚠ Mainline-version/date figures were internally inconsistent in research; treat "current mainline version number" as unverified. Practical takeaway: **nothing here is stable, and it is a moving target on an unreleased branch.**

**Breaking rename to know about:** `gen_ai.system` → **`gen_ai.provider.name`** (v1.36→v1.37). Claude Code's beta spans still emit the *old* `gen_ai.system="anthropic"` — so any normalizer must handle both.

**Spans.** Required: `gen_ai.operation.name` ∈ `chat`, `embeddings`, `retrieval`, `execute_tool`, `create_agent`, `invoke_agent`, `plan`; `gen_ai.provider.name` ∈ `openai`, `anthropic`, `aws.bedrock`, `azure.ai.inference`, `gcp.vertex_ai`. Conditionally required: `gen_ai.request.model`, `gen_ai.conversation.id`, `error.type` (Stable). Recommended: `gen_ai.response.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.request.temperature`, `gen_ai.request.max_tokens`, `gen_ai.request.top_k`/`top_p`, `gen_ai.response.finish_reasons` (array), `gen_ai.request.stream`, `gen_ai.response.time_to_first_chunk`. Span name = `{gen_ai.operation.name} {gen_ai.request.model}`.

**Agent + tool.** `gen_ai.agent.name`/`.id`/`.description` (Recommended). `gen_ai.tool.name` (Required on tool calls), `gen_ai.tool.call.id`, `gen_ai.tool.type`, `gen_ai.tool.description` (Recommended), `gen_ai.tool.definitions` (Opt-In). MCP tool calls carry both `gen_ai.operation.name=execute_tool` and `mcp.method.name=tools/call`, span name `tools/call {tool_name}`.

**Metrics** (all Histograms): `gen_ai.client.token.usage` `{token}` (attrs `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.token.type`), `gen_ai.client.operation.duration` `s`, `gen_ai.client.operation.time_to_first_chunk` `s`, `gen_ai.client.operation.time_per_output_chunk` `s`; server-side `gen_ai.server.request.duration`, `gen_ai.server.time_to_first_token`, `gen_ai.server.time_per_output_token`; agentic `gen_ai.invoke_workflow.duration`, `gen_ai.invoke_agent.duration`, `gen_ai.invoke_agent.inference_calls` `{inference_call}`, `gen_ai.invoke_agent.tool_calls` `{tool_call}`, `gen_ai.execute_tool.duration`.

**No cost metric exists** — confirmed. Cost must be derived from `gen_ai.client.token.usage` × a price table you maintain (open issue #23 on token-type granularity). Contrast: Claude Code ships a real `claude_code.cost.usage` in USD, which the standard has no home for.

**Events/logs.** Current: single `gen_ai.client.inference.operation.details` event, plus `gen_ai.evaluation.result`. Deprecated in v1.37.0: the per-message events `gen_ai.system.message`, `gen_ai.user.message`, `gen_ai.assistant.message`, `gen_ai.tool.message`, `gen_ai.choice` → replaced by `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions` (all **Opt-In**, PII-bearing).

**Content/stability env vars:** `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` ∈ `NO_CONTENT` (default) | `SPAN_ONLY` | `EVENT_ONLY` | `SPAN_AND_EVENT`; `OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental` to emit the newer conventions rather than legacy v1.36. **Neither matches Claude Code's vocabulary** — Claude Code uses its own `OTEL_LOG_*` family.

**Anthropic-specific doc:** requires `gen_ai.usage.cache_creation.input_tokens` and `gen_ai.usage.cache_read.input_tokens`, with the aggregation rule `gen_ai.usage.input_tokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens`. Claude Code emits the un-aggregated form (`type` ∈ input|cacheRead|cacheCreation on `claude_code.token.usage`), so a conforming normalizer must do this sum itself.

**Coding-agent coverage: none.** Confirmed absent from the conventions: file edits (path, lines added/removed, diff), shell commands executed, code generation/synthesis, subagent spawning/orchestration, and any wiring of MCP tool calls to code-edit semantics. What exists nearby is generic and unrelated: CLI spans (exit code, PID, args) and code attributes (`code.file.path`, `code.function.name`, `code.lineno`). Gemini CLI has an open epic to align with GenAI conventions, and file-operation detail is explicitly out of scope. **Every coding-agent-specific attribute ingested will be vendor-namespaced (`claude_code.*`) or a custom extension, for the foreseeable future.**

---

## (e) Sources

**Claude Code (primary, all verified by direct fetch):**
- https://code.claude.com/docs/en/monitoring-usage — metrics, events, all OTEL_* vars, traces beta, standard attributes
- https://code.claude.com/docs/en/hooks — 30 hook events, payloads, HTTP hook type, exit codes, matchers
- https://code.claude.com/docs/en/sessions — transcript path, JSONL format + instability disclaimer, `CLAUDE_CONFIG_DIR`, `cleanupPeriodDays`, script interfaces
- https://code.claude.com/docs/en/agent-sdk/observability — SDK→CLI telemetry passthrough, span tree, trace-context propagation, content opt-in table
- https://platform.claude.com/docs/en/manage-claude/claude-code-analytics-api — endpoint, params, full response schema, limits
- https://platform.claude.com/docs/en/api/admin-api/usage-cost/get-messages-usage-report — usage report params/fields
- https://platform.claude.com/docs/en/manage-claude/compliance-api and .../compliance-content-data — local-session endpoints, scopes, 6-year retention
- https://platform.claude.com/docs/en/manage-claude/analytics-api — Admin vs Enterprise Analytics key split
- Ground truth for the JSONL schema: 1 639 transcripts under `~/.claude/projects/` (Claude Code v2.1.x), inspected via `jq` key enumeration only.

**Other agents:**
- https://github.com/openai/codex/discussions/3827 — session/rollout files
- https://github.com/openai/codex/issues/24948 — rollout JSONL unbounded growth
- https://github.com/openai/codex/issues/12913 — `codex exec` / `codex mcp-server` OTel gaps
- https://github.com/openai/codex/pull/2103 — OpenTelemetry events
- https://developers.openai.com/codex/config-advanced — `[otel]` in config.toml
- https://geminicli.com/docs/cli/telemetry/ — Gemini CLI OTel, settings.json, OTLP gRPC/HTTP
- https://github.com/o11y-dev/opentelemetry-hooks — multi-agent OTel normalizer

**Existing tools:**
- https://github.com/ColeMurray/claude-code-otel
- https://grafana.com/docs/grafana-cloud/monitor-infrastructure/integrations/integration-reference/integration-claude-code/ and https://grafana.com/grafana/dashboards/25052-claude-code/
- https://github.com/jeremyeder/ccusage-graphs
- https://quesma.com/blog/track-claude-code-usage-and-limits-with-grafana-cloud/
- https://www.faros.ai/copilot-module and https://www.faros.ai/blog/claude-code-analytics
- https://hackernoon.com/ai-code-disconnect-measuring-what-is-generated-vs-what-survives
- https://blog.exceeds.ai/ai-agent-dx-metrics-comparison/
- https://docs.getdx.com/connectors/claude-code/
- https://www.minware.com/blog/how-to-get-reporting-data-out-of-claude-code

**OTel GenAI conventions:**
- https://github.com/open-telemetry/semantic-conventions-genai (+ `docs/gen-ai/`: `gen-ai-spans.md`, `gen-ai-metrics.md`, `gen-ai-events.md`, `gen-ai-agent-spans.md`, `anthropic.md`, `openai.md`, `aws-bedrock.md`, `azure-ai-inference.md`, `mcp.md`)
- https://github.com/open-telemetry/semantic-conventions/releases/tag/v1.37.0
- https://github.com/open-telemetry/semantic-conventions-genai/issues/23 — cost/token-type tracking
- https://opentelemetry.io/blog/2026/genai-observability/
- https://opentelemetry.io/docs/specs/semconv/cli/cli-spans/

**Confidence flags:** section (a) and (d)-attribute-level facts are direct-fetch verified. Section (b) rows for Cursor, opencode and Aider, and section (d)'s mainline semconv version number, rest on search-result summaries and are marked ⚠ — verify before designing against them.
