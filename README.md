# Argus

> **status: pre-alpha** — Phase 1 (scaffold) is in progress. Nothing here works yet.

Argus is an observability backend + UI for Claude Code telemetry: it ingests OTLP logs/metrics
and hook events, stores them in Postgres, and serves an analytics/session-explorer UI plus a
live view — see `docs/SPEC.md` for the full design and `docs/PLAN.md` for the build sequence.

## Quickstart (placeholder)

This section is a placeholder outline. The real, verified quickstart lands in a later phase
(P6-01) once `docker compose up` actually serves the app end to end.

```bash
# 1. Clone
git clone git@github.com:YohannHommet/argus.git && cd argus

# 2. Start the stack (Postgres + argusd), once deploy/docker-compose.yml exists
make compose-up

# 3. Point Claude Code's OTLP exporter at argusd (exact env vars TBD, see docs/SPEC.md §8.2)
# 4. Open the UI
#    http://localhost:8080/
```

See `make help` for the full list of developer targets (`dev`, `build`, `test`, `lint`, `ci`,
`gen`, `migrate`, `sim`, `compose-up`, `compose-smoke`).

## Documentation

- `docs/SPEC.md` — the spec (architecture, data model, API, deploy).
- `docs/PLAN.md` — phased implementation plan and ticket breakdown.
- `CONTRIBUTING.md` — contribution guidelines.

## License

MIT — see `LICENSE`.
