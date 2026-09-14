#!/usr/bin/env bash
# scripts/check-port-sync.sh (called by `make up`) — warn, non-fatally, when
# ~/.claude/settings.json's Argus OTel endpoint still points at a different
# port than the stack `up` just (re)started on. Never edits settings.json —
# the fix is `make install-hook`, which is what the warning tells you to run.
#
# Env:
#   ARGUS_HTTP_PORT      the port the stack was just started on (the Makefile
#                        already exports this from PORT=/deploy/.env/8080).
#   ARGUS_SETTINGS_FILE  path to the settings.json to check (default
#                        ~/.claude/settings.json). Override to point at a
#                        throwaway copy for testing.
set -euo pipefail

port="${ARGUS_HTTP_PORT:-8080}"
settings_file="${ARGUS_SETTINGS_FILE:-$HOME/.claude/settings.json}"

[[ -f "$settings_file" ]] || exit 0

configured_port="$(
  grep -oE '"OTEL_EXPORTER_OTLP_ENDPOINT"[[:space:]]*:[[:space:]]*"http://[^"]*:[0-9]+"' "$settings_file" 2>/dev/null \
    | grep -oE '[0-9]+"$' \
    | tr -d '"' \
    || true
)"

[[ -n "$configured_port" ]] || exit 0
[[ "$configured_port" != "$port" ]] || exit 0

echo "Claude wiring points at :${configured_port} but the stack is on :${port} — run 'make install-hook' to re-sync"
