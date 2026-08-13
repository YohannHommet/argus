// Package sim implements argus-sim (SPEC §7), the traffic generator that
// exercises Argus end to end by speaking the same wire protocols a real
// Claude Code process speaks: OTLP/HTTP logs and metrics to
// POST {target}/v1/logs and /v1/metrics, and native "type": "http" hook
// payloads to POST {target}/ingest/hook. It never imports internal/store,
// internal/httpapi, or internal/query (depguard-enforced, .golangci.yml,
// mirroring SPEC §3.1's "sim never touches the database, never imports
// store") — that constraint is what makes `docker compose up && argus-sim`
// a genuine end-to-end exercise of the ingestion path rather than a
// shortcut that writes rows directly.
//
// # Fidelity rule (SPEC §7, normative)
//
// The simulator may only emit an attribute that the live capture
// (docs/research/live-capture-2026-08-11.md) or the research doc
// (docs/research/telemetry-surfaces.md) records the real agent emitting, on
// the event that carries it. Every attribute-setting call site in this
// package carries a comment citing its source line. The two hard
// consequences called out by name in the SPEC:
//
//   - No OTel `api_request`/`tool_result` payload this package builds ever
//     sets `agent_id` or `parent_agent_id` (SPEC §1.9, live capture §3):
//     those attributes exist only on hook payloads for subagents.
//   - Every OTel log record carries both the prefixed `body`
//     (`"claude_code.api_request"`) and the unprefixed `event.name`
//     attribute (`"api_request"`), exactly as the capture shows (finding
//     4.1) and exactly what eventname.go's resolution order expects.
//
// # Generator / transport split
//
// Event *generation* (this package's session.go, otel_log_events.go,
// hook_events.go, otel_metric_events.go) is pure: given a seed, an ordinal,
// and a simulated clock, it deterministically returns OTLP protobuf
// messages and hook JSON payloads with no I/O, no wall-clock reads, and no
// goroutines. That purity is what makes the "round-trips through the
// normalizers with zero unknown kinds and zero rejections" AC a cheap unit
// test with no server involved (roundtrip_test.go) and what makes the
// determinism AC checkable without a live process (golden_test.go).
// Encoding (encode.go) and delivery (transport.go: HTTPTransport,
// FileTransport) are a separate, thin layer the generator never imports —
// runner.go is the only file that wires generation to transport, and it is
// also the only place rate control, concurrency, and the exit report live.
//
// # Chaos hooks (P2-13)
//
// This ticket (P2-12) deliberately excludes --chaos-*: duplicates,
// out-of-order delivery, orphaned turns, clock skew, and unknown event
// names. The seams P2-13 needs are already in place without a rewrite:
//   - runner.go's send loop is the single place every encoded payload
//     passes through before Transport.Send — chaos-duplicates (resend) and
//     chaos-out-of-order (delay/reorder) are send-loop decorators there.
//   - session.go's per-session event slice is generated in full before any
//     transport happens — chaos-orphans (drop/reorder the SessionStart
//     record within that slice) is a post-generation slice transform.
//   - clock.go's Clock.Now is the single time source every event
//     timestamp goes through — chaos-clock-skew is a wrapper around it.
//   - otel_log_events.go's newLogRecord takes an explicit unprefixed name —
//     chaos-unknown is a call site that passes an invented name through the
//     same builder everything else uses.
package sim
