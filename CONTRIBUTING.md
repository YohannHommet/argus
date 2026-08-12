# Contributing to Argus

> This project is pre-alpha; conventions below will grow as the codebase does. See
> `docs/SPEC.md` and `docs/PLAN.md` before proposing changes — they are the source of truth for
> architecture and sequencing decisions.

## Workflow

1. Check `docs/PLAN.md` for the ticket that covers your change; ticket file lists are normative —
   stay inside them.
2. Branch from `main`; do not commit directly to it.
3. Run `make ci` locally before opening a pull request.
4. Keep commits small and scoped, with messages that explain *why*, not just *what*.

## Code style

- Go: `gofmt` + `golangci-lint` (`.golangci.yml`), enforced by `make lint` and CI.
- Web: ESLint + Prettier via `pnpm lint`, enforced by CI.
- Line endings are always LF — see `.gitattributes` / `.editorconfig`. Never introduce CRLF.

## Tests

- Go changes need `go test` coverage; see per-package coverage floors in `docs/SPEC.md` §8.3.
- Web changes need `pnpm unit` coverage.
- `make test` runs both suites locally.

## Reporting issues

Open a GitHub issue with reproduction steps. Security issues: do not open a public issue —
contact the maintainer directly.
