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
        self.assertAlmostEqual(sum(float(row[0]) for row in ledger), 0.3 + 5 * 0.201, places=6)
        statuses = [line.split("\t")[2] for line in self.repo.output("hunter-status.tsv").splitlines()]
        self.assertEqual(statuses, ["cap"] * 5)
        self.assertIn("cost cap ($0.50) reached during the hunt phase", self.repo.output("FINDINGS.md"))
        self.assertEqual(result.returncode, 3, self.explain(result))

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

    def test_large_change_is_chunked_and_every_chunk_reviewed(self):
        """A change bigger than one chunk is split, and every chunk gets every hunter."""
        self.repo.plant_feature()
        for i in range(3):
            body = "".join(f"// filler line {j} in file {i} to push the change past one chunk\n" for j in range(40))
            self.repo.write(f"internal/session/filler{i}.go", f"package session\n\n{body}")
        self.repo.commit("filler")
        self.repo.set_config("chunk_bytes", "3000")
        self.repo.install("claude", "golangci-lint")
        result = self.repo.run("orchestrate.sh")

        hunters = self.repo.calls("hunter")
        self.assertEqual(sum(1 for c in hunters if PLANT in c["stdin"]), 5, "the plant lands in exactly one chunk")
        self.assertIn(FINDING_TITLE, self.repo.output("FINDINGS.md"), self.explain(result))
        chunks = json.loads(self.repo.output("review/manifest.json"))["chunks_kept"]
        self.assertGreaterEqual(chunks, 2, self.explain(result))
        self.assertEqual(len(hunters), 5 * chunks, "every chunk gets every hunter")

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
