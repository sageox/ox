import contextlib
import io
import json
import sys
import tempfile
import unittest
from unittest import mock
import xml.etree.ElementTree as ET
from pathlib import Path

import test_split


class PlanTest(unittest.TestCase):
    def test_shards_are_the_run_order_cut_into_slices(self):
        names = [f"TestCase{i}" for i in range(1000)]
        shards = test_split.plan(names, {"TestCase7": 60.0, "TestCase900": 30.0}, 4)
        self.assertEqual(4, len(shards))
        self.assertEqual(names, [name for shard in shards for name in shard])

    def test_cuts_balance_time_not_count(self):
        weights = {"TestA": 60.0, "TestC": 30.0, "TestB": 60.0, "TestD": 30.0}
        shards = test_split.plan(list(weights), weights, 2)
        self.assertEqual([["TestA", "TestC"], ["TestB", "TestD"]], shards)

    def test_unlisted_tests_cost_the_recorded_average(self):
        names = ["TestHeavy"] + [f"TestLight{i}" for i in range(100)]
        # At the 0.05s default the heavy test outweighs all 100 others; at the
        # recorded 0.3s average they outweigh it, so the cut moves.
        self.assertEqual(1, len(test_split.plan(names, {"TestHeavy": 10.0}, 2)[0]))
        self.assertGreater(len(test_split.plan(names, {"TestHeavy": 10.0}, 2, default_seconds=0.3)[0]), 1)

    def test_light_tests_split_evenly(self):
        names = [f"TestLight{i}" for i in range(9)]
        self.assertEqual([3, 3, 3], [len(shard) for shard in test_split.plan(names, {}, 3)])

    def test_plan_is_deterministic(self):
        names = [f"Test{i}" for i in range(200)]
        weights = {"Test3": 5.0, "Test50": 2.0}
        self.assertEqual(test_split.plan(names, weights, 4), test_split.plan(list(names), weights, 4))

    def test_unknown_tests_still_run_and_no_shard_is_empty(self):
        self.assertEqual([["TestNewlyAdded"]], test_split.plan(["TestNewlyAdded"], {"TestDeleted": 9.0}, 3))

    def test_rejects_duplicate_names(self):
        with self.assertRaises(ValueError):
            test_split.plan(["TestA", "TestA"], {}, 2)

    def test_rejects_run_pattern_past_the_argv_limit(self):
        names = [f"Test{'x' * 200}{i}" for i in range(1000)]
        with self.assertRaises(ValueError):
            test_split.plan(names, {}, 1)
        self.assertEqual(3, len(test_split.plan(names, {}, 3)))

    def test_rejects_zero_shards(self):
        with self.assertRaises(ValueError):
            test_split.plan(["TestA"], {}, 0)


class ParseListTest(unittest.TestCase):
    def test_keeps_runnable_names_and_drops_noise(self):
        output = "TestOne\nExampleTwo\nFuzzThree\nBenchmarkFour\nTestMain says hi\nok  \tgithub.com/x\t0.01s\n"
        self.assertEqual(["TestOne", "ExampleTwo", "FuzzThree"], test_split.parse_list_output(output))


class MergeTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.root = Path(self.directory.name)

    def tearDown(self):
        self.directory.cleanup()

    def write(self, name: str, body: str) -> Path:
        path = self.root / name
        path.write_text(body, encoding="utf-8")
        return path

    def test_cover_sums_block_counts(self):
        a = self.write("a.cov", "mode: atomic\nx.go:1.1,2.2 1 0\nx.go:3.1,4.2 2 3\n")
        b = self.write("b.cov", "mode: atomic\nx.go:1.1,2.2 1 5\ny.go:1.1,1.9 1 0\n")
        out = self.root / "merged.cov"
        test_split.merge_cover([a, b], out)
        self.assertEqual(
            "mode: atomic\nx.go:1.1,2.2 1 5\nx.go:3.1,4.2 2 3\ny.go:1.1,1.9 1 0\n",
            out.read_text(encoding="utf-8"),
        )

    def test_cover_rejects_mixed_modes(self):
        a = self.write("a.cov", "mode: atomic\n")
        b = self.write("b.cov", "mode: set\n")
        with self.assertRaises(ValueError):
            test_split.merge_cover([a, b], self.root / "out.cov")

    def test_junit_keeps_every_suite_and_sums_totals(self):
        a = self.write("a.xml", '<testsuites><testsuite name="p1" tests="2" failures="1" errors="0" time="1.5"/></testsuites>')
        b = self.write("b.xml", '<testsuites><testsuite name="p2" tests="3" failures="0" errors="0" time="2.0"/></testsuites>')
        out = self.root / "merged.xml"
        test_split.merge_junit([a, b], out)
        root = ET.parse(out).getroot()
        self.assertEqual(["p1", "p2"], [suite.get("name") for suite in root.iter("testsuite")])
        self.assertEqual(("5", "1", "3.500"), (root.get("tests"), root.get("failures"), root.get("time")))


class LayoutTest(unittest.TestCase):
    def layout(self):
        return test_split.Layout(["./cmd/ox=3", "./internal/daemon=2"], ["heavy=./internal/ledger,./internal/daemon/agentwork"])

    def test_slots_cover_rest_groups_and_every_slice(self):
        self.assertEqual(["rest", "heavy", "ox-1", "ox-2", "ox-3", "daemon-1", "daemon-2"], self.layout().slots())

    def test_rejects_malformed_specs_and_colliding_slots(self):
        for splits, groups in ((["./cmd/ox"], []), (["./cmd/ox=0"], []), ([], ["heavy"]), (["./a/ox=1", "./b/ox=1"], [])):
            with self.subTest(splits=splits, groups=groups):
                with self.assertRaises(ValueError):
                    test_split.Layout(splits, groups).slots()

    def test_merge_slots_refuses_a_missing_or_unexpected_part(self):
        with tempfile.TemporaryDirectory() as directory:
            parts = Path(directory)
            for slot in ("rest", "ox-1"):
                (parts / f"coverage-{slot}.out").write_text("mode: atomic\nx.go:1.1,2.2 1 1\n", encoding="utf-8")
            args = test_split.argparse.Namespace(split=["./cmd/ox=2"], group=[], parts=str(parts), out=str(parts / "all.out"))
            self.assertEqual(1, test_split.merge_slots(args))
            (parts / "coverage-ox-2.out").write_text("mode: atomic\nx.go:1.1,2.2 1 2\n", encoding="utf-8")
            self.assertEqual(0, test_split.merge_slots(args))
            self.assertEqual("mode: atomic\nx.go:1.1,2.2 1 4\n", (parts / "all.out").read_text(encoding="utf-8"))
            (parts / "coverage-ox-9.out").write_text("mode: atomic\n", encoding="utf-8")
            self.assertEqual(1, test_split.merge_slots(args))


class DiagnosticArtifactsTest(unittest.TestCase):
    """Exercise the package-only failure that lean summaries used to hide."""

    def setUp(self):
        """Allocate isolated artifact paths for each diagnostic scenario."""
        self.directory = tempfile.TemporaryDirectory()
        self.root = Path(self.directory.name)
        self.path = self.root / "test-timings.json"

    def tearDown(self):
        """Remove every synthetic test artifact."""
        self.directory.cleanup()

    def events(self, failed=True):
        """Model a passed test whose output contains a later package failure."""
        package = "github.com/sageox/ox/internal/daemon"
        return [
            {"Action": "run", "Package": package, "Test": "TestOne"},
            {"Action": "output", "Package": package, "Test": "TestOne", "Output": "=== RUN   TestOne\n"},
            {"Action": "output", "Package": package, "Test": "TestOne", "Output": "WARNING: DATA RACE\nbackground write at worker.go:42\n"},
            {"Action": "pass", "Package": package, "Test": "TestOne", "Elapsed": 0.2},
            {"Action": "output", "Package": package, "Output": "testing: race detected outside of test execution\n"},
            {"Action": "fail" if failed else "pass", "Package": package, "Elapsed": 0.3},
        ]

    def write_events(self, events):
        """Serialize the same full JSON stream gotestsum writes."""
        body = "".join(json.dumps(event) + "\n" for event in events)
        self.path.write_text(body, encoding="utf-8")
        return body

    def test_all_individual_tests_pass_but_package_failure_keeps_hidden_race(self):
        """Keep hidden passed-test output in both the artifact and diagnosis."""
        events = self.events()
        original = self.write_events(events)
        diagnostic = test_split.finalize_test_events(self.path, failed=True)
        self.assertEqual(original, self.path.read_text(encoding="utf-8"))
        self.assertIn("WARNING: DATA RACE", diagnostic)
        self.assertIn("background write at worker.go:42", diagnostic)
        self.assertIn("race detected outside of test execution", diagnostic)
        self.assertNotIn("=== RUN", diagnostic)
        self.assertFalse(any(event["Action"] == "fail" and event.get("Test") for event in events))

    def test_passed_shard_returns_to_lean_timing_events(self):
        """Passing shards upload the original terminal-event-only format."""
        events = self.events(failed=False)
        self.write_events(events)
        diagnostic = test_split.finalize_test_events(self.path, failed=False)
        retained = [json.loads(line) for line in self.path.read_text(encoding="utf-8").splitlines()]
        self.assertEqual([event for event in events if event["Action"] == "pass"], retained)
        self.assertEqual("", diagnostic)
        self.assertNotIn("WARNING", self.path.read_text(encoding="utf-8"))

    def test_failed_group_prints_only_the_package_without_a_failed_test(self):
        """Avoid replaying passed packages or ordinary assertion summaries."""
        events = self.events() + [
            {"Action": "output", "Package": "passed", "Output": "unrelated passing-package output\n"},
            {"Action": "pass", "Package": "passed"},
            {"Action": "output", "Package": "assertion", "Test": "TestBad", "Output": "ordinary assertion output\n"},
            {"Action": "fail", "Package": "assertion", "Test": "TestBad"},
            {"Action": "fail", "Package": "assertion"},
        ]
        original = self.write_events(events)
        diagnostic = test_split.finalize_test_events(self.path, failed=True)
        self.assertIn("DATA RACE", diagnostic)
        self.assertNotIn("unrelated passing-package", diagnostic)
        self.assertNotIn("ordinary assertion", diagnostic)
        self.assertEqual(original, self.path.read_text(encoding="utf-8"))

    def test_partial_failed_stream_is_preserved_for_postmortem(self):
        """A producer crash must not destroy its incomplete final record."""
        original = self.write_events(self.events()) + '{"Action":"output"'
        self.path.write_text(original, encoding="utf-8")
        self.assertIn("DATA RACE", test_split.finalize_test_events(self.path, failed=True))
        self.assertEqual(original, self.path.read_text(encoding="utf-8"))

    def test_missing_events_leave_the_original_process_failure_available(self):
        """Compile failures may produce no JSON file to compact or diagnose."""
        self.assertEqual("", test_split.finalize_test_events(self.path, failed=True))
        self.assertFalse(self.path.exists())

    def test_failed_process_uploads_full_stream_and_prints_hidden_context(self):
        """Run a fake gotestsum process through finalization and artifact merge."""
        work = self.root / "work"
        job = test_split.Job("daemon-1", work, "daemon-1")
        producer = self.root / "gotestsum.py"
        producer.write_text(
            "import json, pathlib, sys\n"
            "args = sys.argv[1:]\n"
            "events = " + repr(self.events()) + "\n"
            "timing_only = '--jsonfile-timing-events' in args\n"
            "flag = '--jsonfile-timing-events' if timing_only else '--jsonfile'\n"
            "if timing_only: events = [e for e in events if e['Action'] in {'pass', 'fail', 'skip'}]\n"
            "pathlib.Path(args[args.index(flag)+1]).write_text(''.join(json.dumps(e)+'\\n' for e in events))\n"
            "cover = next(a.split('=',1)[1] for a in args if a.startswith('-coverprofile='))\n"
            "pathlib.Path(cover).write_text('mode: atomic\\nx.go:1.1,1.9 1 1\\n')\n"
            "print('FAIL package summary omits passed-test output')\n"
            "sys.exit(1)\n", encoding="utf-8")
        job.command = test_split.gotestsum_command([sys.executable, str(producer)], job, [], [], ["./internal/daemon"])
        args = test_split.argparse.Namespace(go="go", gotestsum="fake", work_dir=str(work), split=[], group=[], weights=None,
                                            only="rest", go_flags=[], packages=[], coverprofile=str(self.root / "cover.out"), junit=None, timings=str(self.path))
        console, errors = io.StringIO(), io.StringIO()
        with mock.patch.object(test_split, "build_jobs", return_value=[job]), contextlib.redirect_stdout(console), contextlib.redirect_stderr(errors):
            result = test_split.run(args)
        self.assertEqual(1, result)
        self.assertIn("Package-level failure context", console.getvalue())
        self.assertIn("WARNING: DATA RACE", console.getvalue())
        self.assertIn("background write at worker.go:42", console.getvalue())
        self.assertIn("1 of 1 processes failed", errors.getvalue())
        retained = [json.loads(line) for line in self.path.read_text(encoding="utf-8").splitlines()]
        self.assertEqual(self.events(), retained, "the existing CI-uploaded timing path includes full failed-shard output")


class WeightsTest(unittest.TestCase):
    def test_repository_weights_file_loads(self):
        path = Path(__file__).resolve().parents[1] / ".config/test-split-weights.json"
        weights = test_split.load_weights(path)
        self.assertIn("github.com/sageox/ox/cmd/ox", weights)
        cmd_ox = weights["github.com/sageox/ox/cmd/ox"]
        self.assertTrue(all(seconds >= test_split.WEIGHT_FLOOR_SECONDS for seconds in cmd_ox.tests.values()))
        self.assertLess(cmd_ox.default_seconds, test_split.WEIGHT_FLOOR_SECONDS)

    def test_rejects_unknown_version(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "w.json"
            path.write_text(json.dumps({"version": 2, "packages": {}}), encoding="utf-8")
            with self.assertRaises(ValueError):
                test_split.load_weights(path)


if __name__ == "__main__":
    unittest.main()
