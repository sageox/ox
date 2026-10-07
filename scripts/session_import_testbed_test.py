"""Tests for session_import_testbed.py: capture cleans real sessions into a
publishable fixture, and create turns a fixture into a repo ox can import from.

Run: cd scripts && python3 -m unittest -v session_import_testbed_test.py
"""

import io
import json
import os
import subprocess
import tempfile
import time
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import session_import_testbed as tb

CLAUDE_ID = "5b1d7e3a-2c4f-4e8a-9b6d-0f1e2d3c4b5a"
CODEX_ID = "01a0ee54-4491-70e1-b771-3c5d2e8f9a10"
CODEX_FILE = "rollout-2026-10-01T10-00-00-%s.jsonl" % CODEX_ID


def git(cwd, *args):
    subprocess.run(["git", "-C", str(cwd), "-c", "user.name=t", "-c", "user.email=t@example.com",
                    "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"] + list(args),
                   check=True, capture_output=True)


def jsonl(path, records, quiet=True):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(json.dumps(r) for r in records) + "\n", encoding="utf-8")
    if quiet:
        past = time.time() - 3600
        os.utime(path, (past, past))


class Testbed(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp()).resolve()
        self.home = self.tmp / "home"
        self.source = self.home / "work" / "game"
        self.source.mkdir(parents=True)
        (self.source / "app.py").write_text("print('hi')\n")
        (self.source / "pkg").mkdir()
        (self.source / "pkg" / ".gitignore").write_text("*.log\n")
        (self.source / ".sageox").mkdir()
        (self.source / ".sageox" / "config.json").write_text("{}")
        (self.source / "AGENTS.md").write_text("ox managed")
        git(self.source, "init", "-q", "-b", "main")
        git(self.source, "add", "-A")
        git(self.source, "commit", "-q", "-m", "init")
        (self.source / "__pycache__").mkdir()
        (self.source / "__pycache__" / "app.cpython-39.pyc").write_bytes(b"\0")
        self.claude = self.tmp / "claude-config"
        self.codex = self.tmp / "codex-home"
        env = {"HOME": str(self.home), "CLAUDE_CONFIG_DIR": str(self.claude), "CODEX_HOME": str(self.codex), "USER": "dreal"}
        patcher = mock.patch.dict(os.environ, env)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.write_sessions()

    def write_sessions(self, email="dana@corp.example"):
        cwd = str(self.source)
        big = "x" * (tb.PAYLOAD_CAP * 3)
        prime = "<ox-prime><team-context>Acme internal roadmap</team-context></ox-prime>"
        jsonl(self.claude / "projects" / "-home-work-game" / (CLAUDE_ID + ".jsonl"), [
            {"type": "attachment", "attachment": {"type": "hook_success", "content": prime}, "sessionId": CLAUDE_ID, "cwd": cwd},
            {"type": "user", "sessionId": CLAUDE_ID, "cwd": cwd, "timestamp": "2026-10-01T10:00:00Z", "toolUseResult": {"stdout": big},
             "wireToolInputs": [big], "serverClassifierContext": "vendor",
             "message": {"role": "user", "content": "I'm Dana Real (%s); my shell says\ndreal@laptop %% ls %s/app.py" % (email, cwd)}},
            {"type": "assistant", "sessionId": CLAUDE_ID, "cwd": cwd, "timestamp": "2026-10-01T10:01:00Z",
             "message": {"role": "assistant", "usage": {"input_tokens": 9}, "content": [
                 {"type": "thinking", "thinking": "private reasoning", "signature": "sig"},
                 {"type": "text", "text": "Works on macOS too."},
                 {"type": "tool_use", "id": "t1", "name": "Write", "input": {"file_path": cwd + "/app.py", "content": big}}]}},
            {"type": "user", "sessionId": CLAUDE_ID, "cwd": cwd, "timestamp": "2026-10-01T10:02:00Z",
             "message": {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": prime + big}]}},
        ])
        jsonl(self.codex / "sessions" / "2026" / "10" / "01" / CODEX_FILE, [
            {"type": "session_meta", "timestamp": "2026-10-01T10:00:00Z", "payload": {"id": CODEX_ID, "cwd": cwd, "source": "vscode",
                                                                                 "base_instructions": {"text": "vendor system prompt"}}},
            {"type": "turn_context", "timestamp": "2026-10-01T10:00:01Z", "payload": {"cwd": cwd, "model": "m", "user_instructions": "AGENTS.md text"}},
            {"type": "response_item", "timestamp": "2026-10-01T10:00:02Z", "payload": {"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "vendor"}]}},
            {"type": "response_item", "timestamp": "2026-10-01T10:00:03Z", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Fix the timer"}]}},
            {"type": "response_item", "timestamp": "2026-10-01T10:00:04Z", "payload": {"type": "reasoning", "encrypted_content": "opaque"}},
            {"type": "event_msg", "timestamp": "2026-10-01T10:00:05Z", "payload": {"type": "task_started"}},
            {"type": "response_item", "timestamp": "2026-10-01T10:00:06Z", "payload": {"type": "custom_tool_call_output", "call_id": "c1",
                                                                                     "output": "<ox-prime>Acme internal roadmap" + big}},
            {"type": "event_msg", "timestamp": "2026-10-01T10:00:07Z", "payload": {"type": "token_count", "info": {}}},
            {"type": "response_item", "timestamp": "2026-10-01T10:00:08Z", "payload": {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Done"}]}},
            {"type": "event_msg", "timestamp": "2026-10-01T10:00:09Z", "payload": {"type": "task_complete"}},
        ])
        jsonl(self.codex / "sessions" / "2026" / "10" / "01" / ("rollout-2026-10-01T11-00-00-%s.jsonl" % ("0" * 8 + CODEX_ID[8:])), [
            {"type": "session_meta", "timestamp": "2026-10-01T11:00:00Z", "payload": {"id": "x", "cwd": str(self.home / "elsewhere")}},
        ])

    def run_tb(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = tb.main(list(argv))
        return code, out.getvalue() + err.getvalue()

    def capture(self, *extra):
        out = self.tmp / "fixture"
        code, text = self.run_tb("capture", str(self.source), "--out", str(out), "--identity", "Dana Real",
                                 "--identity", "dana@corp.example", *extra)
        return code, text, out

    # Failure prevented: a published fixture carrying its author's identity,
    # vendor-internal data, or team context, or too big for the repo.
    def test_capture_keeps_what_ox_reads_and_removes_the_rest(self):
        code, text, out = self.capture()
        self.assertEqual(0, code, text)
        claude = (out / "claude" / (CLAUDE_ID + ".jsonl")).read_text()
        codex = (out / "codex" / CODEX_FILE).read_text()
        both = claude + codex
        for gone in ("Dana Real", "dana@corp.example", "dreal", "Acme internal roadmap", "private reasoning",
                     "vendor system prompt", "opaque", "toolUseResult", "wireToolInputs", "serverClassifierContext",
                     "usage", "hook_success", "AGENTS.md text", "token_count", str(self.home)):
            self.assertNotIn(gone, both, gone)
        self.assertIn("__REPO__/app.py", claude)
        self.assertIn('"cwd":"__REPO__"', codex)
        self.assertIn("Works on macOS too.", claude, "a whole-word identity rule must not touch other words")
        self.assertIn("devon@example.com", claude)
        self.assertIn("<ox-prime>[removed from fixture]</ox-prime>", claude)
        self.assertIn("<ox-prime>[removed from fixture]</ox-prime>", codex, "a block with no closing tag is still blanked")
        self.assertIn("bytes trimmed from fixture", claude)
        self.assertIn("task_complete", codex, "turn boundaries stay: an open turn would read as in progress")
        self.assertFalse(list((out / "codex").glob("*00000000*")), "another repo's session is not captured")
        self.assertEqual("print('hi')\n", (out / "code" / "app.py").read_text())
        self.assertTrue((out / "code" / "pkg" / "dot-gitignore").exists(), "a nested .gitignore must not hide fixture files")
        for absent in ("AGENTS.md", ".sageox", "__pycache__"):
            self.assertFalse((out / "code" / absent).exists(), absent)
        self.assertIn("devon@example.com", (out / "PROVENANCE.md").read_text())

    # Failure prevented: an address nobody listed reaching a public repo.
    def test_capture_refuses_an_unlisted_email(self):
        self.write_sessions(email="pat@corp.example")
        code, text, out = self.capture()
        self.assertEqual(1, code)
        self.assertIn("pat@corp.example", text)
        self.assertFalse(out.exists() and any(out.iterdir()), "nothing is written when the gate refuses")

    # Failure prevented: capturing half of a session that is still running.
    def test_capture_refuses_a_session_still_running(self):
        now = time.time()
        os.utime(self.claude / "projects" / "-home-work-game" / (CLAUDE_ID + ".jsonl"), (now, now))
        code, text, _ = self.capture()
        self.assertEqual(1, code)
        self.assertIn("still active", text)
        code, text, _ = self.capture("--allow-active")
        self.assertEqual(0, code, text)

    # Failure prevented: a test repo whose sessions are out of scope, dated now
    # (so the import leaves them alone), or written anywhere but the repo.
    def test_create_builds_a_repo_ox_can_import_from(self):
        code, text, fixture = self.capture()
        self.assertEqual(0, code, text)
        target = self.tmp / "beds" / "tb-1"
        code, text = self.run_tb("create", str(target), "--fixture", str(fixture))
        self.assertEqual(0, code, text)
        repo = str(target.resolve())
        log = subprocess.run(["git", "-C", repo, "log", "--oneline"], capture_output=True, text=True, check=True).stdout
        self.assertEqual(1, len(log.splitlines()))
        self.assertTrue((target / "pkg" / ".gitignore").exists())
        data = target / ".git" / "ox-test-data"
        claude = data / "claude" / "projects" / tb.slug(repo) / (CLAUDE_ID + ".jsonl")
        codex = data / "codex" / "sessions" / "2026" / "10" / "01" / CODEX_FILE
        for seeded in (claude, codex):
            text = seeded.read_text()
            self.assertIn('"cwd":"%s"' % repo, text)
            self.assertNotIn("__REPO__", text)
            self.assertNotIn("__HOME__", text)
            self.assertGreater(time.time() - seeded.stat().st_mtime, 23 * 3600, "dated yesterday, past the quiet period")
        code, text = self.run_tb("create", str(target), "--fixture", str(fixture))
        self.assertEqual(1, code, "an existing directory is never overwritten")

    def test_word_matches_whole_words_and_escaped_newlines(self):
        self.assertIsNone(tb.word("Mac").search("macOS on a MacBook"))
        self.assertIsNotNone(tb.word("dreal").search('"push it:\\n\\ndreal@laptop repo %"'))
        self.assertIsNone(tb.word("dreal").search("xdreal"))


if __name__ == "__main__":
    unittest.main()
