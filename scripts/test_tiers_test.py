import copy
import json
import os
import unittest
from pathlib import Path

import test_tiers


class TestTierContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.config = test_tiers.load_config(test_tiers.DEFAULT_CONFIG)

    def test_repository_contract_is_valid(self):
        self.assertEqual([], test_tiers.validate(self.config))

    def test_fast_flags_bind_short_flag_and_build_tag(self):
        self.assertEqual(
            [
                "-short",
                "-tags=short",
                "-race",
                "-p",
                "8",
                "-parallel",
                "32",
                "-timeout=10m",
            ],
            test_tiers.go_test_flags(self.config, "fast"),
        )

    def test_full_flags_do_not_accidentally_exclude_not_short_files(self):
        flags = test_tiers.go_test_flags(self.config, "full")
        self.assertNotIn("-short", flags)
        self.assertFalse(any(flag.startswith("-tags=short") for flag in flags))
        self.assertIn("-timeout=15m", flags)
        self.assertIn("-count=1", flags)

    def test_rejects_split_short_semantics(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["fast"]["go_test"]["tags"] = []
        self.assertIn(
            "fast: must set testing.Short and the short build tag together",
            test_tiers.validate(config),
        )

    def test_rejects_release_that_omits_an_enforced_tier(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["release"]["requires"] = ["full", "slow"]
        self.assertIn(
            "release: requires must be exactly full, slow, acceptance, and digital_twin",
            test_tiers.validate(config),
        )

    def test_slow_tier_disables_go_test_cache(self):
        flags = test_tiers.go_test_flags(self.config, "slow")
        self.assertIn("-count=1", flags)

    def test_rejects_slow_short_mode(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["slow"]["go_test"]["short"] = True
        self.assertIn(
            "slow: must not set testing.Short", test_tiers.validate(config)
        )

    def test_rejects_unsafe_or_malformed_go_test_settings(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["fast"]["go_test"]["tags"] = ["short; echo unsafe"]
        config["tiers"]["full"]["go_test"]["race"] = False
        config["tiers"]["slow"]["includes"] = "not an array"
        config["tiers"]["slow"]["go_test"]["timeout"] = "10m; echo unsafe"

        failures = test_tiers.validate(config)

        self.assertIn(
            "fast: go_test.tags must contain only safe tag names", failures
        )
        self.assertIn("full: race detection must remain enabled", failures)
        self.assertIn(
            "slow: includes must be a non-empty string array", failures
        )
        self.assertIn("slow: go_test.timeout must be a safe Go duration", failures)

    def test_rejects_boolean_parallelism(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["fast"]["go_test"]["package_parallelism"] = True
        config["tiers"]["full"]["go_test"]["test_parallelism"] = False

        failures = test_tiers.validate(config)

        self.assertIn(
            "fast: go_test.package_parallelism must be a positive integer",
            failures,
        )
        self.assertIn(
            "full: go_test.test_parallelism must be a positive integer",
            failures,
        )

    def test_rejects_missing_integration_executor(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["integration"]["executor"] = ""
        self.assertIn(
            "integration: executor must be a non-empty string",
            test_tiers.validate(config),
        )

    def test_rejects_non_object_go_test_without_traceback(self):
        config = copy.deepcopy(self.config)
        config["tiers"]["fast"]["go_test"] = "-short"

        self.assertIn(
            "fast: go_test must be an object", test_tiers.validate(config)
        )

    def test_concurrency_overrides_dial_a_single_run_down(self):
        # a loaded workstation is a local decision; CI still gets the contract
        with _env(OX_TEST_P="2", OX_TEST_PARALLEL="8"):
            flags = test_tiers.go_test_flags(self.config, "full")
        self.assertEqual(["-p", "2"], flags[flags.index("-p") : flags.index("-p") + 2])
        self.assertEqual(
            ["-parallel", "8"],
            flags[flags.index("-parallel") : flags.index("-parallel") + 2],
        )

    def test_concurrency_overrides_default_to_the_contract(self):
        with _env(OX_TEST_P=None, OX_TEST_PARALLEL=None):
            self.assertEqual(
                test_tiers.go_test_flags(self.config, "full"),
                ["-race", "-count=1", "-p", "8", "-parallel", "32", "-timeout=15m"],
            )

    def test_rejects_nonsense_concurrency_override(self):
        for value in ("0", "-4", "many"):
            with self.subTest(value=value), _env(OX_TEST_P=value):
                with self.assertRaises(ValueError):
                    test_tiers.go_test_flags(self.config, "full")

    def test_config_remains_machine_readable_json(self):
        encoded = json.dumps(self.config)
        self.assertEqual(self.config, json.loads(encoded))
        self.assertTrue(Path(test_tiers.DEFAULT_CONFIG).is_file())


if __name__ == "__main__":
    unittest.main()


class _env:
    """Temporarily set (or unset, with None) environment variables."""

    def __init__(self, **overrides):
        self.overrides = overrides
        self.saved = {}

    def __enter__(self):
        for key, value in self.overrides.items():
            self.saved[key] = os.environ.get(key)
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        return self

    def __exit__(self, *exc):
        for key, value in self.saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        return False
