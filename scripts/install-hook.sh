#!/usr/bin/env bash
# scripts/install-hook.sh (make install-hook / make uninstall-hook) — the ONE
# re-runnable command that syncs ALL Claude-side Argus config to the current
# port: the HTTP hooks (PostToolUse + SessionEnd + SessionStart ->
# /ingest/hook) AND the OTel env block (endpoint + the fixed telemetry keys)
# in a Claude Code settings.json. This is the fiddly bit the README used to
# ask users to hand-edit; the actual JSON surgery lives in
# scripts/argus_hook.py (stdlib json — safer than sed/jq for "preserve
# everything else exactly"), this wrapper only resolves the port and the
# settings path and reports what happened. Re-running after a port change
# (PORT= or deploy/.env's ARGUS_HTTP_PORT) moves every Argus entry to the new
# port with no old-port references and no duplicates — see argus_hook.py.
#
#   bash scripts/install-hook.sh install
#   bash scripts/install-hook.sh uninstall
#
# Env:
#   ARGUS_HTTP_PORT      port Argus listens on (default 8080). Must match the
#                        running stack — the Makefile already exports this
#                        (from PORT=, or deploy/.env, see Makefile) so `make
#                        install-hook` / `make uninstall-hook` get it right
#                        without repeating it.
#   ARGUS_SETTINGS_FILE  path to the settings.json to edit (default
#                        ~/.claude/settings.json). Override to target a
#                        throwaway copy — this is how the clean-state install
#                        verification exercises the merge/removal without
#                        touching a real Claude Code config.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

action="${1:-install}"
case "$action" in
  install|uninstall) ;;
  *)
    echo "usage: $0 <install|uninstall>" >&2
    exit 2
    ;;
esac

port="${ARGUS_HTTP_PORT:-8080}"
settings_file="${ARGUS_SETTINGS_FILE:-$HOME/.claude/settings.json}"
base_url="http://localhost:${port}"

if ! command -v python3 >/dev/null 2>&1; then
  echo "[install-hook] FAIL: python3 not found on PATH (needed for a safe JSON merge that preserves your other hooks)" >&2
  exit 1
fi

echo "[install-hook] ${action} (port=${port}, settings=${settings_file})" >&2
python3 "$repo_root/scripts/argus_hook.py" "$action" "$settings_file" "$base_url"
