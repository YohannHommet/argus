# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-09-11

> **Pre-alpha.** Argus is under active development; APIs, schemas, CLI flags, and the
> data model may change without notice before a 1.0 release.

### Added

- OTLP/HTTP ingestion for Claude Code (and any OTel-emitting coding agent) logs and metrics.
- Hook-based ingestion (`SessionStart`, `SessionEnd`, and the rest of the Claude Code hook
  set) for events OTLP alone doesn't carry.
- Session explorer with decision provenance and subagent trees.
- Cost and token analytics, with agent-emitted cost treated as authoritative and a
  repo-maintained price-table fallback marked "estimated".
- Live SSE view for in-flight sessions.
- Data-quality surface: dropped-event and unknown-kind-event visibility, clock-skew and
  ingestion-lag indicators.
- `argus-sim`: a demo-data generator and load-generation tool for exercising the ingest path.
- Self-hosted deployment via `docker-compose` (server + Postgres).

[Unreleased]: https://github.com/YohannHommet/argus/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/YohannHommet/argus/releases/tag/v0.1.0
