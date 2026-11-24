import contextlib
import hashlib
import io
import json
import sys
import tempfile
import tracemalloc
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

    def test_merge_slots_refuses_a_part_with_no_coverage_blocks(self):
        """Failure prevented: a slot that recorded nothing merges as if it had covered its share."""
        for content in ("", "mode: atomic\n"):
            with self.subTest(content=content), tempfile.TemporaryDirectory() as directory:
                parts = Path(directory)
                (parts / "coverage-rest.out").write_text("mode: atomic\nx.go:1.1,2.2 1 1\n", encoding="utf-8")
                (parts / "coverage-ox-1.out").write_text(content, encoding="utf-8")
                args = test_split.argparse.Namespace(split=["./cmd/ox=1"], group=[], parts=str(parts), out=str(parts / "all.out"))
                self.assertEqual(1, test_split.merge_slots(args))
                self.assertFalse((parts / "all.out").exists())


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

    def test_large_stream_has_bounded_memory_and_preserves_failed_artifact(self):
        """Process multi-megabyte shards without retaining their event history."""
        for failed in (False, True):
            with self.subTest(failed=failed):
                events = self.events(failed=failed)
                noise = json.dumps({"Action": "output", "Package": events[0]["Package"], "Output": "noise " * 1024}) + "\n"
                original_hash = hashlib.sha256()
                with self.path.open("wb") as stream:
                    for event in events[:-1]:
                        line = (json.dumps(event) + "\n").encode()
                        stream.write(line)
                        original_hash.update(line)
                    for _ in range(1024):
                        stream.write(noise.encode())
                        original_hash.update(noise.encode())
                    line = (json.dumps(events[-1]) + "\n").encode()
                    stream.write(line)
                    original_hash.update(line)
                self.assertGreater(self.path.stat().st_size, 5 * 1024 * 1024)
                tracemalloc.start()
                try:
                    diagnostic = test_split.finalize_test_events(self.path, failed=failed)
                    _, peak = tracemalloc.get_traced_memory()
                finally:
                    tracemalloc.stop()
                self.assertLess(peak, 2 * 1024 * 1024, "streaming must not retain the whole shard")
                if failed:
                    retained_hash = hashlib.sha256()
                    with self.path.open("rb") as stream:
                        for block in iter(lambda: stream.read(64 * 1024), b""):
                            retained_hash.update(block)
                    self.assertEqual(original_hash.digest(), retained_hash.digest())
                    self.assertIn("DATA RACE", diagnostic)
                    self.assertIn("truncated", diagnostic)
                    self.assertIn(str(self.path), diagnostic)
                else:
                    with self.path.open(encoding="utf-8") as stream:
                        retained = [json.loads(line) for line in stream]
                    self.assertEqual([event for event in events if event["Action"] == "pass"], retained)
                    self.assertEqual("", diagnostic)

    def test_unicode_diagnostic_is_bounded_and_empty_events_do_not_fill_budget(self):
        """Bound console bytes across multibyte output while preserving evidence."""
        events = self.events()
        package = events[0]["Package"]
        events[2:2] = [{"Action": "output", "Package": package, "Output": ""}] * 2048 + [
            {"Action": "output", "Package": package, "Output": "é" * test_split.DIAGNOSTIC_OUTPUT_BYTES},
            {"Action": "output", "Package": package, "Output": "unshown trailing evidence\n"},
        ]
        original = self.write_events(events)
        diagnostic = test_split.finalize_test_events(self.path, failed=True)
        excerpt, notice = diagnostic.split("\n[Diagnostic output truncated", 1)
        self.assertLessEqual(len(excerpt.encode()), test_split.DIAGNOSTIC_OUTPUT_BYTES)
        self.assertIn("full events: " + str(self.path), notice)
        self.assertNotIn("unshown trailing evidence", diagnostic)
        self.assertEqual(original, self.path.read_text(encoding="utf-8"))

    def test_failed_compaction_leaves_original_intact_and_removes_temporary(self):
        """Read, write, or rename errors never replace the full source artifact."""
        for stage in ("read", "write", "replace"):
            with self.subTest(stage=stage):
                original = self.write_events(self.events(failed=False))
                if stage == "read":
                    def interrupted_records(path):
                        """Fail after writing a terminal event into the temporary file."""
                        yield json.dumps({"Action": "pass"}) + "\n", {"Action": "pass"}
                        raise OSError("read failed")
                    patch = mock.patch.object(test_split, "_test_event_records", side_effect=interrupted_records)
                elif stage == "write":
                    create_temporary = tempfile.NamedTemporaryFile

                    def unwritable_temporary(*args, **kwargs):
                        """Inject a real temporary file whose write reports disk failure."""
                        stream = create_temporary(*args, **kwargs)
                        stream.write = mock.Mock(side_effect=OSError("write failed"))
                        return stream
                    patch = mock.patch.object(test_split.tempfile, "NamedTemporaryFile", side_effect=unwritable_temporary)
                else:
                    patch = mock.patch.object(Path, "replace", side_effect=OSError("replace failed"))
                with patch, self.assertRaisesRegex(OSError, stage + " failed"):
                    test_split.finalize_test_events(self.path, failed=False)
                self.assertEqual(original, self.path.read_text(encoding="utf-8"))
                self.assertEqual([self.path], list(self.root.iterdir()))

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
                                            only="rest", go_flags=[], packages=[], coverprofile=str(self.root / "cover.out"), junit=None, timings=str(self.path), tag_only=None, require_tests=False)
        console, errors = io.StringIO(), io.StringIO()
        original_read_text = Path.read_text

        def bounded_reads(path, *read_args, **read_kwargs):
            """Reject whole-file timing reads during finalization and merge."""
            if path == job.timings:
                raise AssertionError("full timing artifacts must be streamed")
            return original_read_text(path, *read_args, **read_kwargs)

        with mock.patch.object(test_split, "build_jobs", return_value=[job]), mock.patch.object(Path, "read_text", new=bounded_reads), \
                contextlib.redirect_stdout(console), contextlib.redirect_stderr(errors):
            result = test_split.run(args)
        self.assertEqual(1, result)
        self.assertIn("Package-level failure context", console.getvalue())
        self.assertIn("WARNING: DATA RACE", console.getvalue())
        self.assertIn("background write at worker.go:42", console.getvalue())
        self.assertIn("1 of 1 processes failed", errors.getvalue())
        retained = [json.loads(line) for line in self.path.read_text(encoding="utf-8").splitlines()]
        self.assertEqual(self.events(), retained, "the existing CI-uploaded timing path includes full failed-shard output")

    def test_finalization_error_collects_other_processes_and_all_artifacts(self):
        """An optional diagnostic failure preserves pass/fail status and merges."""
        for first_failed in (False, True):
            with self.subTest(first_failed=first_failed):
                work = self.root / ("failed-work" if first_failed else "passed-work")
                release = self.root / ("failed-release" if first_failed else "passed-release")
                jobs = [test_split.Job(name, work, name) for name in ("first", "second")]
                producer = self.root / "two-jobs.py"
                producer.write_text(
                    "import json, pathlib, sys, time\n"
                    "failed, wait, release = int(sys.argv[1]), int(sys.argv[2]), pathlib.Path(sys.argv[3])\n"
                    "args = sys.argv[4:]\n"
                    "deadline = time.monotonic() + 5\n"
                    "while wait and not release.exists():\n"
                    "    if time.monotonic() > deadline: sys.exit(9)\n"
                    "    time.sleep(0.005)\n"
                    "events = " + repr(self.events(failed=False)) + "\n"
                    "if failed: events[-1]['Action'] = 'fail'\n"
                    "pathlib.Path(args[args.index('--jsonfile')+1]).write_text(''.join(json.dumps(e)+'\\n' for e in events))\n"
                    "cover = next(a.split('=',1)[1] for a in args if a.startswith('-coverprofile='))\n"
                    "pathlib.Path(cover).write_text('mode: atomic\\nx.go:1.1,1.9 1 1\\n')\n"
                    "name = 'second' if wait else 'first'\n"
                    'xml = f\'<testsuites><testsuite name="{name}" tests="1" failures="{failed}" errors="0" skipped="0" time="0.1"><testcase name="{name}" /></testsuite></testsuites>\'\n'
                    "pathlib.Path(args[args.index('--junitfile')+1]).write_text(xml)\n"
                    "sys.exit(failed)\n", encoding="utf-8")
                for index, job in enumerate(jobs):
                    job.command = test_split.gotestsum_command(
                        [sys.executable, str(producer), str(int(first_failed and index == 0)), str(index), str(release)],
                        job, [], [], ["./internal/daemon"])
                cover = self.root / "merged.cover"
                junit = self.root / "merged.junit.xml"
                args = test_split.argparse.Namespace(go="go", gotestsum="fake", work_dir=str(work), split=[], group=[], weights=None,
                                                    only="rest", go_flags=[], packages=[], coverprofile=str(cover), junit=str(junit), timings=str(self.path), tag_only=None, require_tests=False)
                finalize = test_split.finalize_test_events

                def interrupted_finalization(path, failed):
                    """Fail first finalization while the second process waits on a marker."""
                    if path == jobs[0].timings:
                        self.assertIsNone(jobs[1].process.poll(), "the second process must still be running")
                        release.touch()
                        raise OSError("injected finalization error")
                    return finalize(path, failed)

                console, errors = io.StringIO(), io.StringIO()
                try:
                    with mock.patch.object(test_split, "build_jobs", return_value=jobs), \
                            mock.patch.object(test_split, "finalize_test_events", side_effect=interrupted_finalization), \
                            contextlib.redirect_stdout(console), contextlib.redirect_stderr(errors):
                        result = test_split.run(args)
                    self.assertEqual(int(first_failed), result)
                    self.assertEqual([int(first_failed), 0], [job.process.poll() for job in jobs])
                    self.assertTrue(all(job.handle.closed for job in jobs))
                    self.assertIn("unable to finalize events for first", errors.getvalue())
                    self.assertIn(str(jobs[0].timings), errors.getvalue())
                    self.assertIn("injected finalization error", errors.getvalue())
                    self.assertIn("x.go:1.1,1.9 1 2", cover.read_text(encoding="utf-8"))
                    suites = ET.parse(junit).getroot()
                    self.assertEqual("2", suites.get("tests"))
                    self.assertEqual(str(int(first_failed)), suites.get("failures"))
                    self.assertEqual(["first", "second"], [suite.get("name") for suite in suites])
                    first_events = self.events(failed=first_failed)
                    original = "".join(json.dumps(event) + "\n" for event in first_events).encode()
                    self.assertEqual(original, jobs[0].timings.read_bytes())
                    retained = [json.loads(line) for line in self.path.read_text(encoding="utf-8").splitlines()]
                    self.assertEqual(first_events + [event for event in self.events(failed=False) if event["Action"] == "pass"], retained)
                finally:
                    release.touch()
                    for job in jobs:
                        if job.process is not None:
                            job.process.wait(timeout=5)
                        if job.handle is not None:
                            job.handle.close()


class CoverageOptionalTest(unittest.TestCase):
    """The PR gate runs the fast tier through the same slots, without coverage."""

    def run_slot(self, coverprofile):
        """Run one fake gotestsum process that writes no coverage profile."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            job = test_split.Job("rest", work, "rest")
            producer = root / "gotestsum.py"
            producer.write_text(
                "import pathlib, sys\n"
                "args = sys.argv[1:]\n"
                "pathlib.Path(args[args.index('--jsonfile')+1]).write_text('')\n", encoding="utf-8")
            cover = coverprofile is not None
            job.command = test_split.gotestsum_command([sys.executable, str(producer)], job, [], [], ["./internal/daemon"], cover)
            args = test_split.argparse.Namespace(go="go", gotestsum="fake", work_dir=str(work), split=[], group=[], weights=None,
                                                only="rest", go_flags=[], packages=[],
                                                coverprofile=None if coverprofile is None else str(root / coverprofile),
                                                junit=None, timings=None, tag_only=None, require_tests=False)
            with mock.patch.object(test_split, "build_jobs", return_value=[job]), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as errors:
                result = test_split.run(args)
            return result, errors.getvalue(), (root / coverprofile).exists() if coverprofile else False

    def test_without_coverprofile_a_passing_slot_passes(self):
        """Failure prevented: the fast PR gate fails every slot for lacking a profile it never asked for."""
        result, errors, _ = self.run_slot(None)
        self.assertEqual(0, result, errors)

    def test_with_coverprofile_a_missing_profile_still_fails(self):
        """Failure prevented: making coverage optional lets the full tier lose a slot's coverage silently."""
        result, errors, _ = self.run_slot("cover.out")
        self.assertEqual(1, result)
        self.assertIn("without writing a coverage profile", errors)

    def test_coverage_flag_follows_the_cover_switch(self):
        """Without coverage the go flags pass through untouched; with it, one -coverprofile per process."""
        job = test_split.Job("rest", Path("w"), "rest")
        with_cover = test_split.gotestsum_command(["gotestsum"], job, ["-race"], [], ["./..."])
        without = test_split.gotestsum_command(["gotestsum"], job, ["-race"], [], ["./..."], cover=False)
        self.assertIn(f"-coverprofile={job.cover}", with_cover)
        self.assertFalse(any(arg.startswith("-coverprofile=") for arg in without))
        self.assertEqual(["-race", "./..."], without[without.index("--") + 1:])


class TagOnlyTest(unittest.TestCase):
    """The slow tier runs only what its tag adds, and refuses to run on nothing."""

    def listing(self, plain, tagged, go_adds=None):
        """Fake the file diff; go_adds is what `go test -list` says the tag adds (default: agree with the files)."""
        def fake(go, package, flags):
            return [Path(name) for name in (tagged if "-tags=slow" in flags else plain)]
        patch = mock.patch.object(test_split, "_test_files", side_effect=fake)
        self._go_adds = go_adds
        return patch

    def go_list(self, regex_names):
        adds = regex_names if self._go_adds is None else self._go_adds
        return mock.patch.object(test_split, "_listed_names",
                                 side_effect=lambda go, package, flags, tolerate_excluded=False:
                                 ["TestOrdinary", *adds] if "-tags=slow" in flags else ["TestOrdinary"])

    def test_selects_tests_from_files_only_the_tag_builds(self):
        """Failure prevented: the slow tier re-runs every ordinary test and takes 30 minutes."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "plain_test.go").write_text("func TestOrdinary(t *testing.T) {}\n", encoding="utf-8")
            (root / "slow_test.go").write_text(
                "func TestAlpha(t *testing.T) {}\n"
                "func TestAlpha(t *testing.T) {}\n"
                "func (s *suite) TestMethod(t *testing.T) {}\n"
                "func Testlower(t *testing.T) {}\n"
                "func TestMain(m *testing.M) {}\n"
                "func ExampleNoOutput() {}\n"
                "func helperTest(t *testing.T) {}\n"
                "func FuzzParse(f *testing.F) {}\n"
                "func Test_Underscore(t *testing.T) {}\n"
                "func TestBeta(_ *testing.T) {}\n", encoding="utf-8")
            with self.listing([root / "plain_test.go"], [root / "plain_test.go", root / "slow_test.go"]), \
                    self.go_list(["TestAlpha", "FuzzParse", "Test_Underscore", "TestBeta"]):
                names = test_split.tag_only_tests(["go"], "slow", "./pkg")
        self.assertEqual(["TestAlpha", "FuzzParse", "Test_Underscore", "TestBeta"], names)

    def test_regex_and_go_disagreeing_is_an_error_naming_both_sides(self):
        """Failure prevented: a test shape the regex misses is silently dropped from the slow tier."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "slow_test.go").write_text("func TestAlpha(t *testing.T) {}\n", encoding="utf-8")
            for go_adds, missing, extra in ((["TestAlpha", "TestGenerated"], "TestGenerated", "[]"),
                                            ([], "[]", "TestAlpha")):
                with self.subTest(go_adds=go_adds), self.listing([], [root / "slow_test.go"], go_adds), self.go_list([]):
                    with self.assertRaises(SystemExit) as error:
                        test_split.tag_only_tests(["go"], "slow", "./pkg")
                    self.assertIn("disagrees with `go test -list`", str(error.exception))
                    self.assertIn(missing, str(error.exception))
                    self.assertIn(extra, str(error.exception))

    def test_discovery_uses_the_tiers_build_context(self):
        """Failure prevented: a file gated on `slow && race` is absent from the selection the slow tier would run."""
        seen = []

        def fake(go, package, flags):
            seen.append(flags)
            return []
        with mock.patch.object(test_split, "_test_files", side_effect=fake):
            with self.assertRaises(SystemExit):
                test_split.tag_only_tests(["go"], "slow", "./pkg", ["-race", "-tags=extra"])
        self.assertEqual([["-race", "-tags=extra"], ["-race", "-tags=extra,slow"]], seen)
        self.assertEqual(["-race", "-tags=slow"], test_split._with_tag(["-race"], "slow"))
        self.assertEqual(["-race"], test_split._without_tag(["-race", "-tags=slow"], "slow"))
        self.assertEqual(["-tags=slow", "-race"], test_split.build_context(["-tags=slow", "-race", "-count=1", "-p", "1"]))

    def test_package_whose_every_file_needs_the_tag_is_planned(self):
        """Failure prevented: package discovery without the tag aborts the slot before any test is selected."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text("module example.com/m\n\ngo 1.21\n", encoding="utf-8")
            for name, files in {"gated": {"only_test.go": "//go:build slow\n\npackage gated\n\nimport \"testing\"\n\nfunc TestGated(t *testing.T) {}\n"},
                                "mixed": {"plain_test.go": "package mixed\n\nimport \"testing\"\n\nfunc TestPlain(t *testing.T) {}\n",
                                          "slow_test.go": "//go:build slow\n\npackage mixed\n\nimport \"testing\"\n\nfunc TestMixedSlow(t *testing.T) {}\n"}}.items():
                (root / name).mkdir()
                for filename, content in files.items():
                    (root / name / filename).write_text(content, encoding="utf-8")
            go = ["go", "-C", str(root)]
            jobs = test_split.build_jobs(test_split.Layout([], []), "rest", go, ["gotestsum"], ["-tags=slow", "-count=1"],
                                         ["./gated", "./mixed"], {}, root / "work", False, "slow")
            self.assertEqual(1, len(jobs))
            self.assertEqual({"example.com/m/gated": ["TestGated"], "example.com/m/mixed": ["TestMixedSlow"]}, jobs[0].expected)
            # named (split) packages resolve the same way
            jobs = test_split.build_jobs(test_split.Layout(["./gated=1"], []), "gated-1", go, ["gotestsum"], ["-tags=slow"],
                                         ["./gated", "./mixed"], {}, root / "work", False, "slow")
            self.assertEqual({"example.com/m/gated": ["TestGated"]}, jobs[0].expected)

    def test_package_without_tagged_files_is_an_error(self):
        """Failure prevented: dropping the build tag turns the stage into a green no-op."""
        with self.listing(["a_test.go"], ["a_test.go"]):
            with self.assertRaises(SystemExit) as error:
                test_split.tag_only_tests(["go"], "slow", "./pkg")
        self.assertIn("no test files that build only with -tags=slow", str(error.exception))

    def test_tagged_files_without_tests_are_an_error(self):
        with tempfile.TemporaryDirectory() as directory:
            helper = Path(directory) / "helper_test.go"
            helper.write_text("func helper() {}\n", encoding="utf-8")
            with self.listing([], [helper]):
                with self.assertRaises(SystemExit) as error:
                    test_split.tag_only_tests(["go"], "slow", "./pkg")
        self.assertIn("no tests in its -tags=slow files", str(error.exception))


class RequireTestsTest(unittest.TestCase):
    """`go test` exits 0 on a -run that matches nothing; a release shard must not."""

    def run_slot(self, events, expected, jobs=1):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            producer = root / "gotestsum.py"
            producer.write_text(
                "import pathlib, sys\n"
                "args = sys.argv[1:]\n"
                f"pathlib.Path(args[args.index('--jsonfile')+1]).write_text({events!r})\n", encoding="utf-8")
            planned = []
            for index in range(jobs):
                job = test_split.Job("slot", work, f"slot{index}")
                job.command = test_split.gotestsum_command([sys.executable, str(producer)], job, [], [], ["./p"], False)
                job.expected = expected
                planned.append(job)
            args = test_split.argparse.Namespace(go="go", gotestsum="fake", work_dir=str(work), split=[], group=[], weights=None,
                                                only="slot", go_flags=[], packages=[], coverprofile=None, junit=None,
                                                timings=None, tag_only="slow", require_tests=True)
            with mock.patch.object(test_split, "build_jobs", return_value=planned), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as errors:
                result = test_split.run(args)
            return result, errors.getvalue()

    @staticmethod
    def event(test, action):
        return json.dumps({"Action": action, "Package": "p", "Test": test}) + "\n"

    def test_slot_that_plans_no_process_fails(self):
        result, errors = self.run_slot("", {}, jobs=0)
        self.assertEqual(1, result)
        self.assertIn("planned no tests", errors)

    def test_exit_zero_with_no_test_events_fails(self):
        """Failure prevented: a -run that matches nothing passes the shard."""
        result, errors = self.run_slot("", {"p": ["TestA"]})
        self.assertEqual(1, result)
        self.assertIn("no test reported a result", errors)

    def test_all_skipped_shard_passes_locally_and_fails_on_ci(self):
        """Failure prevented: OX_SKIP_DOCKER=1 fails a local shard whose every test reports a skip; CI must not pass on skips alone."""
        events = self.event("TestA", "skip") + self.event("TestB", "skip")
        with mock.patch.dict("os.environ", {"CI": ""}):
            result, errors = self.run_slot(events, {"p": ["TestA", "TestB"]})
        self.assertEqual(0, result, errors)
        with mock.patch.dict("os.environ", {"CI": "true"}):
            result, errors = self.run_slot(events, {"p": ["TestA", "TestB"]})
        self.assertEqual(1, result)
        self.assertIn("every reported test was skipped", errors)

    def test_an_owed_test_that_never_reported_fails(self):
        result, errors = self.run_slot(self.event("TestA", "pass"), {"p": ["TestA", "TestB"]})
        self.assertEqual(1, result)
        self.assertIn("1 of 2 expected tests in p never reported (first: TestB)", errors)

    def test_every_owed_test_reporting_passes_and_a_skip_counts_as_reported(self):
        events = self.event("TestA", "pass") + self.event("TestB", "skip") + self.event("TestB/sub", "pass")
        result, errors = self.run_slot(events, {"p": ["TestA", "TestB"]})
        self.assertEqual(0, result, errors)


class WeightsTest(unittest.TestCase):
    def test_repository_weights_file_loads(self):
        """Both tiers' committed weights files load and keep only tests at or over the floor."""
        for name in ("test-split-weights.json", "test-split-weights-fast.json"):
            with self.subTest(name=name):
                path = Path(__file__).resolve().parents[1] / ".config" / name
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
