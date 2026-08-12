# testdata/metrics fixtures

Each file is the `protojson` encoding of a
`go.opentelemetry.io/proto/otlp/metrics/v1.MetricsData` message (the same wire shape
`FromOTLPMetrics` decodes in production). All but one were generated with a throwaway Go program
(`protojson.MarshalOptions{Indent: "  "}` against the pinned `google.golang.org/protobuf v1.36.12`
marshaler), not hand-typed, so field names (`resourceMetrics`, `timeUnixNano`, `asInt` as a JSON
string, `aggregationTemporality` as the enum's string name, …) match production exactly.

## Provenance — unverified against real Claude Code output

**None of these fixtures are capture-derived.** The live capture
(`docs/research/live-capture-2026-08-11.md`) ran with `OTEL_METRICS_EXPORTER=none`, so it captured
zero metric data points and verifies nothing about the metrics surface's actual wire shape,
attribute names, or value types. Every attribute set below is instead taken from
`docs/research/telemetry-surfaces.md`'s "7 metrics" list (compiled from Anthropic's documentation,
not observation). Treat the attribute *names and enum values* here as documented-but-unverified —
a real Claude Code capture with metrics enabled could reveal surprises the same way the log-event
capture did (three undocumented log events, an undocumented `terminal.type`, an undocumented
`query_source` vocabulary).

## Inventory — the 7 documented `claude_code.*` metrics (SPEC §1.8 table)

| File | Metric | Type | Temporality | Notes |
|---|---|---|---|---|
| `session_count.json` | `claude_code.session.count` | Sum | delta | `start_type=fresh` |
| `lines_of_code_count.json` | `claude_code.lines_of_code.count` | Sum | delta | 3 data points: [0]/[1] have identical attributes in reversed key order (same series — `series_hash` stability AC); [2] changes `type: added→removed` (different series — `series_hash` difference AC) |
| `commit_count_no_session.json` | `claude_code.commit.count` | Sum | delta | **no `session.id` attribute at all** — "accepted with `session_id = NULL`" AC |
| `pull_request_count.json` | `claude_code.pull_request.count` | Sum | delta | has `session.id` |
| `cost_usage_cumulative.json` | `claude_code.cost.usage` | Sum | **cumulative** | the "cumulative vs delta labelled correctly" AC; `model`, `query_source`, `speed`, `effort` |
| `token_usage.json` | `claude_code.token.usage` | Sum | delta | `type=input`, `model` |
| `code_edit_tool_decision.json` | `claude_code.code_edit_tool.decision` | Sum | delta | `tool_name`, `decision`, `source`, `language` |
| `active_time_total.json` | `claude_code.active_time.total` | Sum | delta | `type=user`, `AsDouble` value |

Every fixture above is a Sum because that is what `telemetry-surfaces.md` documents for all 7 —
Claude Code is not documented to emit any of them as a Gauge or Histogram. Gauge and Histogram
support (required by the ticket for *unknown* future metrics, per the store-anything policy) is
exercised by the fixtures below instead.

## Structural / behavioural fixtures (synthetic, exercise one normalizer rule each)

| File | Exercises |
|---|---|
| `unknown_metric_gauge.json` | an unrecognized metric name, OTLP `Gauge` type — store-anything policy: accepted, not rolled up, `temporality="gauge"` |
| `unknown_metric_histogram.json` | an unrecognized metric name, `Histogram` type with `sum` present — the `_sum`/`_count` two-sample AC |
| `unknown_metric_histogram_no_sum.json` | a `HistogramDataPoint` with `sum` absent (legal per OTLP: "if count is zero then sum must be zero", and sum is optional generally) — must yield only a `_count` sample, never a bogus `_sum=0` |
| `unspecified_temporality.json` | `AGGREGATION_TEMPORALITY_UNSPECIFIED` on a Sum — the honest-mapping AC (mapped to `"unspecified"`, not silently forced to `delta` or `cumulative`) |
| `unsupported_summary_type.json` | a `Metric` whose populated oneof variant is `Summary` (not Sum/Gauge/Histogram) — the one metric-level rejection case this normalizer produces |
| `mixed_batch_with_unsupported.json` | a valid `claude_code.commit.count` Sum metric alongside an unsupported-type metric in the same batch — proves the rejection does not discard the rest of the batch |
| `number_datapoint_missing_value.json` | **hand-crafted, not protojson-generated** (protojson always emits `asDouble`/`asInt` for the oneof it set, so a normal marshal cannot produce this shape): a `NumberDataPoint` with *neither* `asDouble` nor `asInt` set — the one data-point-level rejection case ("structurally undecodable": the point carries no value at all) |

`ts1` across every generated fixture is a single fixed nanosecond timestamp
(`1786569600000000000` = 2026-08-12T21:20:00Z) chosen to sit well inside the test `Normalizer`'s
`[fixedNow-retention, fixedNow+1h]` clamp window, matching the `testdata/otel` convention of using
one fixed, in-window timestamp per fixture unless a test is specifically about clamping.
