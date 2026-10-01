import json
import tempfile
import unittest
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


class WeightsTest(unittest.TestCase):
    def test_repository_weights_file_loads(self):
        path = Path(__file__).resolve().parents[1] / ".config/test-split-weights.json"
        weights = test_split.load_weights(path)
        self.assertIn("github.com/sageox/ox/cmd/ox", weights)
        self.assertTrue(all(seconds >= test_split.WEIGHT_FLOOR_SECONDS for seconds in weights["github.com/sageox/ox/cmd/ox"].values()))

    def test_rejects_unknown_version(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "w.json"
            path.write_text(json.dumps({"version": 2, "packages": {}}), encoding="utf-8")
            with self.assertRaises(ValueError):
                test_split.load_weights(path)


if __name__ == "__main__":
    unittest.main()
