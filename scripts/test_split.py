#!/usr/bin/env python3
"""Run the full test tier with long serial packages fanned out across processes.

cmd/ox runs about 3,600 top-level tests, most of them one at a time (they use
t.Setenv or os.Chdir, which forbid t.Parallel). On a 4-core CI runner it ran
for 7.5 minutes while every other package had finished, leaving three cores
idle. This runner gives each split package N `go test` processes, each with an
explicit -run list, and runs them alongside one process for every other
package. Every test still runs exactly once, with the same flags:

  * the shard lists come from `go test -list` on the package itself, so a new
    test is always assigned to a shard, and plan() refuses a partition that
    drops, duplicates, or reorders a name;
  * coverage profiles merge by summing block counts, which is what a single
    `go test -coverprofile` run would have recorded.

Each shard is a contiguous slice of the package's own run order, cut where the
per-test seconds in the weights file (tests of 0.5s or more on CI) add up to an
equal share; every other test counts as DEFAULT_SECONDS. MAX_RUN_BYTES keeps
each -run argument well below Linux's 128 KiB per-argument limit.
"""

from __future__ import annotations

import argparse
import json
import shlex
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
from collections import Counter
from pathlib import Path

DEFAULT_SECONDS = 0.05
WEIGHT_FLOOR_SECONDS = 0.5
# Linux rejects any single argv string over 128 KiB (MAX_ARG_STRLEN) with
# "argument list too long". Stay well clear of it.
MAX_RUN_BYTES = 100_000
RUNNABLE_PREFIXES = ("Test", "Example", "Fuzz")
JUNIT_SUM_ATTRIBUTES = ("tests", "failures", "errors", "skipped", "time")


def parse_list_output(text: str) -> list[str]:
    """Return the runnable top-level names printed by `go test -list`."""
    names = []
    for line in text.splitlines():
        line = line.strip()
        if line.startswith(RUNNABLE_PREFIXES) and line.isidentifier():
            names.append(line)
    return names


def plan(names: list[str], weights: dict[str, float], shards: int) -> list[list[str]]:
    """Cut the package's run order into contiguous shards of about equal time.

    Contiguous, not interleaved: each shard is a consecutive slice of the order
    `go test` already runs the package in, so every test keeps the neighbours
    it has in a single-process run. cmd/ox has tests that leave package state
    behind (a JSON-mode config, say) and later tests that only pass because a
    test in between reset it; interleaving names across shards broke exactly
    those pairs. A slice boundary can only separate a test from state an
    earlier test left behind, which leaves the later test with cleaner state.
    """
    if shards < 1:
        raise ValueError("shards must be at least 1")
    if len(set(names)) != len(names):
        duplicates = sorted(name for name, count in Counter(names).items() if count > 1)
        raise ValueError(f"duplicate test names in listing: {duplicates[:5]}")

    cost = [max(weights.get(name, 0.0), DEFAULT_SECONDS) for name in names]
    lists = _cut(names, cost, _min_max_capacity(cost, shards))

    if [name for shard in lists for name in shard] != names:
        raise AssertionError("shard plan dropped, duplicated, or reordered a test")
    for position, shard in enumerate(lists):
        if len(run_pattern(shard).encode()) > MAX_RUN_BYTES:
            raise ValueError(f"shard {position} -run pattern exceeds {MAX_RUN_BYTES} bytes; raise the shard count")
    return lists


def _pieces(cost: list[float], capacity: float) -> int:
    pieces, running = 1, 0.0
    for seconds in cost:
        if running and running + seconds > capacity:
            pieces, running = pieces + 1, 0.0
        running += seconds
    return pieces


def _min_max_capacity(cost: list[float], shards: int) -> float:
    """Smallest per-shard total that fits the order into `shards` slices."""
    low, high = max(cost, default=0.0), sum(cost)
    for _ in range(60):
        middle = (low + high) / 2
        if _pieces(cost, middle) <= shards:
            high = middle
        else:
            low = middle
    return high


def _cut(names: list[str], cost: list[float], capacity: float) -> list[list[str]]:
    lists: list[list[str]] = [[]]
    running = 0.0
    for name, seconds in zip(names, cost):
        if lists[-1] and running + seconds > capacity:
            lists.append([])
            running = 0.0
        lists[-1].append(name)
        running += seconds
    return lists


def run_pattern(names: list[str]) -> str:
    return "^(" + "|".join(names) + ")$"


def merge_cover(profiles: list[Path], destination: Path) -> None:
    """Sum block counts across profiles; equal to one run over the union."""
    mode = None
    counts: dict[tuple[str, str], int] = {}
    for profile in profiles:
        for line in profile.read_text(encoding="utf-8").splitlines():
            if not line:
                continue
            if line.startswith("mode:"):
                if mode is not None and line != mode:
                    raise ValueError(f"{profile}: {line!r} does not match {mode!r}")
                mode = line
                continue
            block, statements, count = line.rsplit(" ", 2)
            key = (block, statements)
            counts[key] = counts.get(key, 0) + int(count)
    if mode is None:
        raise ValueError("no coverage profiles to merge")
    body = "".join(f"{block} {statements} {count}\n" for (block, statements), count in sorted(counts.items()))
    destination.write_text(f"{mode}\n{body}", encoding="utf-8")


def merge_junit(reports: list[Path], destination: Path) -> None:
    root = ET.Element("testsuites")
    totals = {attribute: 0.0 for attribute in JUNIT_SUM_ATTRIBUTES}
    for report in reports:
        parsed = ET.parse(report).getroot()
        suites = [parsed] if parsed.tag == "testsuite" else list(parsed.iter("testsuite"))
        for suite in suites:
            root.append(suite)
            for attribute in JUNIT_SUM_ATTRIBUTES:
                totals[attribute] += float(suite.get(attribute, 0) or 0)
    for attribute, value in totals.items():
        root.set(attribute, f"{value:.3f}" if attribute == "time" else str(int(value)))
    ET.ElementTree(root).write(destination, encoding="utf-8", xml_declaration=True)


def load_weights(path: Path | None) -> dict[str, dict[str, float]]:
    if path is None:
        return {}
    payload = json.loads(path.read_text(encoding="utf-8"))
    if payload.get("version") != 1:
        raise ValueError(f"{path}: unsupported weights version")
    return {package: {name: float(seconds) for name, seconds in tests.items()} for package, tests in payload["packages"].items()}


def go_list(go: list[str], patterns: list[str]) -> list[str]:
    result = subprocess.run([*go, "list", *patterns], check=True, text=True, capture_output=True)
    return result.stdout.split()


def list_tests(go: list[str], flags: list[str], package: str) -> list[str]:
    result = subprocess.run([*go, "test", *flags, "-list", ".", package], text=True, capture_output=True)
    if result.returncode != 0:
        sys.stderr.write(result.stdout + result.stderr)
        raise SystemExit(f"go test -list failed for {package}")
    names = parse_list_output(result.stdout)
    if not names:
        raise SystemExit(f"go test -list found no tests in {package}")
    return names


class Job:
    def __init__(self, label: str, work_dir: Path, slug: str):
        self.label = label
        self.cover = work_dir / f"{slug}.cov"
        self.junit = work_dir / f"{slug}.junit.xml"
        self.timings = work_dir / f"{slug}.timings.json"
        self.log = work_dir / f"{slug}.log"
        self.command: list[str] = []
        self.process: subprocess.Popen | None = None
        self.started = 0.0
        self.handle = None

    def start(self) -> None:
        self.handle = self.log.open("w", encoding="utf-8")
        self.started = time.monotonic()
        self.process = subprocess.Popen(self.command, stdout=self.handle, stderr=subprocess.STDOUT)
        print(f"test-split: started {self.label}", flush=True)


def gotestsum_command(gotestsum: list[str], job_paths: Job, go_flags: list[str], extra: list[str], packages: list[str]) -> list[str]:
    return [
        *gotestsum,
        "--junitfile", str(job_paths.junit),
        "--jsonfile-timing-events", str(job_paths.timings),
        "--",
        *go_flags,
        f"-coverprofile={job_paths.cover}",
        *extra,
        *packages,
    ]


def run(args: argparse.Namespace) -> int:
    go = shlex.split(args.go)
    gotestsum = shlex.split(args.gotestsum)
    go_flags = list(args.go_flags)
    work_dir = Path(args.work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    for stale in work_dir.iterdir():
        if stale.is_file():
            stale.unlink()

    splits: list[tuple[str, int]] = []
    for spec in args.split:
        package, _, count = spec.partition("=")
        splits.append((package, int(count)))
    split_paths = go_list(go, [package for package, _ in splits])
    everything = go_list(go, args.packages)
    rest = [package for package in everything if package not in split_paths]
    weights = load_weights(Path(args.weights) if args.weights else None)

    # List the split packages before starting anything: the listing compiles the
    # race-instrumented dependency graph once, and every process below reuses
    # it from the build cache instead of compiling it concurrently.
    planned = []
    for (package, count), import_path in zip(splits, split_paths):
        names = list_tests(go, go_flags, package)
        planned.append((package, import_path, plan(names, weights.get(import_path, {}), count)))

    jobs: list[Job] = []
    if rest:
        job = Job(f"{len(rest)} packages", work_dir, "rest")
        job.command = gotestsum_command(gotestsum, job, go_flags, [], rest)
        jobs.append(job)
    for package, import_path, shards in planned:
        for index, names_in_shard in enumerate(shards):
            slug = f"{import_path.rsplit('/', 1)[-1]}-{index}"
            job = Job(f"{import_path} shard {index + 1}/{len(shards)} ({len(names_in_shard)} tests)", work_dir, slug)
            job.command = gotestsum_command(gotestsum, job, go_flags, ["-run", run_pattern(names_in_shard)], [package])
            jobs.append(job)
    for job in jobs:
        job.start()

    failed = []
    pending = list(jobs)
    while pending:
        for job in list(pending):
            assert job.process is not None
            if job.process.poll() is None:
                continue
            pending.remove(job)
            job.handle.close()
            elapsed = time.monotonic() - job.started
            status = "ok" if job.process.returncode == 0 else f"FAILED (exit {job.process.returncode})"
            print(f"\n── {job.label}: {status} in {elapsed:.0f}s", flush=True)
            sys.stdout.write(job.log.read_text(encoding="utf-8", errors="replace"))
            sys.stdout.flush()
            if job.process.returncode != 0:
                failed.append(job.label)
        time.sleep(0.5)

    covers = [job.cover for job in jobs if job.cover.is_file() and job.cover.stat().st_size]
    if covers:
        merge_cover(covers, Path(args.coverprofile))
    if args.junit:
        merge_junit([job.junit for job in jobs if job.junit.is_file() and job.junit.stat().st_size], Path(args.junit))
    if args.timings:
        with Path(args.timings).open("w", encoding="utf-8") as out:
            for job in jobs:
                if job.timings.is_file():
                    out.write(job.timings.read_text(encoding="utf-8"))

    if failed:
        print(f"\ntest-split: {len(failed)} of {len(jobs)} processes failed: {', '.join(failed)}", file=sys.stderr)
        return 1
    if len(covers) != len(jobs):
        print("test-split: a process exited 0 without writing a coverage profile", file=sys.stderr)
        return 1
    return 0


def update_weights(args: argparse.Namespace) -> int:
    """Rebuild the weights file from a CI timing artifact (gotestsum events)."""
    seconds: dict[str, dict[str, float]] = {package: {} for package in args.package}
    for line in Path(args.timings).read_text(encoding="utf-8").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        package, test = event.get("Package"), event.get("Test")
        if package in seconds and test and "/" not in test and event.get("Action") in ("pass", "fail"):
            elapsed = float(event.get("Elapsed", 0))
            if elapsed >= WEIGHT_FLOOR_SECONDS:
                seconds[package][test] = max(seconds[package].get(test, 0.0), round(elapsed, 1))
    payload = {
        "version": 1,
        "source": args.source,
        "packages": {package: dict(sorted(tests.items(), key=lambda item: (-item[1], item[0]))) for package, tests in sorted(seconds.items())},
    }
    Path(args.output).write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)

    run_parser = sub.add_parser("run", help="run the full tier with split packages")
    run_parser.add_argument("--go", default="go")
    run_parser.add_argument("--gotestsum", required=True, help="gotestsum command plus format flags")
    run_parser.add_argument("--split", action="append", default=[], metavar="PKG=N")
    run_parser.add_argument("--weights")
    run_parser.add_argument("--work-dir", required=True)
    run_parser.add_argument("--coverprofile", required=True)
    run_parser.add_argument("--junit")
    run_parser.add_argument("--timings")
    run_parser.add_argument("--packages", nargs="+", default=["./..."])
    run_parser.add_argument("go_flags", nargs=argparse.REMAINDER, help="-- then go test flags")

    weights_parser = sub.add_parser("update-weights", help="rebuild the weights file from a timing artifact")
    weights_parser.add_argument("--timings", required=True)
    weights_parser.add_argument("--package", action="append", required=True, metavar="IMPORT_PATH")
    weights_parser.add_argument("--source", required=True, help="where the timings came from, e.g. a CI run id")
    weights_parser.add_argument("--output", required=True)

    args = parser.parse_args()
    if args.command == "run":
        if args.go_flags and args.go_flags[0] == "--":
            args.go_flags = args.go_flags[1:]
        return run(args)
    return update_weights(args)


if __name__ == "__main__":
    raise SystemExit(main())
