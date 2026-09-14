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
        run("install", self.path)
        hooks = self.read()["hooks"]

        http_hook = hooks["PostToolUse"][0]["hooks"][0]
        self.assertEqual(http_hook["type"], "http")
        self.assertEqual(http_hook["url"], f"{BASE_URL}/ingest/hook?event=PostToolUse")
        self.assertEqual(http_hook["timeout"], 5)

        self.assertEqual(hooks["SessionEnd"][0]["hooks"][0]["url"], f"{BASE_URL}/ingest/hook?event=SessionEnd")
        self.assertEqual(hooks["SessionEnd"][0]["hooks"][0]["timeout"], 1)

        # SessionStart is a command hook: Claude Code never dispatches an http one.
        start_hook = hooks["SessionStart"][0]["hooks"][0]
        self.assertEqual(start_hook["type"], "command")
        self.assertIn(f"{BASE_URL}/ingest/hook?event=SessionStart", start_hook["command"])
        self.assertEqual(start_hook["timeout"], 2)

    def test_install_is_idempotent(self) -> None:
        run("install", self.path)
        first = self.read()
        second_run = run("install", self.path)
        self.assertIn("no change needed", second_run.stdout)
        self.assertEqual(first, self.read())

    def test_reinstall_on_new_port_replaces_without_duplicating(self) -> None:
        run("install", self.path)
        run("install", self.path, "http://localhost:19999")
        hooks = self.read()["hooks"]
        for event in ("SessionStart", "PostToolUse", "SessionEnd"):
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
        self.write({
            "model": "opus",
            "hooks": {"PreToolUse": [FOREIGN_GROUP], "PostToolUse": [FOREIGN_GROUP]},
            "env": {"MY_OWN": "1"},
        })
        run("install", self.path)
        data = self.read()
        self.assertEqual(data["model"], "opus")
        self.assertEqual(data["hooks"]["PreToolUse"], [FOREIGN_GROUP])
        self.assertEqual(data["hooks"]["PostToolUse"][0], FOREIGN_GROUP)
        self.assertEqual(len(data["hooks"]["PostToolUse"]), 2)
        self.assertEqual(data["env"]["MY_OWN"], "1")

    def test_uninstall_removes_only_argus_entries(self) -> None:
        self.write({
            "model": "opus",
            "hooks": {"PreToolUse": [FOREIGN_GROUP], "PostToolUse": [FOREIGN_GROUP]},
            "env": {"MY_OWN": "1"},
        })
        run("install", self.path)
        run("uninstall", self.path)
        data = self.read()
        self.assertEqual(data["model"], "opus")
        self.assertEqual(data["hooks"], {"PreToolUse": [FOREIGN_GROUP], "PostToolUse": [FOREIGN_GROUP]})
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


if __name__ == "__main__":
    unittest.main(verbosity=2)
