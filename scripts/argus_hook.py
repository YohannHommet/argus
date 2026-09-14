#!/usr/bin/env python3
"""Merge or remove Argus's Claude Code wiring in a Claude Code settings.json:
the SessionStart/PostToolUse/SessionEnd hooks and the OTel env block.

Invoked by scripts/install-hook.sh (`make install-hook` / `make
uninstall-hook`) — never run directly by a user. stdlib json only, so every
existing key and every other hook/env entry round-trips untouched except the
hooks arrays and env keys this script edits.

Each hook posts to its own URL (`/ingest/hook?event=<HookEvent>`) so the
receiver classifies the event from the transport, not only from the body.

SessionStart is wired as a `command` hook running curl, not as an `http` hook
like the other two: Claude Code registers a `type: http` SessionStart hook but
never dispatches it (verified on 2.1.260/2.1.270 — a capture server saw
PostToolUse and SessionEnd arrive while SessionStart never fired, and a
`command` hook on the same event fired normally). Without this, Argus never
sees a session start, so every session stays status=unknown with no
started_at.

Argus's own hook entries are identified by URL, not by position: any hook
group whose sole hook posts to `http://<host>/ingest/hook` — as an `http`
hook's url, or inside a `command` hook's command string — with or without the
`?event=` query, so re-running after an upgrade replaces the old entries
instead of duplicating them. Argus's own env keys are identified by name: the
fixed OTEL_*/CLAUDE_CODE_ENABLE_TELEMETRY set in OTEL_KEYS below. That makes
install idempotent (re-running with the same port is a no-op, re-running with
a different port replaces the old hook URLs/endpoint instead of duplicating
them) and makes uninstall precise (it removes only entries matching that
shape/name — every unrelated hook or env key, including ones with the same
event name and a different matcher/url, is left exactly as it was).
"""
import json
import re
import sys
from pathlib import Path

EVENTS = ("SessionStart", "PostToolUse", "SessionEnd")
# SessionEnd hooks share one hard 1.5s budget across all of them, hence 1.
TIMEOUTS = {"SessionStart": 2, "PostToolUse": 5, "SessionEnd": 1}
# Events Claude Code does not deliver over an `http` hook (see module docstring).
COMMAND_EVENTS = frozenset({"SessionStart"})

# Matches an Argus ingest URL wherever it appears: an `http` hook's url field,
# or somewhere inside a `command` hook's command string.
URL_RE = re.compile(r"https?://[^/\s'\"]+/ingest/hook(\?event=[A-Za-z]+)?(?=['\"\s]|$)")

# Argus-owned OTel env keys with fixed values — everything telemetry needs
# except the endpoint, which carries the port and is computed per-install.
OTEL_KEYS = {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
    "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
    "OTEL_LOG_TOOL_DETAILS": "1",
}
OTEL_ENDPOINT_KEY = "OTEL_EXPORTER_OTLP_ENDPOINT"


def hook_url(base_url: str, event: str) -> str:
    return f"{base_url}/ingest/hook?event={event}"


def is_argus_group(group: object) -> bool:
    """True if `group` is a hook group this script would itself write."""
    if not isinstance(group, dict):
        return False
    hooks = group.get("hooks")
    if not isinstance(hooks, list) or len(hooks) != 1:
        return False
    h = hooks[0]
    if not isinstance(h, dict):
        return False
    if h.get("type") == "http":
        return isinstance(h.get("url"), str) and bool(URL_RE.fullmatch(h["url"]))
    if h.get("type") == "command":
        return isinstance(h.get("command"), str) and bool(URL_RE.search(h["command"]))
    return False


def argus_group(base_url: str, event: str) -> dict:
    timeout = TIMEOUTS[event]
    url = hook_url(base_url, event)
    if event in COMMAND_EVENTS:
        # `|| true` on purpose: observability must never fail or delay a
        # session start, so a stack that is down stays a silent no-op.
        command = (
            f"curl -sS -m {timeout} -X POST -H 'Content-Type: application/json' "
            f"--data-binary @- -o /dev/null '{url}' || true"
        )
        return {"hooks": [{"type": "command", "command": command, "timeout": timeout}]}
    return {"hooks": [{"type": "http", "url": url, "timeout": timeout}]}


def argus_env(base_url: str) -> dict:
    """The full Argus-owned env block (fixed keys + the port-carrying endpoint)."""
    env = dict(OTEL_KEYS)
    env[OTEL_ENDPOINT_KEY] = base_url
    return env


def main() -> int:
    if len(sys.argv) != 4:
        print(f"usage: {sys.argv[0]} <install|uninstall> <settings.json path> <base_url>", file=sys.stderr)
        return 2
    action, path_str, base_url = sys.argv[1], sys.argv[2], sys.argv[3]
    path = Path(path_str)

    existed = path.exists()
    raw = path.read_text() if existed else ""
    try:
        data = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError as e:
        print(f"error: {path} is not valid JSON: {e}", file=sys.stderr)
        return 1

    if not isinstance(data, dict):
        print(f"error: {path} does not contain a JSON object at the top level", file=sys.stderr)
        return 1

    hooks = data.get("hooks")
    if hooks is not None and not isinstance(hooks, dict):
        print(f'error: {path}: "hooks" is not a JSON object', file=sys.stderr)
        return 1

    env = data.get("env")
    if env is not None and not isinstance(env, dict):
        print(f'error: {path}: "env" is not a JSON object', file=sys.stderr)
        return 1

    changed = False

    if action == "install":
        hooks = dict(hooks) if hooks else {}
        for event in EVENTS:
            existing = hooks.get(event, [])
            if not isinstance(existing, list):
                print(f'error: {path}: "hooks.{event}" is not a JSON array', file=sys.stderr)
                return 1
            kept = [g for g in existing if not is_argus_group(g)]
            new_list = kept + [argus_group(base_url, event)]
            if new_list != existing:
                changed = True
            hooks[event] = new_list
        data["hooks"] = hooks

        env = dict(env) if env else {}
        for key, value in argus_env(base_url).items():
            if env.get(key) != value:
                changed = True
            env[key] = value
        data["env"] = env

    elif action == "uninstall":
        if hooks:
            for event in EVENTS:
                existing = hooks.get(event)
                if not isinstance(existing, list):
                    continue
                kept = [g for g in existing if not is_argus_group(g)]
                if len(kept) != len(existing):
                    changed = True
                if kept:
                    hooks[event] = kept
                else:
                    hooks.pop(event, None)
            if hooks:
                data["hooks"] = hooks
            else:
                data.pop("hooks", None)

        if env:
            managed_keys = set(OTEL_KEYS) | {OTEL_ENDPOINT_KEY}
            for key in managed_keys:
                if key in env:
                    env.pop(key)
                    changed = True
            if env:
                data["env"] = env
            else:
                data.pop("env", None)

    else:
        print(f"error: unknown action {action!r} (want install or uninstall)", file=sys.stderr)
        return 2

    if not changed:
        print(f"{action}: no change needed ({path})")
        return 0

    if existed:
        path.with_name(path.name + ".bak").write_text(raw)

    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + "\n")
    backup_note = f" (backup: {path}.bak)" if existed else ""
    print(f"{action}: wrote {path}{backup_note}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
