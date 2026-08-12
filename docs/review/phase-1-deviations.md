# Phase 1 accepted deviations

All entries below deviate from SPEC.md/PLAN.md as originally written; SPEC.md and PLAN.md have
been amended to match. Owner accepted all on 2026-08-12.

| ID | Deviation | Rationale | Status |
|---|---|---|---|
| D-1 | Sidebar has five nav destinations, not six | The sixth §6.2 route, `/sessions/:id`, is reached from the session list, not the nav — spec miscount | accepted 2026-08-12 |
| D-2 | `typescript@6.0.3` pinned instead of 7.0.2 | TS 7's native rewrite breaks `vue-tsc` and `@typescript-eslint` as of 2026-08; revisit later | accepted 2026-08-12 |
| D-3 | `koanf` submodules left unpinned | — | accepted 2026-08-12 |
| D-4 | goose bridged via `stdlib.OpenDBFromPool` | `goose.NewProvider` takes a `*sql.DB`, not a pgx v5 pool directly | accepted 2026-08-12 |
| D-5 | `/readyz` `"migrations":"current"` is asserted, not live-checked | `Maintenance` gains a status method in Phase 2 alongside the queue-saturation readiness condition | accepted 2026-08-12 |
| D-6 | postgres:18 volume mounts `/var/lib/postgresql` (parent dir) | `postgres:18-alpine` moved `PGDATA` to `/var/lib/postgresql/18/docker` and declares `VOLUME /var/lib/postgresql` | accepted 2026-08-12 |
| D-7 | Dockerfile splits `ENTRYPOINT`/`CMD` | — | accepted 2026-08-12 |
| D-8 | compose port is `${ARGUS_HTTP_PORT:-8080}` | — | accepted 2026-08-12 |
| D-9 | No corepack in the `node:25-alpine` build stage | — | accepted 2026-08-12 |
| D-10 | `go.mod` pins `go 1.25.7` (not `1.25`) | Pinned `goose` v3.27.3 requires ≥1.25.7; spec was self-inconsistent | accepted 2026-08-12 |
| D-11 | `exhaustive` linter cannot be positively scoped to `Kind` | Go's `regexp` (RE2) has no negative lookahead, so the pattern can't exclude non-`Kind` types | accepted 2026-08-12 |
| D-12 | `nolintlint` added to golangci config | — | accepted 2026-08-12 |

## Accepted risk (not a deviation, carried forward)

chi's deprecated `RealIP` middleware carries a documented `nolint`. Revisit before any untrusted
proxy sits in front of the service.
