#!/usr/bin/env python3
"""Self-test for scripts/argus_hook.py — the settings.json merge/removal.

stdlib unittest so CI needs nothing but python3:

    python3 scripts/argus_hook_test.py
"""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("argus_hook.py")
BASE_URL = "http://localhost:18080"

# The contract: the hook events Claude Code was observed to fire, hardcoded here
# rather than imported, so a silent edit to the script's WIRING table fails a test.
EVENTS = (
    "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
    "PostToolUseFailure", "PostToolBatch", "SubagentStart", "SubagentStop",
    "Stop", "SessionEnd", "InstructionsLoaded",
)

# A hook group Argus does not own, repeated in every case: the merge must
# leave it byte-identical no matter what else it does.
FOREIGN_GROUP = {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-gate.py"}]}


def run(action: str, path: Path, base_url: str = BASE_URL) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), action, str(path), base_url],
        capture_output=True, text=True, check=True,
    )


class ArgusHookTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "settings.json"

    def write(self, data: dict) -> None:
        self.path.write_text(json.dumps(data, indent=2) + "\n")

    def read(self) -> dict:
        return json.loads(self.path.read_text())

    def test_install_writes_per_event_urls_and_session_start_command(self) -> None:
        """Every wired event gets its own ?event= URL on the right port, transport and timeout."""
        expected_hooks = [
            ("SessionStart", "command", 2),
            ("UserPromptSubmit", "http", 2),
            ("PreToolUse", "http", 2),
            ("PostToolUse", "http", 5),
            ("PostToolUseFailure", "http", 5),
            ("PostToolBatch", "http", 5),
            ("SubagentStart", "http", 2),
            ("SubagentStop", "http", 1),
            ("Stop", "http", 1),
            ("SessionEnd", "http", 1),
            ("InstructionsLoaded", "http", 2),
        ]

        run("install", self.path)
        hooks = self.read()["hooks"]

        for event, expected_transport, expected_timeout in expected_hooks:
            self.assertIn(event, hooks, f"event {event} not wired")
            group = hooks[event][0]
            hook = group["hooks"][0]
            self.assertEqual(hook["type"], expected_transport, f"{event}: expected transport {expected_transport}, got {hook.get('type')}")
            self.assertEqual(hook["timeout"], expected_timeout, f"{event}: expected timeout {expected_timeout}, got {hook.get('timeout')}")

            if expected_transport == "command":
                self.assertIn(f"{BASE_URL}/ingest/hook?event={event}", hook["command"], f"{event} command URL mismatch")
            else:
                self.assertEqual(hook["url"], f"{BASE_URL}/ingest/hook?event={event}", f"{event} URL mismatch")

    def test_install_is_idempotent(self) -> None:
        run("install", self.path)
        first = self.read()
        second_run = run("install", self.path)
        self.assertIn("no change needed", second_run.stdout)
        self.assertEqual(first, self.read())

    def test_reinstall_on_new_port_replaces_without_duplicating(self) -> None:
        """A port change moves every wired event, leaving no duplicate and no stale port."""
        run("install", self.path)
        run("install", self.path, "http://localhost:19999")
        hooks = self.read()["hooks"]
        for event in EVENTS:
            self.assertEqual(len(hooks[event]), 1, f"{event} duplicated on port change")
        self.assertEqual(self.read()["env"]["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://localhost:19999")
        self.assertNotIn("18080", json.dumps(hooks))

    def test_install_replaces_the_pre_event_param_http_shape(self) -> None:
        """Upgrading from the old wiring must replace it, not sit beside it."""
        self.write({"hooks": {
            "SessionStart": [{"hooks": [{"type": "http", "url": f"{BASE_URL}/ingest/hook", "timeout": 2}]}],
            "PostToolUse": [{"hooks": [{"type": "http", "url": f"{BASE_URL}/ingest/hook", "timeout": 5}]}],
            "SessionEnd": [{"hooks": [{"type": "http", "url": f"{BASE_URL}/ingest/hook", "timeout": 1}]}],
        }})
        run("install", self.path)
        hooks = self.read()["hooks"]
        for event in ("SessionStart", "PostToolUse", "SessionEnd"):
            self.assertEqual(len(hooks[event]), 1, f"{event} kept a stale pre-upgrade group")
        self.assertEqual(hooks["SessionStart"][0]["hooks"][0]["type"], "command")

    def test_install_preserves_foreign_hooks_and_keys(self) -> None:
        """Foreign groups survive, first, on wired events — on ignored ones the check would be vacuous."""
        foreign_on = ["PreToolUse", "PostToolUse", "Stop", "Notification"]
        self.write({
            "model": "opus",
            "hooks": {e: [FOREIGN_GROUP] for e in foreign_on},
            "env": {"MY_OWN": "1"},
        })
        run("install", self.path)
        data = self.read()
        self.assertEqual(data["model"], "opus")
        self.assertEqual(data["env"]["MY_OWN"], "1")

        for event in foreign_on:
            groups = data["hooks"][event]
            self.assertEqual(groups[0], FOREIGN_GROUP, f"{event}: foreign group altered or displaced")
            # Notification is not in WIRING, so it must keep exactly what it had.
            expected = 2 if event in EVENTS else 1
            self.assertEqual(len(groups), expected, f"{event}: unexpected group count")

    def test_uninstall_removes_only_argus_entries(self) -> None:
        """Uninstall spares foreign groups, including on newly wired events such as Stop."""
        self.write({
            "model": "opus",
            "hooks": {"PreToolUse": [FOREIGN_GROUP], "PostToolUse": [FOREIGN_GROUP], "Stop": [FOREIGN_GROUP]},
            "env": {"MY_OWN": "1"},
        })
        run("install", self.path)
        run("uninstall", self.path)
        data = self.read()
        self.assertEqual(data["model"], "opus")
        self.assertEqual(data["hooks"], {"PreToolUse": [FOREIGN_GROUP], "PostToolUse": [FOREIGN_GROUP], "Stop": [FOREIGN_GROUP]})
        self.assertEqual(data["env"], {"MY_OWN": "1"})

    def test_uninstall_removes_the_pre_event_param_http_shape(self) -> None:
        self.write({"hooks": {
            "SessionEnd": [{"hooks": [{"type": "http", "url": f"{BASE_URL}/ingest/hook", "timeout": 1}]}],
        }})
        run("uninstall", self.path)
        self.assertNotIn("hooks", self.read())

    def test_uninstall_on_clean_settings_is_a_no_op(self) -> None:
        self.write({"hooks": {"PreToolUse": [FOREIGN_GROUP]}})
        before = self.read()
        result = run("uninstall", self.path)
        self.assertIn("no change needed", result.stdout)
        self.assertEqual(before, self.read())

    def test_install_backs_up_before_editing(self) -> None:
        self.write({"model": "opus"})
        run("install", self.path)
        backup = self.path.with_name(self.path.name + ".bak")
        self.assertEqual(json.loads(backup.read_text()), {"model": "opus"})

    def test_session_start_command_never_fails_the_session(self) -> None:
        run("install", self.path)
        command = self.read()["hooks"]["SessionStart"][0]["hooks"][0]["command"]
        self.assertTrue(command.endswith("|| true"), command)
        # stdout of a SessionStart hook is injected into the session context.
        self.assertIn("-o /dev/null", command)

    def test_session_start_command_posts_the_payload_it_reads_on_stdin(self) -> None:
        """Run the real command with curl against a throwaway HTTP listener."""
        import http.server
        import threading

        received = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802 (http.server's API)
                body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                received.append((self.path, body.decode()))
                self.send_response(202)
                self.end_headers()

            def log_message(self, *args: object) -> None:
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        threading.Thread(target=server.serve_forever, daemon=True).start()

        port = server.server_address[1]
        run("install", self.path, f"http://127.0.0.1:{port}")
        command = self.read()["hooks"]["SessionStart"][0]["hooks"][0]["command"]

        payload = '{"session_id":"s1","cwd":"/home/u/proj","hook_event_name":"SessionStart"}'
        done = subprocess.run(["bash", "-c", command], input=payload, capture_output=True, text=True)
        self.assertEqual(done.returncode, 0)
        self.assertEqual(done.stdout, "", "stdout would be injected into the session context")

        self.assertEqual(len(received), 1)
        path, body = received[0]
        self.assertEqual(path, "/ingest/hook?event=SessionStart")
        self.assertEqual(body, payload)

    def test_session_start_command_is_a_no_op_when_argus_is_down(self) -> None:
        run("install", self.path, "http://127.0.0.1:9")  # discard port: refuses connections
        command = self.read()["hooks"]["SessionStart"][0]["hooks"][0]["command"]
        done = subprocess.run(["bash", "-c", command], input="{}", capture_output=True, text=True)
        self.assertEqual(done.returncode, 0, "a down stack must never fail a session start")

    def test_teardown_trio_has_timeout_one(self) -> None:
        """Teardown hooks share Claude Code's hard 1.5s budget, so each gets 1s."""
        run("install", self.path)
        hooks = self.read()["hooks"]
        for event in ("SessionEnd", "Stop", "SubagentStop"):
            timeout = hooks[event][0]["hooks"][0]["timeout"]
            self.assertEqual(timeout, 1, f"{event} teardown hook must have timeout 1, got {timeout}")

    def test_exactly_one_command_hook_is_session_start(self) -> None:
        """Flipping any other event to a curl command must be a deliberate, reviewed change."""
        run("install", self.path)
        hooks = self.read()["hooks"]
        command_hooks = []
        for event in EVENTS:
            hook_type = hooks[event][0]["hooks"][0]["type"]
            if hook_type == "command":
                command_hooks.append(event)
        self.assertEqual(command_hooks, ["SessionStart"], f"Expected only SessionStart as command hook, got {command_hooks}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
