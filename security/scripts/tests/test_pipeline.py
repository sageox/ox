"""Unit tests for security/scripts/pipeline.py — the parts that decide what a
/security-review run covered. Run: make sec-test
"""
from __future__ import annotations

import io
import json
import re
import sys
import tempfile
import textwrap
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
        """Verify scanner statuses and reasons, including tool-specific install hints."""
        sarif = json.dumps({"runs": [{"results": []}]})
        gosec_ok = json.dumps({"Issues": None, "Report": {}})
        gosec_typecheck = json.dumps({"Issues": [{"FromLinter": "typecheck",
                                                  "Text": "export data version 4 is greater than maximum supported version 2"}]})
        cases = [
            ("opengrep", "missing", {}, "skipped", "not installed — run `make sec-install`"),
            # make sec-install does not install golangci-lint; the reason must name what is missing.
            ("gosec", "missing", {}, "skipped", "golangci-lint v2 is not installed"),
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

    def test_advisory_collectors_keep_aliases_and_the_package(self):
        with tempfile.TemporaryDirectory() as tmp:
            gv, osv = Path(tmp) / "gv.json", Path(tmp) / "osv.json"
            gv.write_text('{"osv": {"id": "GO-2026-0002", "summary": "s", "aliases": ["GHSA-aaaa-bbbb-cccc"]}}\n'
                          '{"finding": {"osv": "GO-2026-0002", "trace": [{"module": "golang.org/x/crypto"}]}}\n')
            osv.write_text(json.dumps({"results": [{"source": {"path": str(Path(tmp) / "go.mod")}, "packages": [
                {"package": {"name": "github.com/docker/docker", "version": "v28.5.2"},
                 "vulnerabilities": [{"id": "GO-2026-4887", "aliases": ["GHSA-x86f-5xw2-fm2r"], "summary": "bypass"}]}]}]}))
            g = pipeline.govulncheck_findings(gv)[0]
            o = pipeline.osv_findings(osv, Path(tmp))[0]
        self.assertEqual((g["aliases"], g["package"]), (["GHSA-aaaa-bbbb-cccc"], "golang.org/x/crypto"))
        self.assertEqual((o["aliases"], o["package"]), (["GHSA-x86f-5xw2-fm2r"], "github.com/docker/docker"))


def full_state() -> dict:
    """State of a run where every stage ran: the baseline each case mutates."""
    return {
        "manifest": {
            "mode": "diff", "since": "origin/main", "diff_empty": False, "chunks_kept": 1, "chunks_total": 1,
            "truncated": False, "unreviewed_files": [], "uncommitted_files": 0,
            "gate_files": ["cmd/ox/session_upload_cmd.go"],
            "chunks": [{"index": 1, "paths": ["cmd/ox/session_upload_cmd.go"]}],
        },
        "tools": {t: {"status": "ran"} for t in pipeline.SCANNERS},
        "surface": {"entry_points": [{"kind": "cobra-command", "name": "ox session upload"}],
                    "chunks": {"total": 1, "ok": 1, "statuses": {"1": "ok"}, "entry_points": {"1": 1}}},
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

    def two_chunks(self, s, second_chunk_entry_points):
        s["manifest"].update({
            "chunks_kept": 2, "chunks_total": 2,
            "gate_files": ["cmd/ox/session_upload_cmd.go", "internal/daemon/finalize.go"],
            "chunks": [{"index": 1, "paths": ["cmd/ox/session_upload_cmd.go"]},
                       {"index": 2, "paths": ["internal/daemon/finalize.go", "docs/notes.md"]}],
        })
        s["surface"]["chunks"] = {"total": 2, "ok": 2, "statuses": {"1": "ok", "2": "ok"},
                                  "entry_points": {"1": 1, "2": second_chunk_entry_points}}
        s["hunter_rows"] += [("cli-input", 2, "ok", 0), ("daemon-ipc", 2, "ok", 0)]

    def test_entry_points_from_one_chunk_do_not_vouch_for_another(self):
        # Without this, chunk 1's entry point satisfies the merged check and a
        # chunk-2 daemon change nobody mapped reads as full coverage.
        self.check(lambda s: self.two_chunks(s, 0), "partial", 1,
                   "cartographer listed no entry points for the gate files in chunk 2 (internal/daemon/)")

    def test_every_gate_chunk_with_its_own_entry_points_is_full(self):
        self.check(lambda s: self.two_chunks(s, 1), "full", 0)

    def test_a_missing_per_chunk_count_fails_closed(self):
        def mutate(s):
            self.two_chunks(s, 1)
            del s["surface"]["chunks"]["entry_points"]["2"]
        self.check(mutate, "partial", 1, "chunk 2 (internal/daemon/)")

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
                      "subsidized": False, "scanner_findings": []})
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

    def test_scanner_findings_are_reported_but_never_called_clean(self):
        # Before, a scanner hit no AI reviewer repeated vanished from FINDINGS.md.
        det = {"findings": [{"tool": "gosec", "ruleId": "G304", "level": "warning",
                             "message": "G304: Potential file inclusion via variable",
                             "locations": [{"file": "cmd/ox/b.go", "line": 3}]}]}
        report = self.render(lambda s: s.update(scanner_findings=pipeline.scanner_report_findings(det, [])))
        self.assertIn("## Scanner findings (not validated by the AI tier)", report)
        self.assertIn("| gosec | G304 | `cmd/ox/b.go:3` | G304: Potential file inclusion via variable |", report)
        self.assertIn("scanner_findings: 1", report)
        self.assertNotIn("ran clean", report)
        self.assertIn("No AI-confirmed findings. 1 scanner finding(s) are listed below, unvalidated.", report)

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


class ScannerFindingsTest(unittest.TestCase):
    DET = {"findings": [
        {"tool": "gosec", "ruleId": "G304", "level": "warning", "message": "dup of an AI finding",
         "locations": [{"file": "cmd/ox/a.go", "line": 7}]},
        {"tool": "gosec", "ruleId": "G204", "level": "warning", "message": "subprocess with variable",
         "locations": [{"file": "cmd/ox/b.go", "line": 3}]},
        {"tool": "govulncheck", "ruleId": "GO-2026-0001", "level": "error", "message": "bad parse",
         "locations": [], "reachable": True},
    ]}

    def test_dedups_against_ai_findings_and_orders_advisories_first(self):
        ai = [{"title": "t", "file": "cmd/ox/a.go:7"}]
        got = pipeline.scanner_report_findings(self.DET, ai)
        self.assertEqual([(f["tool"], f["rule"]) for f in got], [("govulncheck", "GO-2026-0001"), ("gosec", "G204")])
        self.assertTrue(got[0]["reachable"])

    def test_det_summary_lists_each_scanner_and_its_findings(self):
        doc = {**self.DET, "scope": "diff", "since": "origin/main", "touched_files": 2, "coverage": "partial",
               "tools": {"opengrep": {"status": "ran", "findings": 0},
                         "govulncheck": {"status": "ran", "findings": 1},
                         "osv-scanner": {"status": "skipped", "reason": "not installed"},
                         "grype": {"status": "failed", "reason": "exit 2"},
                         "gosec": {"status": "ran", "findings": 2}}}
        md = pipeline.render_det_summary(doc)
        self.assertIn("**PARTIAL COVERAGE**", md)
        self.assertIn("| grype | failed: exit 2 |", md)
        self.assertIn("| osv-scanner | skipped: not installed |", md)
        self.assertIn("| gosec | ran: 2 finding(s) |", md)
        self.assertIn("| govulncheck | GO-2026-0001 (reachable) | dependency | bad parse |", md)
        self.assertIn("| gosec | G204 | `cmd/ox/b.go:3` | subprocess with variable |", md)

    def test_pipes_in_any_cell_are_escaped(self):
        """A file path the PR controls, or a scanner's failure reason, must not split a column."""
        doc = {"findings": [{"tool": "gosec", "ruleId": "G304", "level": "warning", "message": "read",
                             "locations": [{"file": "cmd/ox/a|b.go", "line": 4}]}],
               "coverage": "partial", "scope": "diff", "since": "origin/main", "touched_files": 1,
               "tools": {"gosec": {"status": "ran", "findings": 1},
                         "grype": {"status": "failed", "reason": "exit 2 | db stale"}}}
        md = pipeline.render_det_summary(doc)
        self.assertIn("| gosec | G304 | `cmd/ox/a\\|b.go:4` | read |", md)
        self.assertIn("| grype | failed: exit 2 \\| db stale |", md)

    def test_changed_lines_come_from_added_hunk_ranges(self):
        diff = ("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n"
                "@@ -10,0 +11,2 @@\n+x\n+y\n@@ -20 +22 @@\n-old\n+new\n@@ -30,3 +33,0 @@\n-gone\n"
                "diff --git a/old.go b/old.go\n--- a/old.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n")
        self.assertEqual(pipeline.parse_changed_lines(diff), {"a.go": {11, 12, 22}})

    def test_a_failed_diff_marks_nothing_as_existing_code(self):
        """If the diff cannot be read, no finding may be moved out of the main table."""
        with tempfile.TemporaryDirectory() as tmp:  # not a git repo: both diff attempts fail
            changed = pipeline.changed_lines(Path(tmp), "origin/main")
        self.assertIsNone(changed)
        findings = [{"tool": "gosec", "locations": [{"file": "a.go", "line": 3}]}]
        pipeline.mark_in_diff(findings, changed)
        self.assertNotIn("in_diff", findings[0])
        pipeline.mark_in_diff(findings, {"a.go": {3}})
        self.assertTrue(findings[0]["in_diff"])

    def test_findings_off_the_changed_lines_are_set_apart(self):
        """A hit in a touched file but not on a changed line is existing code, not the change's."""
        doc = {"findings": [
                   {"tool": "gosec", "ruleId": "G304", "level": "warning", "message": "new", "in_diff": True,
                    "locations": [{"file": "a.go", "line": 3}]},
                   {"tool": "gosec", "ruleId": "G104", "level": "warning", "message": "old", "in_diff": False,
                    "locations": [{"file": "a.go", "line": 90}]},
                   {"tool": "grype", "ruleId": "CVE-1", "level": "error", "message": "dep", "locations": []}],
               "coverage": "full", "scope": "diff", "since": "origin/main", "touched_files": 1,
               "tools": {"gosec": {"status": "ran", "findings": 2}, "grype": {"status": "ran", "findings": 1}}}
        md = pipeline.render_det_summary(doc)
        head, marker, tail = md.partition("Elsewhere in touched files (existing code): 1 finding(s)")
        self.assertTrue(marker, md)
        self.assertIn("| gosec | G304 | `a.go:3` | new |", head)
        self.assertIn("| grype | CVE-1 | dependency | dep |", head)
        self.assertIn("| gosec | G104 | `a.go:90` | old |", tail)
        self.assertNotIn("G104", head)

    ADVISORIES = [
        {"tool": "govulncheck", "ruleId": "GO-2026-6355", "level": "error", "message": "DoS in ssh", "locations": [],
         "reachable": True, "aliases": ["GHSA-aaaa-bbbb-cccc"], "package": "golang.org/x/crypto"},
        {"tool": "osv-scanner", "ruleId": "GHSA-aaaa-bbbb-cccc", "level": "warning", "message": "DoS in ssh (osv)",
         "locations": [{"file": "go.mod", "line": 0}], "aliases": ["GO-2026-6355"], "package": "golang.org/x/crypto"},
        {"tool": "grype", "ruleId": "GO-2026-6355-golang.org/x/crypto", "level": "error",
         "message": "A high vulnerability in go-module package: golang.org/x/crypto, version v0.54.0 was found at: /go.mod",
         "locations": [{"file": "/go.mod", "line": 1}]},
        {"tool": "grype", "ruleId": "GHSA-dddd-eeee-ffff-github.com/docker/docker", "level": "warning",
         "message": "A medium vulnerability in go-module package: github.com/docker/docker, version v28.5.2 was found at: /go.mod",
         "locations": [{"file": "/go.mod", "line": 1}]},
    ]

    def advisory_doc(self, dependency_change):
        return {"findings": self.ADVISORIES, "coverage": "partial", "scope": "diff", "since": "origin/main",
                "touched_files": 1, "dependency_change": dependency_change,
                "tools": {"govulncheck": {"status": "ran", "findings": 1}, "osv-scanner": {"status": "ran", "findings": 1},
                          "grype": {"status": "ran", "findings": 2}}}

    def test_one_row_per_vulnerability_across_scanners(self):
        md = pipeline.render_det_summary(self.advisory_doc(True))
        self.assertIn("| govulncheck, grype, osv-scanner | GO-2026-6355 (reachable) | `golang.org/x/crypto` | DoS in ssh |", md)
        self.assertEqual(md.count("GO-2026-6355"), 1, md)
        self.assertIn("| grype | GHSA-dddd-eeee-ffff | `github.com/docker/docker` |", md)
        self.assertNotIn("go.mod:1", md, "grype's line 1 is a placeholder, not a location")

    def test_unreachable_advisories_collapse_unless_dependencies_changed(self):
        md = pipeline.render_det_summary(self.advisory_doc(False))
        head, marker, tail = md.partition("Dependency advisories without a known call path from ox code: 1")
        self.assertTrue(marker, md)
        self.assertIn("GO-2026-6355 (reachable)", head, "a reachable advisory always stays visible")
        self.assertIn("github.com/docker/docker", tail)
        md = pipeline.render_det_summary(self.advisory_doc(True))
        self.assertNotIn("without a known call path", md, "a dependency change shows every advisory")

    def test_det_summary_never_reads_clean_without_coverage(self):
        doc = {"findings": [], "coverage": "none", "scope": "diff", "since": "origin/main", "touched_files": 1,
               "tools": {t: {"status": "skipped", "reason": "not installed"} for t in pipeline.SCANNERS}}
        md = pipeline.render_det_summary(doc)
        self.assertIn("**NO COVERAGE**", md)
        self.assertNotIn("No scanner findings", md)

    def test_sarif_carries_scanner_results_with_their_location(self):
        scan = pipeline.scanner_report_findings(self.DET, [])
        sarif = pipeline.render_sarif([], scan)
        results = {r["ruleId"]: r for r in sarif["runs"][0]["results"]}
        self.assertEqual(results["gosec/G204"]["locations"][0]["physicalLocation"]["region"], {"startLine": 3})
        self.assertNotIn("locations", results["govulncheck/GO-2026-0001"])
        self.assertEqual(results["gosec/G204"]["properties"], {"source": "gosec", "validated": False})


class ValidatorRoutingTest(unittest.TestCase):
    CONFIG = textwrap.dedent(
        """\
        models:
          validator_model: model-default   # most findings
          validator_hard_class_model: model-hard

        hard_classes:
          - daemon-ipc        # peer-cred, payload validation
          - supply-chain

        hunters:
          - cli-input
        """
    )

    def route(self, finding, config=CONFIG):
        with tempfile.TemporaryDirectory() as tmp:
            cfg, f = Path(tmp) / "config.yml", Path(tmp) / "finding"
            if config is not None:
                cfg.write_text(config)
            f.write_text(json.dumps(finding))
            buf = io.StringIO()
            with redirect_stdout(buf):
                pipeline.main(["validator-model", "--config", str(cfg), "--finding", str(f)])
            return buf.getvalue().strip()

    def test_hard_class_gets_the_hard_class_model(self):
        self.assertEqual(self.route({"class": "daemon-ipc"}), "model-hard")
        self.assertEqual(self.route({"class": " Supply-Chain "}), "model-hard")

    def test_other_classes_get_the_default_validator_model(self):
        for finding in ({"class": "cli-input"}, {"class": "daemon-ipc-authz"}, {"class": ""}, {}):
            with self.subTest(finding=finding):
                self.assertEqual(self.route(finding), "model-default")

    def test_quoted_items_and_flow_lists_are_parsed_as_yaml(self):
        """Valid YAML spellings of hard_classes must not silently fall back to the default."""
        for config in ('hard_classes:\n  - "daemon-ipc"\n  - \'supply-chain\'\n',
                       "hard_classes: [daemon-ipc, 'supply-chain']\n",
                       'hard_classes: ["daemon-ipc", supply-chain]  # flow style\n'):
            with self.subTest(config=config):
                self.assertEqual(pipeline.config_list_from_text(config, "hard_classes"), ["daemon-ipc", "supply-chain"])
                self.assertEqual(self.route({"class": "daemon-ipc"}, config), "claude-opus-5-5")
        quoted_models = 'models:\n  validator_hard_class_model: "model-hard"\nhard_classes:\n  - daemon-ipc\n'
        self.assertEqual(self.route({"class": "daemon-ipc"}, quoted_models), "model-hard")

    def test_missing_keys_fall_back_to_sonnet_and_opus(self):
        self.assertEqual(self.route({"class": "daemon-ipc"}, "hard_classes:\n  - daemon-ipc\n"), "claude-opus-5-5")
        self.assertEqual(self.route({"class": "daemon-ipc"}, None), "claude-sonnet-5")

    def test_every_hard_class_is_a_class_an_active_hunter_reports(self):
        """The bug this guards: hard_classes named classes no hunter emits, so nothing reached Opus."""
        repo = Path(__file__).resolve().parents[3]
        config = repo / "security/config.yml"
        reported = {}
        for hunter in pipeline.config_list(config, "hunters"):
            playbook = (repo / ".claude/skills/security-review/prompts" / f"hunter-{hunter}.md").read_text()
            reported[hunter] = re.findall(r'"class":\s*"([^"]+)"', playbook)
        hard = pipeline.config_list(config, "hard_classes")
        self.assertTrue(hard, "no hard classes configured: no finding would reach the hard-class model")
        self.assertLessEqual(set(hard), {c for classes in reported.values() for c in classes},
                             f"hard_classes {hard} vs the classes active hunters report {reported}")


class ValidatorResultTest(unittest.TestCase):
    def merge(self, finding, output=None, unvalidated="", model=""):
        with tempfile.TemporaryDirectory() as tmp:
            f, o = Path(tmp) / "finding", Path(tmp) / "output"
            f.write_text(json.dumps(finding))
            if output is not None:
                o.write_text(output)
            args = ["validator-result", "--finding", str(f), "--output", str(o)]
            if unvalidated:
                args += ["--unvalidated", unvalidated]
            if model:
                args += ["--model", model]
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

    def test_records_the_model_that_judged_it(self):
        got = self.merge({"title": "t"}, json.dumps({"class": "daemon-ipc", "verdict": "confirmed"}),
                         model="claude-opus-5-5")
        self.assertEqual(got["validator_model"], "claude-opus-5-5")
        capped = self.merge({"title": "t"}, '{"verdict": "cap", "reason": "x"}', model="claude-opus-5-5")
        self.assertNotIn("validator_model", capped, "no model judged a finding the cap stopped")


if __name__ == "__main__":
    unittest.main()
