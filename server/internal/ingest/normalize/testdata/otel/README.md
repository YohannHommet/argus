# testdata/otel fixtures

Each file is the `protojson` encoding of a `go.opentelemetry.io/proto/otlp/logs/v1.LogsData`
message (the same wire shape `FromOTLPLogs` decodes in production), generated with a throwaway Go
program rather than hand-typed, so field names (`resourceLogs`, `timeUnixNano`, `intValue`
as a JSON string, …) match the pinned `google.golang.org/protobuf v1.36.12` marshaler exactly.

## Redaction (mandatory, applied to every capture-derived fixture)

The live capture (`docs/research/live-capture-2026-08-11.md`) contains real identity values. In
every fixture below marked **capture-derived**, the attribute *keys* are kept but these values are
replaced with obviously-fake placeholders:

- `user.id` → all-zero 68-hex-char string
- `user.email` → `user@example.invalid`
- `organization.id`, `user.account_uuid` → all-zero UUID
- `user.account_id` → `user_00000000000000000000`

`session.id`, `prompt.id`, `tool_use_id`, `request_id`, `terminal.type` (`wsl-Ubuntu` — itself a
test asset per the capture doc), `service.version`, timestamps and `event.sequence` are kept
verbatim. `response`/`prompt` free-text content was already `<REDACTED>` in the capture's own
console output (`OTEL_LOG_USER_PROMPTS`/response content is opt-in and wasn't enabled).

## Inventory

### Capture-derived (real attribute keys/structure/values, PII redacted per above)

| File | `event.name` | Source |
|---|---|---|
| `plugin_loaded.json` | `plugin_loaded` | capture1 seq 0 |
| `hook_registered.json` | `hook_registered` | capture1 seq 3 |
| `mcp_server_connection.json` | `mcp_server_connection` | capture1 seq 9 |
| `assistant_response.json` | `assistant_response` | capture1 seq 11 |
| `user_prompt.json` | `user_prompt` | capture1 seq 12 |
| `hook_execution_start.json` | `hook_execution_start` | capture1 seq 13 |
| `hook_execution_complete.json` | `hook_execution_complete` | capture1 seq 14 |
| `tool_decision.json` | `tool_decision` | capture1 seq 15 (M10 fixture — carries `tool_use_id`) |
| `tool_result.json` | `tool_result` | capture1 seq 16 |
| `api_request_sdk.json` | `api_request` | capture2 seq 14, `query_source=sdk` |
| `api_request_generate_session_title.json` | `api_request` | capture2 seq 10, `query_source=generate_session_title`, no `prompt.id` |

### Synthetic (not observed in the capture; written from SPEC §1.5.1 / `telemetry-surfaces.md`)

`api_error.json`, `api_refusal.json`, `api_request_body.json`, `api_response_body.json`,
`permission_mode_changed.json`, `auth.json`, `internal_error.json`, `plugin_installed.json`,
`unknown_event.json` (an unrecognized `event.name` for the `KindUnknown` fallback case).

### Structural / behavioural fixtures (synthetic, exercise one normalizer rule each)

| File | Exercises |
|---|---|
| `resolution_eventname_field_wins.json` | `LogRecord.EventName` field wins over both `event.name` attr and `body` |
| `resolution_unprefixed_attr.json` | resolution via the unprefixed `event.name` attribute only |
| `resolution_body_only.json` | resolution via the prefixed `body` string only (no `EventName`, no `event.name` attr) — must normalize to the same `event_name` as `resolution_unprefixed_attr.json` |
| `missing_session_id_batch.json` | two records; the first has no `session.id` (→ `Rejection`), the second is still returned |
| `resource_record_collision.json` | resource attribute `flag` (→ `resource.flag` once prefixed) collides with a record attribute literally named `resource.flag`; record wins |
| `clock_skew.json` | `TimeUnixNano` and the `event.timestamp` attribute disagree by 10s (> the 5s SPEC §3.4 threshold) |

All fixtures use the same resource block (`service.name: claude-code`, `service.version: 2.1.228`,
matching the capture) unless the fixture's own behaviour requires a variant (`resource_record_collision.json`
adds one extra resource attribute).
