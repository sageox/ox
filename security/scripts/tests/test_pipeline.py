"""Unit tests for security/scripts/pipeline.py — the parts that decide what a
/security-review run covered. Run: make sec-test
"""
from __future__ import annotations

import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pipeline  # noqa: E402


def file_diff(path: str, hunks: list) -> str:
    header = f"diff --git a/{path} b/{path}\n--- a/{path}\n+++ b/{path}\n"
    return header + "".join(hunks)


def hunk(start: int, lines: int, width: int = 40) -> str:
    body = "".join(f"+{'x' * width} {start + i}\n" for i in range(lines))
    return f"@@ -{start},0 +{start},{lines} @@\n{body}"


class SplitAndPackTest(unittest.TestCase):
    def test_small_diff_stays_whole(self):
        text = file_diff("a.go", [hunk(1, 3)])
        self.assertEqual(pipeline.split_file_diff(text, 10_000), [text])

    def test_large_diff_splits_at_hunk_boundaries_and_repeats_the_header(self):
        text = file_diff("cmd/ox/a.go", [hunk(1, 20), hunk(100, 20), hunk(200, 20)])
        limit = len(text) // 2
        pieces = pipeline.split_file_diff(text, limit)
        self.assertGreater(len(pieces), 1)
        for piece in pieces:
            self.assertTrue(piece.startswith("diff --git a/cmd/ox/a.go b/cmd/ox/a.go\n"))
            self.assertLessEqual(pipeline.nbytes(piece), limit)
            self.assertTrue(piece.split("+++ b/cmd/ox/a.go\n", 1)[1].startswith("@@ -"))
        rejoined = "".join(p.split("+++ b/cmd/ox/a.go\n", 1)[1] for p in pieces)
        self.assertEqual(rejoined, text.split("+++ b/cmd/ox/a.go\n", 1)[1], "no line lost or duplicated")

    def test_oversized_hunk_is_cut_with_post_change_line_markers(self):
        text = file_diff("big.go", [hunk(50, 400)])
        pieces = pipeline.split_file_diff(text, 4000)
        self.assertGreater(len(pieces), 2)
        for piece in pieces[1:]:
            marker = piece.split("+++ b/big.go\n", 1)[1].splitlines()[0]
            self.assertRegex(marker, r"^@@ continued: post-change line \d+ @@$")
            first_line = piece.split("+++ b/big.go\n", 1)[1].splitlines()[1]
            # each hunk line ends with its own post-change line number
            self.assertEqual(marker.split()[4], first_line.rsplit(" ", 1)[1])

    def test_pack_respects_the_limit_and_keeps_order(self):
        sections = [(f"f{i}.go", file_diff(f"f{i}.go", [hunk(1, 10)])) for i in range(6)]
        size = pipeline.nbytes(sections[0][1])
        chunks = pipeline.pack(sections, size * 2 + 10)
        self.assertEqual([p for chunk in chunks for p, _ in chunk], [p for p, _ in sections])
        for chunk in chunks:
            self.assertLessEqual(sum(pipeline.nbytes(t) for _, t in chunk), size * 2 + 10)

    def test_review_priority_puts_sensitive_paths_first_and_go_sum_last(self):
        paths = ["docs/x.md", "go.sum", "cmd/ox/status.go", "internal/auth/token.go", "go.mod", "cmd/ox/adapter.go"]
        ordered = sorted(paths, key=lambda p: (pipeline.review_priority(p), p))
        self.assertEqual(ordered, ["cmd/ox/adapter.go", "go.mod", "internal/auth/token.go",
                                   "cmd/ox/status.go", "docs/x.md", "go.sum"])

    def test_gate_and_test_classification(self):
        cases = {
            "cmd/ox/session_upload_cmd.go": True,
            "internal/daemon/agentwork/session_finalize.go": True,
            "internal/session/hold_marker.go": True,
            "internal/auth/storage.go": True,
            "cmd/ox/session_upload_cmd_test.go": False,
            "internal/session/testdata/x.go": False,
            "internal/doctor/session.go": False,
            "cmd/ox/templates/skill.md": False,
        }
        for path, gate in cases.items():
            with self.subTest(path=path):
                self.assertEqual(pipeline.is_gate_file(path), gate)


class EnvelopeTest(unittest.TestCase):
    def run_envelope(self, envelope, structured=True):
        with tempfile.TemporaryDirectory() as tmp:
            raw, out = Path(tmp) / "raw.json", Path(tmp) / "out.json"
            raw.write_text(envelope if isinstance(envelope, str) else json.dumps(envelope))
            args = ["envelope", "--raw", str(raw), "--output", str(out)] + (["--structured"] if structured else [])
            buf = io.StringIO()
            with redirect_stdout(buf):
                pipeline.main(args)
            return buf.getvalue().strip().split("\t"), out.read_text()

    def test_cost_comes_from_total_cost_usd(self):
        fields, payload = self.run_envelope(
            {"type": "result", "subtype": "success", "is_error": False, "total_cost_usd": 0.132036,
             "usage": {"input_tokens": 18}, "structured_output": {"findings": []}}
        )
        self.assertEqual(fields, ["0.132036", "success", "false", "0"])
        self.assertEqual(json.loads(payload), {"findings": []})

    def test_budget_stop_reports_what_it_spent_and_writes_an_error_stub(self):
        fields, payload = self.run_envelope(
            {"type": "result", "subtype": "error_max_budget_usd", "is_error": True, "result": None,
             "total_cost_usd": 0.0191667}
        )
        self.assertEqual(fields, ["0.019167", "error_max_budget_usd", "true", "0"])
        self.assertEqual(json.loads(payload)["verdict"], "error")

    def test_no_envelope_is_an_error_with_no_spend(self):
        fields, payload = self.run_envelope("claude: command crashed\n")
        self.assertEqual(fields, ["0.000000", "no-envelope", "true", "0"])
        self.assertEqual(json.loads(payload)["verdict"], "error")

    def test_result_text_is_used_without_a_schema(self):
        fields, payload = self.run_envelope(
            {"subtype": "success", "total_cost_usd": 0.01, "result": "# Attack surface map\n",
             "permission_denials": [{"tool_name": "Bash"}]},
            structured=False,
        )
        self.assertEqual(fields[3], "1")
        self.assertEqual(payload, "# Attack surface map\n\n")


class ClassifyPayloadTest(unittest.TestCase):
    def classify(self, text, required="findings"):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "payload"
            if text is not None:
                path.write_text(text)
            return pipeline.classify_payload(path, required)[0]

    def test_statuses(self):
        cases = [
            ('{"findings": []}', "findings", "ok"),
            ('{"findings": [{"class": "cli-input"}]}', "findings", "ok"),
            ('{"class": "a"}\n{"class": "b"}\n', "findings", "ok"),  # JSONL is accepted
            ('{"verdict": "error", "reason": "claude CLI exit 1"}', "findings", "cli-error"),
            ('{"verdict": "cap", "reason": "cost cap reached"}', "findings", "cap"),
            ('{"verdict": "skipped", "reason": "claude CLI missing"}', "findings", "skipped-no-cli"),
            ("Here are the findings: none", "findings", "parse-error"),
            ('{"summary": "nothing"}', "findings", "unknown-shape"),
            (None, "findings", "io-error"),
            ('{"entry_points": [], "sinks": []}', "entry_points", "ok"),
            ('{"verdict": "confirmed", "class": "cli-input"}', "verdict", "ok"),
            ('{"verdict": "error", "reason": "x"}', "verdict", "cli-error"),
        ]
        for text, required, want in cases:
            with self.subTest(text=text, required=required):
                self.assertEqual(self.classify(text, required), want)


class ScannerStatusTest(unittest.TestCase):
    def status(self, tool, exit_code, files=None):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            if exit_code is not None:
                (out / f"det-{tool}.exit").write_text(f"{exit_code}\n")
            for name, text in (files or {}).items():
                (out / name).write_text(text)
            return pipeline.scanner_status(out, tool)

    def test_statuses(self):
        sarif = json.dumps({"runs": [{"results": []}]})
        gosec_ok = json.dumps({"Issues": None, "Report": {}})
        gosec_typecheck = json.dumps({"Issues": [{"FromLinter": "typecheck",
                                                  "Text": "export data version 4 is greater than maximum supported version 2"}]})
        cases = [
            ("opengrep", "missing", {}, "skipped", "not installed"),
            ("opengrep", "nofiles", {}, "skipped", "no changed files"),
            ("opengrep", 0, {"det-opengrep.sarif": sarif}, "ran", ""),
            ("opengrep", 0, {}, "failed", "no SARIF output"),
            ("opengrep", 2, {"det-opengrep.log": "Fatal error: invalid rule\n"}, "failed", "invalid rule"),
            ("gosec", 3, {"det-gosec.log": "Error: unknown flag: --out-format\n"}, "failed", "golangci-lint v2 is required"),
            # A Go panic ends in stack frames; the reason must be the panic line.
            ("gosec", 2, {"det-gosec.log": "panic: file requires newer Go version go1.27 (application built with "
                          "go1.26)\n\ngoroutine 1 [running]:\nsync.(*WaitGroup).Go.func1()\n\tsync/waitgroup.go:238 +0x70\n"},
             "failed", "panic: file requires newer Go version go1.27"),
            ("gosec", 0, {"det-gosec.json": gosec_ok}, "ran", ""),
            ("gosec", 1, {"det-gosec.json": gosec_typecheck}, "failed", "use the Go version go.mod pins"),
            ("gosec", 1, {}, "failed", "no JSON report"),
            ("govulncheck", 0, {"det-govulncheck.json": '{\n  "config": {"protocol_version": "v1.0.0"}\n}\n'}, "ran", ""),
            ("govulncheck", 0, {"det-govulncheck.json": ""}, "failed", "no JSON stream"),
            ("osv-scanner", 1, {"det-osv.json": '{"results": []}'}, "ran", ""),
            ("osv-scanner", 128, {}, "failed", "no packages"),
            ("grype", "syft:1", {"det-syft.log": "syft: boom\n"}, "failed", "syft exited 1"),
            ("grype", 0, {"det-grype.sarif": sarif}, "ran", ""),
            ("grype", None, {}, "failed", "did not record"),
        ]
        for tool, code, files, want, reason in cases:
            with self.subTest(tool=tool, code=code):
                got = self.status(tool, code, files)
                self.assertEqual(got["status"], want, got)
                self.assertIn(reason, got.get("reason", ""))

    def test_gosec_paths_that_do_not_resolve_fail_instead_of_reading_as_zero(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            out = root / "security" / ".output"
            out.mkdir(parents=True)
            (root / "cmd" / "ox").mkdir(parents=True)
            (root / "cmd" / "ox" / "upload.go").write_text("package main\n")
            touched = out / "det-touched.txt"
            touched.write_text("cmd/ox/upload.go\n")
            (out / "det-gosec.exit").write_text("1\n")
            for name in ("opengrep", "govulncheck", "osv-scanner", "grype"):
                (out / f"det-{name}.exit").write_text("missing\n")

            def merge(filename):
                issue = {"FromLinter": "gosec", "Text": "G304: Potential file inclusion via variable",
                         "Pos": {"Filename": filename, "Line": 3}}
                (out / "det-gosec.json").write_text(json.dumps({"Issues": [issue]}))
                with redirect_stdout(io.StringIO()):
                    pipeline.main(["det-merge", "--out", str(out), "--root", str(root), "--scope", "diff",
                                   "--touched", str(touched)])
                return json.loads((out / "findings-deterministic.json").read_text())

            # golangci-lint v2's default: paths relative to the config file's directory.
            doc = merge("../cmd/ox/upload.go")
            self.assertEqual(doc["tools"]["gosec"]["status"], "failed")
            self.assertIn("do not resolve under the repo root", doc["tools"]["gosec"]["reason"])
            doc = merge("cmd/ox/upload.go")
            self.assertEqual(doc["tools"]["gosec"], {"status": "ran", "findings": 1})

    def test_govulncheck_stream_of_indented_objects_is_parsed(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "gv.json"
            path.write_text(
                '{\n  "config": {}\n}\n{\n  "osv": {"id": "GO-2026-0001", "summary": "bad parse"}\n}\n'
                '{\n  "finding": {"osv": "GO-2026-0001", "trace": [{"module": "m"}]}\n}\n'
                '{\n  "finding": {"osv": "GO-2026-0001", "trace": [{"module": "m", "function": "Parse"}]}\n}\n'
            )
            findings = pipeline.govulncheck_findings(path)
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0]["message"], "bad parse")
        self.assertTrue(findings[0]["reachable"], "a symbol-level trace means the code is called")


def full_state() -> dict:
    """State of a run where every stage ran: the baseline each case mutates."""
    return {
        "manifest": {
            "mode": "diff", "since": "origin/main", "diff_empty": False, "chunks_kept": 1, "chunks_total": 1,
            "truncated": False, "unreviewed_files": [], "uncommitted_files": 0,
            "gate_files": ["cmd/ox/session_upload_cmd.go"],
        },
        "tools": {t: {"status": "ran"} for t in pipeline.SCANNERS},
        "surface": {"entry_points": [{"kind": "cobra-command", "name": "ox session upload"}],
                    "chunks": {"total": 1, "ok": 1, "statuses": {"1": "ok"}}},
        "hunters": ["cli-input", "daemon-ipc"],
        "hunter_rows": [("cli-input", 1, "ok", 0), ("daemon-ipc", 1, "ok", 0)],
        "dedup": "skipped-empty",
        "findings": [],
        "cap_hit": "",
        "cap": 2.0,
    }


class CoverageTest(unittest.TestCase):
    def check(self, mutate, level, code, *reasons):
        state = full_state()
        mutate(state)
        cov = pipeline.compute_coverage(state)
        self.assertEqual(cov["level"], level, cov)
        self.assertEqual(cov["exit_code"], code, cov)
        text = " | ".join(cov["none"] + cov["partial"] + cov["notes"])
        for reason in reasons:
            self.assertIn(reason, text)

    def test_every_stage_ran_is_full(self):
        self.check(lambda s: None, "full", 0)

    def test_sensitive_change_with_no_entry_points_is_none(self):
        def mutate(s):
            s["surface"]["entry_points"] = []
        self.check(mutate, "none", 3, "touches cmd/ox/ but the surface map lists no entry points")

    def test_no_entry_points_is_fine_when_no_gate_file_changed(self):
        def mutate(s):
            s["surface"]["entry_points"] = []
            s["manifest"]["gate_files"] = []
        self.check(mutate, "full", 0)

    def test_every_scanner_skipped_or_failed_is_none(self):
        def mutate(s):
            s["tools"] = {t: {"status": "skipped", "reason": "not installed"} for t in pipeline.SCANNERS}
            s["tools"]["gosec"] = {"status": "failed", "reason": "golangci-lint exit 3"}
        self.check(mutate, "none", 3, "every deterministic scanner was skipped or failed", "gosec: failed")

    def test_some_scanners_skipped_is_partial_without_a_failure_exit(self):
        def mutate(s):
            s["tools"]["opengrep"] = {"status": "skipped", "reason": "not installed"}
        self.check(mutate, "partial", 0, "scanner skipped — opengrep: skipped")

    def test_a_failed_scanner_is_partial_with_exit_1(self):
        def mutate(s):
            s["tools"]["osv-scanner"] = {"status": "failed", "reason": "exit 127"}
        self.check(mutate, "partial", 1, "scanner failed — osv-scanner: failed (exit 127)")

    def test_no_hunter_completed_is_none(self):
        def mutate(s):
            s["hunter_rows"] = [("cli-input", 1, "cli-error", 0), ("daemon-ipc", 1, "parse-error", 0)]
        self.check(mutate, "none", 3, "no hunter run completed")

    def test_one_hunter_failed_is_partial_with_exit_1(self):
        def mutate(s):
            s["hunter_rows"][1] = ("daemon-ipc", 1, "cli-error", 0)
        self.check(mutate, "partial", 1, "1 of 2 hunter runs did not complete (1× cli-error)")

    def test_a_hunter_that_never_reported_counts_as_not_started(self):
        def mutate(s):
            s["hunter_rows"] = s["hunter_rows"][:1]
        self.check(mutate, "partial", 1, "1× not-started")

    def test_cap_hit_is_exit_2_and_names_the_phase(self):
        def mutate(s):
            s["cap_hit"] = "validate:3"
            s["findings"] = [{"title": "t", "verdict": "unvalidated"}]
        self.check(mutate, "partial", 2, "cost cap ($2.00) reached during the validation phase",
                   "1 finding(s) could not be validated")

    def test_cap_hit_before_any_hunter_finished_is_none(self):
        def mutate(s):
            s["cap_hit"] = "hunt:c01"
            s["hunter_rows"] = [("cli-input", 1, "cap", 0), ("daemon-ipc", 1, "cap", 0)]
        self.check(mutate, "none", 3, "no hunter run completed (2× cap)", "reached during the hunt phase")

    def test_truncated_review_input_is_partial(self):
        def mutate(s):
            s["manifest"].update({"truncated": True, "chunks_total": 3, "unreviewed_files": ["docs/a.md"]})
        self.check(mutate, "partial", 0, "1 of 3 chunks reviewed, 1 file(s) never reached a reviewer")

    def test_dedup_fallback_is_partial_with_exit_1(self):
        def mutate(s):
            s["dedup"] = "fallback"
        self.check(mutate, "partial", 1, "dedup failed")

    def test_empty_change_is_empty(self):
        def mutate(s):
            s["manifest"]["diff_empty"] = True
            s["tools"] = {}
        self.check(mutate, "empty", 0, "nothing to review")

    def test_prep_failure_is_none(self):
        def mutate(s):
            s["manifest"] = None
        self.check(mutate, "none", 3, "the review input was never built")

    def test_missing_scanner_status_is_none(self):
        def mutate(s):
            s["tools"] = None
        self.check(mutate, "none", 3, "reported no scanner status")

    def test_no_reviewable_source_is_a_note_not_a_failure(self):
        def mutate(s):
            s["manifest"].update({"chunks_kept": 0, "chunks_total": 0, "gate_files": []})
            s["surface"] = {"entry_points": [], "chunks": {"total": 0, "ok": 0, "statuses": {}}}
            s["hunter_rows"] = []
        self.check(mutate, "full", 0, "no reviewable source in the change")


class FindingsReportTest(unittest.TestCase):
    def render(self, mutate):
        state = full_state()
        state.update({"raw_count": 0, "deduped_count": 0, "dropped": 0, "malformed": 0, "cost": 0.5,
                      "subsidized": False})
        state["manifest"].update({"files": [], "excluded_tests": []})
        mutate(state)
        cov = pipeline.compute_coverage(state)
        counts = {sev: 0 for sev in pipeline.SEVERITY_ORDER}
        return pipeline.render_findings(state, cov, counts)

    def test_ran_clean_only_with_full_coverage(self):
        self.assertIn("Pipeline ran clean", self.render(lambda s: None))
        no_coverage = self.render(lambda s: s["surface"].update(entry_points=[]))
        self.assertNotIn("ran clean", no_coverage)
        self.assertIn("**NO COVERAGE**", no_coverage)
        self.assertIn("coverage: none", no_coverage)

    def test_unvalidated_findings_are_shown_and_tagged(self):
        def mutate(s):
            s["findings"] = [{"title": "Traversal", "severity": "high", "file": "cmd/ox/a.go", "line": 7,
                              "verdict": "unvalidated", "verdict_reason": "cost cap reached before validation",
                              "fix": {"patch": "reject separators", "design": "resolve by listing"}}]
        report = self.render(mutate)
        self.assertIn("## [HIGH] Traversal (UNVALIDATED)", report)
        self.assertIn("`cmd/ox/a.go:7`", report)
        self.assertIn("patch: reject separators; design: resolve by listing", report)
        self.assertIn("**PARTIAL COVERAGE**", report)


class ValidatorResultTest(unittest.TestCase):
    def merge(self, finding, output=None, unvalidated=""):
        with tempfile.TemporaryDirectory() as tmp:
            f, o = Path(tmp) / "finding", Path(tmp) / "output"
            f.write_text(json.dumps(finding))
            if output is not None:
                o.write_text(output)
            args = ["validator-result", "--finding", str(f), "--output", str(o)]
            if unvalidated:
                args += ["--unvalidated", unvalidated]
            buf = io.StringIO()
            with redirect_stdout(buf):
                pipeline.main(args)
            return json.loads(buf.getvalue())

    def test_verdict_overlays_the_original(self):
        got = self.merge({"title": "t", "file": "a.go", "severity": "high"},
                         json.dumps({"class": "cli-input", "verdict": "likely", "severity": "medium"}))
        self.assertEqual((got["verdict"], got["severity"], got["file"]), ("likely", "medium", "a.go"))

    def test_failed_validation_keeps_the_finding_unvalidated(self):
        for output, reason in [('{"verdict": "error", "reason": "x"}', "did not complete (cli-error)"),
                               ('{"verdict": "cap", "reason": "x"}', "cost cap reached"),
                               (None, "did not complete (io-error)")]:
            with self.subTest(output=output):
                got = self.merge({"title": "t"}, output)
                self.assertEqual(got["verdict"], "unvalidated")
                self.assertIn(reason, got["verdict_reason"])


if __name__ == "__main__":
    unittest.main()
