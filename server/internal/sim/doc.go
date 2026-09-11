// Package sim implements argus-sim (SPEC §7), the traffic generator that
// exercises Argus end to end via OTLP/HTTP logs/metrics and hook payloads.
// It never imports internal/store, internal/httpapi, or internal/query
// (depguard-enforced per SPEC §3.1) — enabling genuine end-to-end ingestion
// testing rather than database shortcuts.
//
// # Fidelity rule (SPEC §7, normative)
//
// Simulator emits only attributes recorded in live capture or research docs
// on matching events. Two hard consequences: (1) OTel api_request/tool_result
// never set agent_id/parent_agent_id (SPEC §1.9; subagent hooks only);
// (2) every OTel log record carries both prefixed ("claude_code.api_request")
// and unprefixed ("api_request") event.name (SPEC §1.5.1 mapping).
//
// # Generator / transport split
//
// Event generation (session.go, otel_log_events.go, etc.) is pure: returns
// OTLP/hook payloads deterministically from seed, ordinal, and clock (no I/O,
// no goroutines). This purity enables roundtrip_test.go and golden_test.go
// to run without a live server. Encoding/delivery are separate (encode.go,
// transport.go); runner.go wires generation to transport.
//
// # Chaos hooks (P2-13)
//
// Chaos seams are already in place without a rewrite: duplicates/out-of-order
// via Transport.Send decorator (chaos.go), orphans via post-generation slice
// transform, clock-skew via Clock.Now wrapper, unknown via newLogRecord
// call site with invented name.
package sim
