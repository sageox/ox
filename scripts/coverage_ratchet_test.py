import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from datetime import date
from pathlib import Path
from unittest import mock

import coverage_ratchet


class CoverageRatchetTest(unittest.TestCase):
    def write_profile(self, directory: Path, body: str) -> Path:
        path = directory / "coverage.out"
        path.write_text("mode: atomic\n" + body, encoding="utf-8")
        return path

    def write_config(self, directory: Path, minimum: float) -> Path:
        path = directory / "ratchets.json"
        path.write_text(
            json.dumps(
                {
                    "module": "example.com/project/",
                    "packages": [{"path": "internal/risk/", "minimum": minimum}],
                }
            ),
            encoding="utf-8",
        )
        return path

    def test_aggregates_statement_weight_and_duplicate_blocks(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            directory = Path(raw_directory)
            profile = self.write_profile(
                directory,
                "example.com/project/internal/risk/a.go:1.1,2.1 3 1\n"
                "example.com/project/internal/risk/a.go:3.1,4.1 1 0\n"
                "example.com/project/internal/risk/a.go:1.1,2.1 3 0\n"
                "example.com/project/internal/other/b.go:1.1,2.1 100 1\n",
            )

            files = coverage_ratchet.load_profile(profile, "example.com/project/")
            result = coverage_ratchet.package_coverage(files, "internal/risk/")

            self.assertEqual(4, result.statements)
            self.assertEqual(3, result.covered)
            self.assertEqual(75.0, result.percent)

    def test_reports_below_floor_and_missing_package(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            directory = Path(raw_directory)
            profile = self.write_profile(
                directory,
                "example.com/project/internal/risk/a.go:1.1,2.1 4 1\n"
                "example.com/project/internal/risk/a.go:3.1,4.1 6 0\n",
            )
            config = self.write_config(directory, 50.0)

            with redirect_stdout(io.StringIO()):
                failures = coverage_ratchet.check(profile, config)
            self.assertEqual(
                ["internal/risk/: 40.0% is below the 50.0% floor"], failures
            )

            config.write_text(
                json.dumps(
                    {
                        "module": "example.com/project/",
                        "packages": [{"path": "missing/", "minimum": 0}],
                    }
                ),
                encoding="utf-8",
            )
            with redirect_stdout(io.StringIO()):
                failures = coverage_ratchet.check(profile, config)
            self.assertEqual(["missing/: no statements matched the coverage profile"], failures)

    def test_can_exclude_subpackages(self):
        files = {
            "internal/risk/a.go": coverage_ratchet.Coverage(10, 5),
            "internal/risk/sub/b.go": coverage_ratchet.Coverage(90, 90),
        }

        recursive = coverage_ratchet.package_coverage(files, "internal/risk/")
        direct = coverage_ratchet.package_coverage(
            files, "internal/risk/", recursive=False
        )

        self.assertEqual(95.0, recursive.percent)
        self.assertEqual(50.0, direct.percent)

    def test_rejects_malformed_profile(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            directory = Path(raw_directory)
            profile = self.write_profile(directory, "not a coverage block\n")
            with self.assertRaisesRegex(ValueError, "malformed coverage block"):
                coverage_ratchet.load_profile(profile, "example.com/project/")

    def test_parses_added_lines_from_multiple_hunks_and_files(self):
        diff = """diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -2,0 +3,2 @@
+one
+two
@@ -8 +10 @@
-old
+new
diff --git a/deleted.go b/deleted.go
--- a/deleted.go
+++ /dev/null
@@ -1 +0,0 @@
-gone
"""

        self.assertEqual(
            {"a.go": {3, 4, 10}}, coverage_ratchet.parse_changed_lines(diff)
        )

    def test_changed_line_coverage_fails_below_floor_and_unprofiled_files(self):
        blocks = {
            ("internal/risk/a.go", 10, 1, 12, 1): coverage_ratchet.CoverageBlock(
                "internal/risk/a.go", 10, 12, 3, True
            ),
            ("internal/risk/a.go", 20, 1, 20, 2): coverage_ratchet.CoverageBlock(
                "internal/risk/a.go", 20, 20, 2, False
            ),
        }
        settings = {
            "minimum": 90,
            "excluded_paths": ["**/*_generated.go"],
            "exceptions": [],
        }

        result, failures, _ = coverage_ratchet.evaluate_changed_lines(
            blocks,
            {
                "internal/risk/a.go": {11, 20},
                "internal/risk/types.go": {1},
                "internal/new/file.go": {1},
                "internal/risk/a_test.go": {1},
                "internal/risk/schema_generated.go": {1},
            },
            settings,
        )

        self.assertEqual(5, result.statements)
        self.assertEqual(3, result.covered)
        self.assertEqual(
            [
                "internal/new/file.go: changed production file has no coverage data",
                "changed executable statements: 60.0% is below the 90.0% floor",
            ],
            failures,
        )

    def test_declaration_only_file_skips_but_a_function_still_fails(self):
        """A const-only package can never be profiled; a real function still can.

        `internal/constants/` holds nothing but const blocks, so `go tool cover`
        emits zero blocks for it and the package-level fallback -- "does a
        sibling in this package have coverage?" -- is structurally always no.
        That made every edit to the package fail the gate: PR #909 merged with
        this check red for exactly this reason.
        """
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            (root / "internal/constants").mkdir(parents=True)
            (root / "internal/constants/agent.go").write_text(
                'package constants\n\nconst Prime = "ox agent prime"\n',
                encoding="utf-8",
            )
            (root / "internal/live").mkdir(parents=True)
            (root / "internal/live/run.go").write_text(
                "package live\n\nfunc Run() error { return nil }\n",
                encoding="utf-8",
            )
            # Go permits whitespace before a top-level declaration, and a
            # package-level func literal is a coverable body with no `func`
            # at line start. Neither may be mistaken for declaration-only.
            (root / "internal/live/indented.go").write_text(
                "package live\n\n  func Indented() {}\n",
                encoding="utf-8",
            )
            (root / "internal/live/literal.go").write_text(
                "package live\n\nvar Handler = func() {}\n",
                encoding="utf-8",
            )
            settings = {"minimum": 90, "excluded_paths": [], "exceptions": []}

            with mock.patch("coverage_ratchet.Path", side_effect=lambda p: root / p):
                _, failures, notices = coverage_ratchet.evaluate_changed_lines(
                    {},
                    {
                        "internal/constants/agent.go": {3},
                        "internal/live/run.go": {3},
                        "internal/live/indented.go": {3},
                        "internal/live/literal.go": {3},
                    },
                    settings,
                )

        self.assertIn(
            "SKIP internal/constants/agent.go: declaration-only file"
            " has no coverable statements",
            notices,
        )
        self.assertEqual(
            [
                "internal/live/indented.go: changed production file has no coverage data",
                "internal/live/literal.go: changed production file has no coverage data",
                "internal/live/run.go: changed production file has no coverage data",
            ],
            failures,
        )

    @mock.patch("coverage_ratchet.subprocess.run")
    def test_changed_lines_diff_uses_merge_base(self, run):
        run.side_effect = [
            mock.Mock(stdout="abc123\n"),
            mock.Mock(
                stdout="""diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -0,0 +1 @@
+package a
"""
            ),
        ]

        changed = coverage_ratchet.git_changed_lines("origin/main")

        self.assertEqual({"a.go": {1}}, changed)
        self.assertEqual(
            ["git", "merge-base", "origin/main", "HEAD"],
            run.call_args_list[0].args[0],
        )
        self.assertEqual("abc123", run.call_args_list[1].args[0][4])

    def test_changed_line_exception_is_reviewable_and_expires(self):
        settings = {
            "minimum": 90,
            "exceptions": [
                {
                    "path": "internal/legacy/*.go",
                    "reason": "Covered by an external protocol conformance gate",
                    "expires": "2026-09-30",
                }
            ],
        }
        changed = {"internal/legacy/bridge.go": {10}}

        result, failures, notices = coverage_ratchet.evaluate_changed_lines(
            {}, changed, settings, today=date(2026, 8, 31)
        )
        self.assertEqual(0, result.statements)
        self.assertEqual([], failures)
        self.assertEqual(1, len(notices))
        self.assertIn("external protocol conformance gate", notices[0])

        with self.assertRaisesRegex(ValueError, "expired"):
            coverage_ratchet.evaluate_changed_lines(
                {}, changed, settings, today=date(2026, 10, 1)
            )

    def test_invalid_exception_fails_even_without_changed_files(self):
        settings = {
            "minimum": 90,
            "exceptions": [{"path": "internal/legacy/*.go", "reason": ""}],
        }
        with self.assertRaisesRegex(ValueError, "require path"):
            coverage_ratchet.evaluate_changed_lines(
                {}, {}, settings, today=date(2026, 8, 31)
            )

    def test_rejects_configs_that_can_silently_disable_ratchets(self):
        base = {
            "module": "example.com/project/",
            "packages": [
                {
                    "path": "internal/risk/",
                    "minimum": 50,
                    "recursive": False,
                    "reason": "risk boundary",
                }
            ],
            "changed_line_coverage": {
                "minimum": 90,
                "excluded_paths": [],
                "exceptions": [],
            },
        }
        invalid_mutations = [
            ("empty package list", lambda c: c.update(packages=[]), "packages must be a non-empty array"),
            ("negative floor", lambda c: c["packages"][0].update(minimum=-1), "must be between 0 and 100"),
            ("non-finite floor", lambda c: c["changed_line_coverage"].update(minimum=float("nan")), "must be between 0 and 100"),
            ("string false", lambda c: c["packages"][0].update(recursive="false"), "recursive must be boolean"),
            ("string exclusions", lambda c: c["changed_line_coverage"].update(excluded_paths="**"), "excluded_paths must be a string array"),
            ("blanket exclusion", lambda c: c["changed_line_coverage"].update(excluded_paths=["**"]), "cannot exclude all files"),
            ("blanket Go exclusion", lambda c: c["changed_line_coverage"].update(excluded_paths=["*.go"]), "cannot exclude all files"),
            ("recursive Go exclusion", lambda c: c["changed_line_coverage"].update(excluded_paths=["**/*.go"]), "cannot exclude all files"),
            ("union exclusion", lambda c: c["changed_line_coverage"].update(excluded_paths=["root.go", "cmd/**", "internal/**"]), "cannot exclude all files"),
        ]

        for name, mutate, want_error in invalid_mutations:
            with self.subTest(name=name):
                config = json.loads(json.dumps(base))
                mutate(config)
                with self.assertRaisesRegex(ValueError, want_error):
                    coverage_ratchet.validate_config(
                        config, require_changed_lines=True
                    )

    def test_rejects_unknown_coverage_mode(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            profile = Path(raw_directory) / "coverage.out"
            profile.write_text("mode: forged\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "invalid Go coverage mode"):
                coverage_ratchet.load_profile(profile, "example.com/project/")

    def test_provenance_binds_profile_to_production_source_contents(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            root = Path(raw_directory)
            source = root / "internal" / "risk" / "risk.go"
            source.parent.mkdir(parents=True)
            source.write_text("package risk\n\nfunc Risk() {}\n", encoding="utf-8")
            profile = self.write_profile(
                root,
                "example.com/project/internal/risk/risk.go:3.1,3.15 1 1\n",
            )
            provenance = root / "coverage.provenance.json"

            with mock.patch(
                "coverage_ratchet.evidence_paths", return_value=[source]
            ), mock.patch("coverage_ratchet.subprocess.run") as run:
                run.return_value = mock.Mock(stdout="abc123\n")
                coverage_ratchet.write_provenance(profile, provenance, root)
                coverage_ratchet.verify_provenance(profile, provenance, root)

                source.write_text(
                    "package risk\n\nfunc Risk() { panic(\"changed\") }\n",
                    encoding="utf-8",
                )
                with self.assertRaisesRegex(ValueError, "code, tests, dependencies"):
                    coverage_ratchet.verify_provenance(profile, provenance, root)

    def test_provenance_rejects_substituted_profile(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            root = Path(raw_directory)
            source = root / "risk.go"
            source.write_text("package risk\n", encoding="utf-8")
            profile = self.write_profile(
                root,
                "example.com/project/risk.go:1.1,1.13 1 1\n",
            )
            provenance = root / "coverage.provenance.json"

            with mock.patch(
                "coverage_ratchet.evidence_paths", return_value=[source]
            ), mock.patch("coverage_ratchet.subprocess.run") as run:
                run.return_value = mock.Mock(stdout="abc123\n")
                coverage_ratchet.write_provenance(profile, provenance, root)
                profile.write_text(
                    "mode: atomic\nexample.com/project/risk.go:1.1,1.13 1 0\n",
                    encoding="utf-8",
                )
                with self.assertRaisesRegex(ValueError, "content does not match"):
                    coverage_ratchet.verify_provenance(profile, provenance, root)

    def test_provenance_rejects_changed_commit_identity(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            root = Path(raw_directory)
            source = root / "risk.go"
            source.write_text("package risk\n", encoding="utf-8")
            profile = self.write_profile(
                root,
                "example.com/project/risk.go:1.1,1.13 1 1\n",
            )
            provenance = root / "coverage.provenance.json"

            with mock.patch(
                "coverage_ratchet.evidence_paths", return_value=[source]
            ), mock.patch("coverage_ratchet.subprocess.run") as run:
                run.return_value = mock.Mock(stdout="first\n")
                coverage_ratchet.write_provenance(profile, provenance, root)
                run.return_value = mock.Mock(stdout="second\n")
                with self.assertRaisesRegex(ValueError, "git HEAD changed"):
                    coverage_ratchet.verify_provenance(profile, provenance, root)

    def test_missing_provenance_fails_closed(self):
        with tempfile.TemporaryDirectory() as raw_directory:
            root = Path(raw_directory)
            profile = self.write_profile(
                root,
                "example.com/project/risk.go:1.1,1.13 1 1\n",
            )
            with self.assertRaises(FileNotFoundError):
                coverage_ratchet.verify_provenance(
                    profile, root / "missing.provenance.json", root
                )


if __name__ == "__main__":
    unittest.main()
