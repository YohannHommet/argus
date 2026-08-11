# Argus — Design Decisions (grilling session, 2026-08-11)

Settled with the project owner (Yohann) in a structured design interview. These are
**decisions, not suggestions** — the spec must conform to them. Anything not covered
here is open for the spec to propose.

## Identity & purpose

- **Name (working codename)**: Argus — the hundred-eyed watchman. Binary: `argusd` (server), `argus-sim` (simulator subcommand).
- **What**: open-source, self-hosted **coding-agent observability platform**.
- **Audience**: (a) personal/team daily-use tool, dogfooded on the owner's own Claude Code sessions, and (b) open-source portfolio project demonstrating senior-level architecture. NOT a SaaS; no multi-tenancy in v1.
- **Thesis / differentiator**: existing tools (Grafana dashboards, ccusage, Langfuse-class) are metric dashboards. Argus models what nobody else does: **permission/tool-decision provenance** (accept/reject + who decided: config|hook|user) and **subagent trees** as first-class objects, on a normalized agent-agnostic event schema (OTel GenAI conventions have zero coding-agent vocabulary).

## Scope

- **Agents observed**: Claude Code first and deepest, but ingestion is agent-agnostic (OTLP-native) so any OTel-emitting agent works.
- **v1 features** (definition of shipped): `docker compose up` → point Claude Code (OTel env vars + one HTTP hook config) at it → UI provides:
  1. **Session explorer**: session list + session detail timeline, with decision badges (permission provenance) and a subagent tree view.
  2. **Cost & token analytics**: dashboards filtered by project, model, date range.
  3. **Live view**: watch an in-flight session in real time.
  Plus: `argus-sim` (demo mode + load generator), README quickstart.
- **v2 backlog** (explicitly out of v1): alerting, fleet view, auth, retention UI, git-join ($/merged-MR), JSONL transcript enrichment, OTel traces ingestion, ClickHouse storage backend.

## Architecture

- **Backend**: Go, **single binary**, stdlib `net/http` + `chi` router, `pgx` + `sqlc`. Internal packages cleanly separated: `ingest`, `store`, `query`, `stream` — so a later service split is a deployment change, not a refactor. No batteries framework.
- **Ingestion surfaces (v1)**:
  - Native **OTLP/HTTP receiver** implemented in the Go service (protobuf + JSON). Claude Code points `OTEL_EXPORTER_OTLP_ENDPOINT` at Argus. No OTel Collector container (users with a Collector can still forward, since we speak OTLP).
  - **Claude Code hooks** via native `"type": "http"` hook — POST straight to an Argus webhook endpoint.
  - **Skipped in v1**: OTel traces (beta/unstable), local JSONL transcripts (format explicitly unstable). Both become v2 enrichment behind the same event model.
- **Event model**: **Session → Turn (keyed by `prompt.id`) → Event** (api_request, tool_call, decision, lifecycle, …). Every event stores raw payload as `jsonb` + normalized columns (timestamps, model, tokens, cost, tool name, outcome). Agent-agnostic core with a `vendor` field; vendor attributes (`claude_code.*`) preserved in raw. OTel and hooks both normalize into this one stream, merged on `session.id` / `prompt.id` / `tool_use_id`.
- **Storage**: **PostgreSQL only**, partitioned event tables + rollup tables, behind a Go storage interface (ClickHouse = possible v2 swap). Retention: raw events 90 days, rollups forever — both configurable.
- **Cost**: agent-emitted cost is authoritative when present (`claude_code.cost.usage`, `api_request.cost_usd`); fallback = repo-maintained price table × tokens, marked "estimated" in the UI.
- **Real-time**: **SSE** (server → browser only).
- **API**: REST/JSON + SSE, **OpenAPI spec generating the TS client**.
- **Frontend**: Vue 3 + Vite + TypeScript, **Tailwind + shadcn-vue**, **ECharts**. Pinia for state.
- **Deployment**: docker-compose self-hosted (server + postgres), also runs fine locally. **No auth in v1**, but an auth-shaped middleware seam.

## Repo & process

- **Monorepo** `~/Labs/argus`: `/server` (Go), `/web` (Vue), `/deploy` (compose), `/docs`. GitHub, **public from day one**, **MIT** license.
- **CI from commit #1**: GitHub Actions — Go: `go test ./...` + golangci-lint; Web: vitest + eslint + type-check; compose build check. Table-driven Go tests; storage layer integration-tested against real Postgres.
- **Quality bar**: test culture is part of the deliverable (portfolio project).
- **Build orchestration**: Fable orchestrates; Opus agents produce spec/architecture/phase plans decomposed into tickets; Sonnet agents implement against them; owner reviews at **phase boundaries**: spec → scaffold+CI → ingestion → storage/query → UI explorer+analytics → live view → polish/README.
- **Owner identity for this project**: personal (not Ignimission work identity).
