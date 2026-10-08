"""End-to-end tests for the /security-review pipeline's plumbing.

Each test builds a scratch git repo (a base commit on origin/main, a feature
commit on top), copies the pipeline in, and runs the real orchestrate.sh /
deterministic.sh against it with a fake `claude` and a fake golangci-lint on
PATH (tests/fakes/). No network, no tokens.

The customer promise under test: a /security-review run reports what it
actually reviewed. The planted path traversal is reported only if the change
reached the hunters; a run that reviewed nothing says NO COVERAGE instead of
"ran clean"; a scanner that errored is "failed", not "ran"; the cost cap counts
the cost the CLI reports.

SEC_PIPELINE_SRC=<dir> copies the pipeline from another tree, e.g. an export of
origin/main, to watch these tests fail against the old pipeline.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
FAKES = Path(__file__).resolve().parent / "fakes"
PLANT = "os.RemoveAll(filepath.Join(heldDir, args[0]))"
FINDING_TITLE = "Session name from argv reaches os.RemoveAll unvalidated"
TEST_ONLY_MARKER = "TEST_ONLY_MARKER_never_sent_to_hunters"

BASE_UPLOAD = textwrap.dedent(
    """\
    package main

    import (
    \t"fmt"
    \t"os"
    \t"path/filepath"
    )

    // publishHeld uploads a held session from heldDir.
    func publishHeld(heldDir, name string) error {
    \tif _, err := os.Stat(filepath.Join(heldDir, "index")); err != nil {
    \t\treturn fmt.Errorf("no index: %w", err)
    \t}
    \treturn nil
    }
    """
)

FEATURE_UPLOAD = BASE_UPLOAD + textwrap.dedent(
    f"""
    // discardHeld drops a held session's local copy instead of publishing it.
    func discardHeld(heldDir string, args []string) error {{
    \treturn {PLANT}
    }}
    """
)

# Every tool the pipeline would run for real. A test must reach only the fakes it
# installed; a real one on PATH would spend tokens or scan for real.
REAL_TOOLS = ("claude", "golangci-lint", "opengrep", "govulncheck", "osv-scanner", "syft", "grype")

GIT_ENV = {
    "GIT_AUTHOR_NAME": "Pipeline Test",
    "GIT_AUTHOR_EMAIL": "pipeline-test@example.invalid",
    "GIT_COMMITTER_NAME": "Pipeline Test",
    "GIT_COMMITTER_EMAIL": "pipeline-test@example.invalid",
    "GIT_CONFIG_NOSYSTEM": "1",
}


class ScratchRepo:
    """A git repo with the pipeline copied in and a feature branch to review."""

    def __init__(self, root: Path, src: Path):
        self.root = root / "repo"
        self.bin = root / "bin"
        self.home = root / "home"
        self.log = root / "claude-calls.jsonl"
        for d in (self.root, self.bin, self.home):
            d.mkdir(parents=True)
        self.installed = set()
        # Only bash comes from the host (macOS /bin/bash is 3.2); PATH is
        # otherwise fakes + /usr/bin + /bin, so no other host tool leaks in.
        (self.bin / "bash").symlink_to(shutil.which("bash") or "/bin/bash")
        self.path = os.pathsep.join([str(self.bin), "/usr/bin", "/bin"])
        self._git("init", "-q", "-b", "main")
        for rel in ("security", ".claude/skills/security-review"):
            shutil.copytree(src / rel, self.root / rel, ignore=shutil.ignore_patterns(".output", "tests", "__pycache__"))
        self.write("cmd/ox/upload.go", BASE_UPLOAD)
        self.write("internal/daemon/finalize.go", "package daemon\n\nfunc finalize() error { return nil }\n")
        self.write("README.md", "# scratch\n")
        self.commit("base")
        self._git("update-ref", "refs/remotes/origin/main", "HEAD")
        self._git("checkout", "-q", "-b", "feature")

    def _git(self, *args: str) -> str:
        r = subprocess.run(["git", *args], cwd=self.root, env={**os.environ, **GIT_ENV, "HOME": str(self.home)},
                           capture_output=True, text=True, check=True)
        return r.stdout

    def write(self, rel: str, text: str) -> None:
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self, message: str) -> None:
        self._git("add", "-A")
        self._git("commit", "-q", "-m", message)

    def plant_feature(self) -> None:
        """The change under review: a Cobra-path traversal in cmd/ox, a daemon
        change, a test file and a doc."""
        self.write("cmd/ox/upload.go", FEATURE_UPLOAD)
        self.write("cmd/ox/upload_test.go", f'package main\n\n// {TEST_ONLY_MARKER}\nfunc helper() {{}}\n')
        self.write("internal/daemon/finalize.go", "package daemon\n\nfunc finalize() error { return publish() }\n")
        self.write("docs/notes.md", "Held sessions can now be discarded.\n")
        self.commit("feature: discard held sessions")

    def install(self, *fakes: str) -> None:
        for name in fakes:
            target = self.bin / name
            shutil.copy(FAKES / name, target)
            target.chmod(0o755)
            self.installed.add(name)

    def leaked_tools(self) -> list:
        """Real tools reachable on the test PATH (must be none)."""
        return [t for t in REAL_TOOLS if t not in self.installed and shutil.which(t, path=self.path)]

    def set_config(self, key: str, value: str) -> None:
        cfg = self.root / "security/config.yml"
        lines = [line for line in cfg.read_text().splitlines() if not line.strip().startswith(key + ":")]
        cfg.write_text("\n".join(lines) + f"\n{key}: {value}\n")

    def run(self, script: str, *args: str, **env: str) -> subprocess.CompletedProcess:
        leaked = self.leaked_tools()
        if leaked:
            raise AssertionError(f"real tools on the test PATH would run for real: {leaked} ({self.path})")
        full_env = {
            "PATH": self.path,
            "HOME": str(self.home),
            "FAKE_CLAUDE_LOG": str(self.log),
            "LANG": "C.UTF-8",
            **GIT_ENV,
            **env,
        }
        return subprocess.run([str(self.bin / "bash"), f"security/scripts/{script}", *args], cwd=self.root,
                              env=full_env, capture_output=True, text=True, timeout=180)

    def calls(self, role: str | None = None) -> list:
        if not self.log.exists():
            return []
        calls = [json.loads(line) for line in self.log.read_text().splitlines() if line.strip()]
        return [c for c in calls if role is None or c["role"] == role]

    def output(self, name: str) -> str:
        path = self.root / "security/.output" / name
        return path.read_text() if path.exists() else ""


class OrchestrateE2ETest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        src = Path(os.environ.get("SEC_PIPELINE_SRC") or REPO)
        self.repo = ScratchRepo(Path(self._tmp.name), src)

    def tearDown(self):
        self._tmp.cleanup()

    def explain(self, result: subprocess.CompletedProcess) -> str:
        return f"\n--- stdout ---\n{result.stdout[-4000:]}\n--- stderr ---\n{result.stderr[-2000:]}"

    def test_planted_issue_reaches_every_reviewer_and_is_reported(self):
        """Devon's change plants an argv path traversal; the hunters see it."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh")

        cartographers = self.repo.calls("cartographer")
        hunters = self.repo.calls("hunter")
        self.assertTrue(cartographers, "cartographer never ran" + self.explain(result))
        for call in cartographers:
            self.assertIn(PLANT, call["stdin"], "the cartographer was not shown the diff")
        self.assertEqual(len(hunters), 5, self.explain(result))
        for call in hunters:
            self.assertIn(PLANT, call["stdin"], f"{call['playbook']} was not shown the diff")
            self.assertNotIn(TEST_ONLY_MARKER, call["stdin"], "test files must not be sent to hunters")
        for call in self.repo.calls():
            self.assertEqual(call["tools"], "Read,Grep,Glob", "subagents get read-only tools only")

        report = self.repo.output("FINDINGS.md")
        self.assertIn(FINDING_TITLE, report, self.explain(result))
        self.assertIn("`confirmed`", report)
        self.assertNotIn("NO COVERAGE", report)
        self.assertIn("PARTIAL COVERAGE", report)  # four scanners are not installed here
        self.assertEqual(result.returncode, 0, self.explain(result))

    def test_subagents_get_no_context_from_the_reviewed_branch(self):
        """Avery reviews a stranger's PR: its commit messages, branch name, CLAUDE.md, rules and
        project hooks are the PR author's, so none of them may reach a reviewer's context."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh")

        calls = self.repo.calls()
        self.assertTrue(calls, self.explain(result))
        for call in calls:
            with self.subTest(role=call["role"]):
                self.assertEqual(call["setting_sources"], "user",
                                 "project settings would run the branch's hooks and MCP servers")
                self.assertEqual(call["env"]["CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS"], "1",
                                 "the git status snapshot carries the branch name and its commit messages")
                self.assertEqual(call["env"]["CLAUDE_CODE_DISABLE_CLAUDE_MDS"], "1",
                                 "CLAUDE.md and .claude/rules files come from the branch")

    def test_sensitive_change_without_entry_points_reports_no_coverage(self):
        """Avery's run maps no entry points for a cmd/ox + daemon change."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", FAKE_NO_ENTRY_POINTS="1", FAKE_PLANT="(matches nothing)")

        report = self.repo.output("FINDINGS.md")
        self.assertIn("**NO COVERAGE**", report, self.explain(result))
        self.assertNotIn("ran clean", report)
        self.assertIn("the change touches cmd/ox/, internal/daemon/ but the surface map lists no entry points", report)
        self.assertIn("coverage: NO COVERAGE", result.stdout)
        self.assertEqual(result.returncode, 3, self.explain(result))

    def test_every_scanner_skipped_or_failed_reports_no_coverage(self):
        """Sam's machine has no scanners and a golangci-lint that cannot type-check."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", FAKE_GOSEC="typecheck")

        report = self.repo.output("FINDINGS.md")
        self.assertIn("**NO COVERAGE**", report, self.explain(result))
        self.assertIn("every deterministic scanner was skipped or failed", report)
        self.assertIn("gosec: failed (golangci-lint could not type-check", report)
        # The scanner summary survives in det-runner.log and is echoed by the orchestrator.
        self.assertIn("gosec:         failed", self.repo.output("det-runner.log"))
        self.assertIn("gosec:         failed", result.stdout)
        self.assertIn(FINDING_TITLE, report, "findings are still shown under NO COVERAGE")
        self.assertEqual(result.returncode, 3, self.explain(result))

    def test_scanner_that_errors_is_reported_failed_not_ran(self):
        """Riley runs the fast tier; golangci-lint exits 3."""
        self.repo.plant_feature()
        self.repo.install("golangci-lint")
        crashed = self.repo.run("deterministic.sh", FAKE_GOSEC="crash")
        gosec_line = next((l for l in crashed.stdout.splitlines() if l.strip().startswith("gosec:")), "")
        self.assertIn("failed", gosec_line, self.explain(crashed))
        self.assertNotIn("ran", gosec_line.split("(", 1)[0])
        self.assertIn("NO COVERAGE", crashed.stdout)
        self.assertEqual(crashed.returncode, 3, self.explain(crashed))

        clean = self.repo.run("deterministic.sh", FAKE_GOSEC="issue")
        gosec_line = next((l for l in clean.stdout.splitlines() if l.strip().startswith("gosec:")), "")
        self.assertIn("ran (1 finding(s))", gosec_line, self.explain(clean))
        self.assertEqual(clean.returncode, 0, self.explain(clean))

    def test_fast_tier_writes_a_findings_summary(self):
        """Riley's PR runs the fast tier in CI; the job summary says what each scanner found."""
        self.repo.plant_feature()
        self.repo.install("golangci-lint")
        result = self.repo.run("deterministic.sh", FAKE_GOSEC="issue")

        summary = self.repo.output("det-summary.md")
        self.assertIn("| gosec | ran: 1 finding(s) |", summary, self.explain(result))
        self.assertIn("| opengrep | skipped: not installed", summary)
        self.assertIn("| gosec | G304 | `cmd/ox/upload.go:11` |", summary)
        self.assertIn("**PARTIAL COVERAGE**", summary)

    def test_fast_tier_sets_apart_findings_off_the_changed_lines(self):
        """Ryan's change appends to upload.go; a gosec hit on an untouched line of that file is existing
        code, not his, and must not be listed as if the change introduced it."""
        self.repo.plant_feature()
        self.repo.install("golangci-lint")
        old = self.repo.run("deterministic.sh", FAKE_GOSEC="issue")  # line 11: not changed by the feature
        head, marker, tail = self.repo.output("det-summary.md").partition(
            "Elsewhere in touched files (existing code): 1 finding(s)")
        self.assertTrue(marker, self.explain(old))
        self.assertIn("`cmd/ox/upload.go:11`", tail)
        self.assertNotIn("upload.go:11", head)

        new = self.repo.run("deterministic.sh", FAKE_GOSEC="issue", FAKE_GOSEC_LINE="19")  # the planted line
        summary = self.repo.output("det-summary.md")
        self.assertIn("| gosec | G304 | `cmd/ox/upload.go:19` |", summary, self.explain(new))
        self.assertNotIn("Elsewhere in touched files", summary)

    def test_fast_tier_records_whether_the_change_touches_dependencies(self):
        """Dependency advisories collapse unless the change edits go.mod or go.sum, so det-merge must say which."""
        self.repo.plant_feature()
        self.repo.install("golangci-lint")
        self.repo.run("deterministic.sh")
        self.assertIs(json.loads(self.repo.output("findings-deterministic.json"))["dependency_change"], False)

        self.repo.write("go.mod", "module example.com/scratch\n\ngo 1.26\n")
        self.repo.commit("add go.mod")
        result = self.repo.run("deterministic.sh")
        self.assertIs(json.loads(self.repo.output("findings-deterministic.json"))["dependency_change"], True,
                      self.explain(result))

    def test_reported_cost_is_tracked_and_the_cap_stops_later_phases(self):
        """Quinn caps a run at $0.50; each call reports $0.20 in total_cost_usd."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", "--cap=0.5", FAKE_COST="0.2", FAKE_HONOR_BUDGET="0")

        # cartographer $0.20, then one wave of five hunters at $0.20 each.
        self.assertIn("cost:    $1.20 (cap $0.50)", result.stdout, self.explain(result))
        self.assertEqual(self.repo.calls("dedup") + self.repo.calls("validator"), [], "the cap must stop later phases")
        report = self.repo.output("FINDINGS.md")
        self.assertIn(f"{FINDING_TITLE} (UNVALIDATED)", report, "a capped run keeps its findings")
        self.assertIn("cost cap ($0.50) reached during the dedup phase", report)
        self.assertEqual(result.returncode, 2, self.explain(result))

    def test_budget_stop_is_recorded_as_spend_not_a_crash(self):
        """The CLI stops a hunter at --max-budget-usd; what it spent still counts."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", "--cap=0.5", FAKE_COST="0.3")

        ledger = [line.split("\t") for line in self.repo.output(".cost-ledger").splitlines()]
        self.assertEqual(len(ledger), 6, self.explain(result))  # cartographer + five stopped hunters
        # The wave's $0.20 remainder is split five ways: each hunter stops at $0.04.
        self.assertAlmostEqual(sum(float(row[0]) for row in ledger), 0.3 + 5 * 0.041, places=6)
        statuses = [line.split("\t")[2] for line in self.repo.output("hunter-status.tsv").splitlines()]
        self.assertEqual(statuses, ["cap"] * 5)
        self.assertIn("cost cap ($0.50) reached during the hunt phase", self.repo.output("FINDINGS.md"))
        self.assertEqual(result.returncode, 3, self.explain(result))

    def test_parallel_hunters_cannot_jointly_spend_past_the_cap(self):
        """Each hunter stays under its own limit; together they must not pass the cap."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        # $0.20 per call, cap $1: the cartographer leaves $0.80 for five hunters.
        # With no findings, nothing after the hunt would ever notice an overshoot.
        result = self.repo.run("orchestrate.sh", "--cap=1.0", FAKE_COST="0.2", FAKE_PLANT="(matches nothing)")

        spent = sum(float(line.split("\t")[0]) for line in self.repo.output(".cost-ledger").splitlines())
        self.assertLessEqual(spent, 1.0 + 5 * 0.001 + 1e-9, self.explain(result))  # + the CLI's per-call overshoot
        self.assertIn("cost cap ($1.00) reached during the hunt phase", self.repo.output("FINDINGS.md"))

    def test_scanner_findings_reach_the_report(self):
        """A gosec hit in a touched file is listed in FINDINGS.md and the SARIF."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", FAKE_GOSEC="issue")

        report = self.repo.output("FINDINGS.md")
        self.assertIn("| gosec | G304 | `cmd/ox/upload.go:11` |", report, self.explain(result))
        self.assertIn("gosec/G304", self.repo.output("findings.sarif"))

    def validator_models(self) -> dict:
        """The model each validation ran on, keyed by the class of the finding it judged."""
        models = {}
        for call in self.repo.calls("validator"):
            for cls in ("cli-input", "daemon-ipc"):
                if f'"class": "{cls}"' in call["stdin"]:
                    models[cls] = call["model"]
        return models

    def test_hard_class_finding_is_validated_by_the_hard_class_model(self):
        """Devon's change also touches daemon IPC; that finding goes to Opus, the argv one to Sonnet."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", FAKE_DAEMON_FINDING="1")

        self.assertEqual(self.validator_models(), {"daemon-ipc": "claude-opus-5-5", "cli-input": "claude-sonnet-5"},
                         self.explain(result))
        report = self.repo.output("FINDINGS.md")
        self.assertIn("- **validated by**: `claude-opus-5-5`", report, self.explain(result))
        self.assertIn("- **validated by**: `claude-sonnet-5`", report)

    def test_model_edits_in_config_reach_every_phase(self):
        """Quinn changes every model in security/config.yml; each phase runs on the one configured."""
        keys = ("cartographer_model", "hunter_model", "dedup_model", "validator_model", "validator_hard_class_model")
        self.repo.plant_feature()
        for key in keys:
            self.repo.set_config(key, f'"configured-{key}"')  # quoted, as YAML allows
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", FAKE_DAEMON_FINDING="1")

        for role in ("cartographer", "hunter", "dedup"):
            used = {call["model"] for call in self.repo.calls(role)}
            self.assertEqual(used, {f"configured-{role}_model"}, role + self.explain(result))
        self.assertEqual(self.validator_models(), {"daemon-ipc": "configured-validator_hard_class_model",
                                                   "cli-input": "configured-validator_model"}, self.explain(result))

    def test_stale_clean_report_is_replaced_when_claude_is_missing(self):
        """Last week's report said "ran clean"; this run cannot reach claude."""
        self.repo.plant_feature()
        self.repo.install("golangci-lint")
        out = self.repo.root / "security/.output"
        out.mkdir(parents=True, exist_ok=True)
        (out / "FINDINGS.md").write_text("_No confirmed findings. Pipeline ran clean._\n")
        result = self.repo.run("orchestrate.sh")

        report = self.repo.output("FINDINGS.md")
        self.assertNotIn("ran clean", report, self.explain(result))
        self.assertIn("**NO COVERAGE**", report)
        self.assertIn("no hunter run completed (5× skipped-no-cli)", report)
        self.assertEqual(result.returncode, 3, self.explain(result))

    def plant_large_change(self) -> None:
        """The planted feature plus internal/session files: several chunks at 3 KB."""
        self.repo.plant_feature()
        for i in range(3):
            body = "".join(f"// filler line {j} in file {i} to push the change past one chunk\n" for j in range(40))
            self.repo.write(f"internal/session/filler{i}.go", f"package session\n\n{body}")
        self.repo.commit("filler")
        self.repo.set_config("chunk_bytes", "3000")
        self.repo.install("claude", "golangci-lint")

    def test_large_change_is_chunked_and_every_chunk_reviewed(self):
        """A change bigger than one chunk is split, and every chunk gets every hunter."""
        self.plant_large_change()
        result = self.repo.run("orchestrate.sh")

        hunters = self.repo.calls("hunter")
        self.assertEqual(sum(1 for c in hunters if PLANT in c["stdin"]), 5, "the plant lands in exactly one chunk")
        self.assertIn(FINDING_TITLE, self.repo.output("FINDINGS.md"), self.explain(result))
        chunks = json.loads(self.repo.output("review/manifest.json"))["chunks_kept"]
        self.assertGreaterEqual(chunks, 2, self.explain(result))
        self.assertEqual(len(hunters), 5 * chunks, "every chunk gets every hunter")

    def test_entry_points_from_one_chunk_do_not_vouch_for_another(self):
        """Only the cmd/ox chunk gets mapped; the internal/session chunks stay blind."""
        self.plant_large_change()
        result = self.repo.run("orchestrate.sh")

        report = self.repo.output("FINDINGS.md")
        self.assertIn("cartographer listed no entry points for the gate files in chunk", report, self.explain(result))
        self.assertIn("(internal/session/)", report)
        self.assertNotIn("ran clean", report)
        self.assertEqual(result.returncode, 1, self.explain(result))

    def test_unresolvable_base_ref_reports_no_coverage(self):
        """A base ref that does not exist means nothing could be diffed."""
        self.repo.plant_feature()
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh", "--since=does-not-exist")

        report = self.repo.output("FINDINGS.md")
        self.assertIn("**NO COVERAGE**", report, self.explain(result))
        self.assertIn("the review input was never built", report)
        self.assertEqual(self.repo.calls("hunter"), [])
        self.assertEqual(result.returncode, 3, self.explain(result))


if __name__ == "__main__":
    unittest.main()
