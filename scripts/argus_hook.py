#!/usr/bin/env python3
"""Merge or remove Argus's HTTP hook entries in a Claude Code settings.json.

Invoked by scripts/install-hook.sh (`make install-hook` / `make
uninstall-hook`) — never run directly by a user. stdlib json only, so every
existing key and every other hook entry round-trips untouched except the
PostToolUse/SessionEnd arrays this script edits.

Argus's own entries are identified by URL, not by position: any hook group
whose sole hook is `{"type": "http", "url": "http://<host>/ingest/hook"}`.
That makes install idempotent (re-running with the same port is a no-op,
re-running with a different port replaces the old entry instead of
duplicating it) and makes uninstall precise (it removes only entries matching
that shape — every unrelated hook, including ones with the same event name
and a different matcher/url, is left exactly as it was).
"""
import json
import re
import sys
from pathlib import Path

EVENTS = ("PostToolUse", "SessionEnd")
TIMEOUTS = {"PostToolUse": 5, "SessionEnd": 1}
URL_RE = re.compile(r"^https?://[^/\s]+/ingest/hook$")


def is_argus_group(group: object) -> bool:
    """True if `group` is a hook group this script would itself write."""
    if not isinstance(group, dict):
        return False
    hooks = group.get("hooks")
    if not isinstance(hooks, list) or len(hooks) != 1:
        return False
    h = hooks[0]
    return (
        isinstance(h, dict)
        and h.get("type") == "http"
        and isinstance(h.get("url"), str)
        and bool(URL_RE.match(h["url"]))
    )


def argus_group(url: str, event: str) -> dict:
    return {"hooks": [{"type": "http", "url": url, "timeout": TIMEOUTS[event]}]}


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

    changed = False

    if action == "install":
        url = f"{base_url}/ingest/hook"
        hooks = dict(hooks) if hooks else {}
        for event in EVENTS:
            existing = hooks.get(event, [])
            if not isinstance(existing, list):
                print(f'error: {path}: "hooks.{event}" is not a JSON array', file=sys.stderr)
                return 1
            kept = [g for g in existing if not is_argus_group(g)]
            new_list = kept + [argus_group(url, event)]
            if new_list != existing:
                changed = True
            hooks[event] = new_list
        data["hooks"] = hooks

    elif action == "uninstall":
        if not hooks:
            print("uninstall: no hooks configured — nothing to remove")
            return 0
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
