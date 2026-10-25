#!/usr/bin/env python3
"""Validate and expose the repository's executable test-tier contract."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path


DEFAULT_CONFIG = Path(__file__).resolve().parents[1] / ".config/test-tiers.json"
REQUIRED_TIERS = {
    "fast",
    "full",
    "slow",
    "acceptance",
    "digital_twin",
    "integration",
    "release",
}
SAFE_TAG = re.compile(r"^[A-Za-z0-9_.-]+$")
ENV_PACKAGE_PARALLELISM = "OX_TEST_P"
ENV_TEST_PARALLELISM = "OX_TEST_PARALLEL"
SAFE_TIMEOUT = re.compile(r"^(?:[0-9]+(?:ns|us|µs|ms|s|m|h))+$")


def load_config(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def validate(config: dict) -> list[str]:
    failures: list[str] = []
    if config.get("version") != 1:
        failures.append("version must be 1")
    tiers = config.get("tiers")
    if not isinstance(tiers, dict):
        return failures + ["tiers must be an object"]

    missing = sorted(REQUIRED_TIERS - set(tiers))
    if missing:
        failures.append(f"missing required tiers: {', '.join(missing)}")

    for name, tier in tiers.items():
        if not isinstance(tier, dict):
            failures.append(f"{name}: tier must be an object")
            continue
        description = tier.get("description")
        if not isinstance(description, str) or not description.strip():
            failures.append(f"{name}: description must be a non-empty string")
        for field in ("includes", "excludes"):
            value = tier.get(field)
            if not isinstance(value, list) or not value or not all(
                isinstance(item, str) and item.strip() for item in value
            ):
                failures.append(f"{name}: {field} must be a non-empty string array")
        go_test = tier.get("go_test")
        if go_test is not None:
            if not isinstance(go_test, dict):
                failures.append(f"{name}: go_test must be an object")
                continue
            if not isinstance(go_test.get("short"), bool):
                failures.append(f"{name}: go_test.short must be boolean")
            tags = go_test.get("tags")
            if not isinstance(tags, list) or not all(
                isinstance(tag, str) and SAFE_TAG.fullmatch(tag) for tag in tags
            ):
                failures.append(f"{name}: go_test.tags must contain only safe tag names")
            if not isinstance(go_test.get("race"), bool):
                failures.append(f"{name}: go_test.race must be boolean")
            if "count" in go_test:
                count = go_test["count"]
                if not isinstance(count, int) or isinstance(count, bool) or count < 1:
                    failures.append(f"{name}: go_test.count must be a positive integer")
            for field in ("package_parallelism", "test_parallelism"):
                value = go_test.get(field)
                if not isinstance(value, int) or isinstance(value, bool) or value < 1:
                    failures.append(f"{name}: go_test.{field} must be a positive integer")
            timeout = go_test.get("timeout")
            if not isinstance(timeout, str) or not SAFE_TIMEOUT.fullmatch(timeout):
                failures.append(f"{name}: go_test.timeout must be a safe Go duration")
        executor = tier.get("executor")
        if executor is not None and (
            not isinstance(executor, str) or not executor.strip()
        ):
            failures.append(f"{name}: executor must be a non-empty string")

    def go_settings(name: str) -> dict:
        tier = tiers.get(name)
        if not isinstance(tier, dict):
            return {}
        settings = tier.get("go_test")
        return settings if isinstance(settings, dict) else {}

    fast = go_settings("fast")
    if fast.get("short") is not True or "short" not in fast.get("tags", []):
        failures.append("fast: must set testing.Short and the short build tag together")
    full = go_settings("full")
    if full.get("short") is not False or "short" in full.get("tags", []):
        failures.append("full: must not set testing.Short or the short build tag")
    slow = go_settings("slow")
    if "slow" not in slow.get("tags", []):
        failures.append("slow: must set the slow build tag")
    if slow.get("short") is not False:
        failures.append("slow: must not set testing.Short")
    for name in ("acceptance", "digital_twin", "integration"):
        tier = tiers.get(name)
        if isinstance(tier, dict) and not isinstance(tier.get("executor"), str):
            failures.append(f"{name}: executor must be configured")

    for name, tier in tiers.items():
        if not isinstance(tier, dict):
            continue
        requires = tier.get("requires", [])
        if not isinstance(requires, list) or not all(
            isinstance(dependency, str) for dependency in requires
        ):
            failures.append(f"{name}: requires must be a string array")
            continue
        for dependency in requires:
            if dependency not in tiers:
                failures.append(f"{name}: unknown required tier {dependency!r}")
    release = tiers.get("release")
    release_requires_raw = release.get("requires", []) if isinstance(release, dict) else []
    release_requires = set(release_requires_raw) if isinstance(release_requires_raw, list) else set()
    expected_release = {"full", "slow", "acceptance", "digital_twin"}
    if release_requires != expected_release:
        failures.append(
            "release: requires must be exactly full, slow, acceptance, and digital_twin"
        )
    for name in ("fast", "full", "slow"):
        if go_settings(name).get("race") is not True:
            failures.append(f"{name}: race detection must remain enabled")
    return failures


def _concurrency(env_var: str, declared: int) -> int:
    """Return the declared parallelism, or an operator override from the environment.

    The declared values are sized for a dedicated CI runner with the machine to
    itself. Locally the same numbers run N-at-a-time, once per agent session and
    git worktree: on an 18-core workstation two concurrent full-tier runs drove
    the load average to 37 and the machine to ~50% system time. These overrides
    let one run be dialed down without editing the tier contract that CI
    enforces, so a loaded machine is a local decision rather than a repo-wide one.
    """
    raw = os.environ.get(env_var)
    if raw is None:
        return declared
    try:
        value = int(raw)
    except ValueError:
        raise ValueError(f"{env_var} must be a positive integer, got {raw!r}") from None
    if value < 1:
        raise ValueError(f"{env_var} must be >= 1, got {value}")
    return value


def go_test_flags(config: dict, tier_name: str) -> list[str]:
    tier = config["tiers"].get(tier_name)
    if tier is None:
        raise ValueError(f"unknown test tier {tier_name!r}")
    settings = tier.get("go_test")
    if settings is None:
        raise ValueError(f"test tier {tier_name!r} has no direct Go test command")

    flags: list[str] = []
    if settings["short"]:
        flags.append("-short")
    tags = settings["tags"]
    if tags:
        flags.append("-tags=" + ",".join(tags))
    if settings.get("race", False):
        flags.append("-race")
    if "count" in settings:
        flags.append("-count=" + str(settings["count"]))
    flags.extend(["-p", str(_concurrency(ENV_PACKAGE_PARALLELISM, settings["package_parallelism"]))])
    flags.extend(["-parallel", str(_concurrency(ENV_TEST_PARALLELISM, settings["test_parallelism"]))])
    flags.append("-timeout=" + settings["timeout"])
    return flags


def describe(config: dict, tier_name: str) -> str:
    tier = config["tiers"].get(tier_name)
    if tier is None:
        raise ValueError(f"unknown test tier {tier_name!r}")
    lines = [f"{tier_name}: {tier['description']}", "Includes:"]
    lines.extend(f"  - {item}" for item in tier["includes"])
    lines.append("Excludes:")
    lines.extend(f"  - {item}" for item in tier["excludes"])
    if "requires" in tier:
        lines.append("Requires: " + ", ".join(tier["requires"]))
    if "executor" in tier:
        lines.append("Executor: " + tier["executor"])
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("validate")
    flags_parser = subparsers.add_parser("flags")
    flags_parser.add_argument("tier")
    describe_parser = subparsers.add_parser("describe")
    describe_parser.add_argument("tier")
    args = parser.parse_args()

    try:
        config = load_config(args.config)
        failures = validate(config)
        if failures:
            for failure in failures:
                print(f"test tier contract: {failure}", file=sys.stderr)
            return 1
        if args.command == "validate":
            print(f"OK: {len(config['tiers'])} test tiers")
        elif args.command == "flags":
            print(" ".join(go_test_flags(config, args.tier)))
        elif args.command == "describe":
            print(describe(config, args.tier))
    except (OSError, KeyError, TypeError, ValueError, json.JSONDecodeError) as error:
        print(f"test tier contract error: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
