#!/usr/bin/env bash
# scripts/setup.sh (make setup) — guided onboarding for a fresh clone: checks
# docker, creates deploy/.env from deploy/.env.example if missing, brings the
# stack up (`make up`), then syncs Claude Code's wiring (OTel env block +
# hooks) to the port actually in use via `make install-hook`
# (scripts/install-hook.sh).
#
# Idempotent and safe to re-run: never overwrites an existing deploy/.env,
# `make up` no-ops onto an already-running stack, and install-hook.sh merges
# rather than clobbers (backs up settings.json first, touches only Argus's
# own entries).
#
# Env:
#   ARGUS_HTTP_PORT / PORT   host port (default 8080). Invoked via
#                            `make setup`, ARGUS_HTTP_PORT is already resolved
#                            by the Makefile (CLI PORT=, else deploy/.env's
#                            ARGUS_HTTP_PORT, else 8080) and exported — this
#                            script does not need its own copy of that logic.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

log() { printf '[setup] %s\n' "$*" >&2; }

port="${ARGUS_HTTP_PORT:-${PORT:-8080}}"
base_url="http://localhost:${port}"

# --- 1. docker present and reachable -----------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  log "FAIL: docker not found on PATH. Install Docker (https://docs.docker.com/get-docker/), then re-run: make setup"
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  log "FAIL: 'docker compose' (the v2 plugin) not found. Install/update Docker so 'docker compose version' works, then re-run: make setup"
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  log "FAIL: docker is installed but its daemon isn't reachable (is Docker Desktop / the docker service running?). Start it, then re-run: make setup"
  exit 1
fi
log "docker OK ($(docker --version))"

# --- 2. deploy/.env from the example -----------------------------------------
if [[ -f deploy/.env ]]; then
  log "deploy/.env already exists — leaving it as-is (edit it directly to customize, then 'make up' again)"
else
  cp deploy/.env.example deploy/.env
  # Bake in the resolved port so a later plain `make up` (no PORT=) still
  # matches this run: deploy/.env becomes the single source of truth from
  # here on, not something you also have to remember to pass on the CLI.
  sed -i.bak "s/^ARGUS_HTTP_PORT=.*/ARGUS_HTTP_PORT=${port}/" deploy/.env
  rm -f deploy/.env.bak
  log "created deploy/.env from deploy/.env.example (ARGUS_HTTP_PORT=${port})"
fi

# --- 3. start the stack -------------------------------------------------------
log "starting the stack: make up PORT=${port} (builds on first run — this can take a minute)"
make up PORT="${port}"

# --- 4. install the hook + OTel env into Claude Code's settings.json --------
# make install-hook (scripts/install-hook.sh -> scripts/argus_hook.py) writes
# both the OTel env block and the PostToolUse/SessionEnd/SessionStart hooks
# into ~/.claude/settings.json for the port just started on — no manual
# export, no hand-editing. Re-run it any time the port changes.
make install-hook PORT="${port}"

cat <<WIRING

--------------------------------------------------------------------------
Claude Code wiring merged into ~/.claude/settings.json for ${base_url}:
env (OTel export) + hooks (SessionStart, PostToolUse, SessionEnd -> /ingest/hook?event=…).
Changed the port? Re-run: make install-hook
--------------------------------------------------------------------------
WIRING

cat <<DONE

Argus is up at ${base_url} — run a Claude Code session, then open that URL.
No real traffic yet? 'make demo' seeds sample data.
Customize retention/rollup/auth/log level by editing deploy/.env, then 'make up' again.
DONE
