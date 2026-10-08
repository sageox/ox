#!/usr/bin/env python3
"""Run the full test tier with long serial packages fanned out across processes.

cmd/ox runs about 3,600 top-level tests, most of them one at a time (they use
t.Setenv or os.Chdir, which forbid t.Parallel), so one 7.5-minute chain set the
floor for the whole PR job. This runner cuts the tier into slots: each split
package becomes N `go test` processes with explicit -run lists, groups of
packages get one process each, and everything else is the `rest` slot.
Locally all slots run at once; CI runs each slot on its own runner. Every test
still runs exactly once, with the same flags:

  * the shard lists come from `go test -list` on the package itself, so a new
    test is always assigned to a shard, and plan() refuses a partition that
    drops, duplicates, or reorders a name;
  * coverage profiles merge by summing block counts, which is what a single
    `go test -coverprofile` run would have recorded.

Each shard is a contiguous slice of the package's own run order, cut where the
per-test seconds in the weights file (tests of 0.5s or more on CI) add up to an
equal share; every other test counts as the package's recorded average for
tests under that floor (DEFAULT_SECONDS if none is recorded). MAX_RUN_BYTES keeps
each -run argument well below Linux's 128 KiB per-argument limit.
"""

from __future__ import annotations

import argparse
import json
import shlex
import shutil
import subprocess
import sys
import tempfile
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
DIAGNOSTIC_OUTPUT_BYTES = 64 * 1024


def parse_list_output(text: str) -> list[str]:
    """Return the runnable top-level names printed by `go test -list`."""
    names = []
    for line in text.splitlines():
        line = line.strip()
        if line.startswith(RUNNABLE_PREFIXES) and line.isidentifier():
            names.append(line)
    return names


def plan(names: list[str], weights: dict[str, float], shards: int, default_seconds: float = DEFAULT_SECONDS) -> list[list[str]]:
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

    cost = [weights.get(name, default_seconds) for name in names]
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


class PackageWeights:
    def __init__(self, tests: dict[str, float] | None = None, default_seconds: float = DEFAULT_SECONDS):
        self.tests = tests or {}
        self.default_seconds = default_seconds


def load_weights(path: Path | None) -> dict[str, PackageWeights]:
    if path is None:
        return {}
    payload = json.loads(path.read_text(encoding="utf-8"))
    if payload.get("version") != 1:
        raise ValueError(f"{path}: unsupported weights version")
    return {
        package: PackageWeights({name: float(seconds) for name, seconds in entry["tests"].items()}, float(entry["default_seconds"]))
        for package, entry in payload["packages"].items()
    }


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
        "--jsonfile", str(job_paths.timings),
        "--",
        *go_flags,
        f"-coverprofile={job_paths.cover}",
        *extra,
        *packages,
    ]


def _test_event_records(path: Path):
    """Read one JSONL event at a time, tolerating a producer's partial record."""
    with path.open(encoding="utf-8", newline="") as stream:
        for line in stream:
            try:
                yield line, json.loads(line)
            except json.JSONDecodeError:
                continue


def finalize_test_events(path: Path, failed: bool) -> str:
    """Preserve failed artifacts and return at most 64 KiB of diagnostic output.

    gotestsum can attribute a race or background error to a test that passes,
    then report only a package failure. Scan twice to find these packages and
    show bounded context; the full original stream remains in the artifact.
    Passing shards are compacted with an atomic replacement after streaming
    terminal events to a temporary file, so failed compaction preserves input.
    """
    if not path.is_file():
        return ""
    if not failed:
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", newline="", dir=path.parent,
                                             prefix=path.name + ".", delete=False) as stream:
                temporary = Path(stream.name)
                for line, event in _test_event_records(path):
                    if event.get("Action") in {"pass", "fail", "skip"}:
                        stream.write(line)
            temporary.replace(path)
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
        return ""

    package_failures, test_failures = set(), set()
    for _, event in _test_event_records(path):
        if event.get("Action") == "fail":
            failures = test_failures if event.get("Test") else package_failures
            failures.add(event.get("Package"))
    package_only = package_failures - test_failures
    output = bytearray()
    for _, event in _test_event_records(path):
        if event.get("Package") not in package_only or event.get("Action") != "output":
            continue
        text = event.get("Output", "")
        if text.lstrip().startswith(("=== RUN", "=== PAUSE", "=== CONT", "--- PASS:", "--- SKIP:")):
            continue
        remaining = DIAGNOSTIC_OUTPUT_BYTES - len(output)
        # Slice before encoding so even a huge individual event cannot grow
        # the retained excerpt beyond the byte budget.
        snippet = text[:remaining + 1].encode("utf-8")
        output.extend(snippet[:remaining])
        if len(snippet) > remaining:
            return output.decode("utf-8", errors="ignore") + f"\n[Diagnostic output truncated at {DIAGNOSTIC_OUTPUT_BYTES} bytes; full events: {path}]\n"
    return output.decode("utf-8")


class Layout:
    """The slots one full-tier run is cut into.

    `rest` is every package not named below. A group (NAME=PKG,PKG) runs its
    packages in one process. A split (PKG=N) runs PKG as N contiguous slices of
    its test order. Locally every slot runs at once; CI gives each slot its own
    runner, because on a 4-vCPU runner the tests are CPU-bound and processes
    sharing it only slow each other down.
    """

    def __init__(self, splits: list[str], groups: list[str]):
        self.splits: list[tuple[str, int]] = []
        for spec in splits:
            package, _, count = spec.partition("=")
            if not package or int(count) < 1:
                raise ValueError(f"bad --split {spec!r}; want PKG=N")
            self.splits.append((package, int(count)))
        self.groups: list[tuple[str, list[str]]] = []
        for spec in groups:
            name, _, packages = spec.partition("=")
            if not name or not packages:
                raise ValueError(f"bad --group {spec!r}; want NAME=PKG,PKG")
            self.groups.append((name, packages.split(",")))

    @staticmethod
    def split_slot(package: str, index: int) -> str:
        return f"{package.rstrip('/').rsplit('/', 1)[-1]}-{index + 1}"

    def slots(self) -> list[str]:
        names = ["rest", *(name for name, _ in self.groups)]
        for package, count in self.splits:
            names.extend(self.split_slot(package, index) for index in range(count))
        if len(set(names)) != len(names):
            raise ValueError(f"slot names collide: {names}")
        return names


def build_jobs(layout: Layout, only: str | None, go: list[str], gotestsum: list[str], go_flags: list[str], packages: list[str], weights: dict[str, PackageWeights], work_dir: Path) -> list[Job]:
    if only is not None and only not in layout.slots():
        raise SystemExit(f"unknown slot {only!r}; slots are {layout.slots()}")
    wanted = lambda slot: only is None or only == slot  # noqa: E731
    jobs: list[Job] = []

    named = [package for package, _ in layout.splits] + [package for _, group in layout.groups for package in group]
    if wanted("rest"):
        excluded = set(go_list(go, named)) if named else set()
        rest = [package for package in go_list(go, packages) if package not in excluded]
        job = Job(f"rest ({len(rest)} packages)", work_dir, "rest")
        job.command = gotestsum_command(gotestsum, job, go_flags, [], rest)
        jobs.append(job)
    for name, group in layout.groups:
        if wanted(name):
            job = Job(f"{name} ({', '.join(group)})", work_dir, name)
            job.command = gotestsum_command(gotestsum, job, go_flags, [], group)
            jobs.append(job)

    # List a split package before starting anything: the listing compiles the
    # race-instrumented dependency graph once, and every process reuses it from
    # the build cache instead of compiling it concurrently.
    for package, count in layout.splits:
        slots = [index for index in range(count) if wanted(layout.split_slot(package, index))]
        if not slots:
            continue
        import_path = go_list(go, [package])[0]
        known = weights.get(import_path, PackageWeights())
        shards = plan(list_tests(go, go_flags, package), known.tests, count, known.default_seconds)
        for index in slots:
            slot = layout.split_slot(package, index)
            if index >= len(shards):
                print(f"test-split: {slot} has no tests in this plan", flush=True)
                continue
            job = Job(f"{slot} ({package} slice {index + 1}/{len(shards)}, {len(shards[index])} tests)", work_dir, slot)
            job.command = gotestsum_command(gotestsum, job, go_flags, ["-run", run_pattern(shards[index])], [package])
            jobs.append(job)
    return jobs


def run(args: argparse.Namespace) -> int:
    go = shlex.split(args.go)
    gotestsum = shlex.split(args.gotestsum)
    work_dir = Path(args.work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    for stale in work_dir.iterdir():
        if stale.is_file():
            stale.unlink()

    layout = Layout(args.split, args.group)
    weights = load_weights(Path(args.weights) if args.weights else None)
    jobs = build_jobs(layout, args.only, go, gotestsum, list(args.go_flags), args.packages, weights, work_dir)
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
            diagnostic = finalize_test_events(job.timings, job.process.returncode != 0)
            if diagnostic:
                print(f"Package-level failure context (full events retained in {job.timings}):", flush=True)
                sys.stdout.write(diagnostic)
                sys.stdout.flush()
            if job.process.returncode != 0:
                failed.append(job.label)
        time.sleep(0.5)

    destination = Path(args.coverprofile)
    destination.parent.mkdir(parents=True, exist_ok=True)
    covers = [job.cover for job in jobs if job.cover.is_file() and job.cover.stat().st_size]
    if covers:
        merge_cover(covers, destination)
    elif not jobs:
        destination.write_text("mode: atomic\n", encoding="utf-8")
    if args.junit:
        merge_junit([job.junit for job in jobs if job.junit.is_file() and job.junit.stat().st_size], Path(args.junit))
    if args.timings:
        with Path(args.timings).open("w", encoding="utf-8", newline="") as out:
            for job in jobs:
                if job.timings.is_file():
                    with job.timings.open(encoding="utf-8", newline="") as source:
                        shutil.copyfileobj(source, out)

    if failed:
        print(f"\ntest-split: {len(failed)} of {len(jobs)} processes failed: {', '.join(failed)}", file=sys.stderr)
        return 1
    if len(covers) != len(jobs):
        print("test-split: a process exited 0 without writing a coverage profile", file=sys.stderr)
        return 1
    return 0


def merge_slots(args: argparse.Namespace) -> int:
    """Merge one coverage part per slot, refusing to merge if a slot is missing."""
    layout = Layout(args.split, args.group)
    directory = Path(args.parts)
    expected = {slot: directory / f"coverage-{slot}.out" for slot in layout.slots()}
    missing = [slot for slot, path in expected.items() if not path.is_file()]
    extra = sorted(path.name for path in directory.glob("coverage-*.out") if path not in expected.values())
    if missing or extra:
        print(f"test-split: coverage parts missing {missing}, unexpected {extra}", file=sys.stderr)
        return 1
    merge_cover(list(expected.values()), Path(args.out))
    return 0


def update_weights(args: argparse.Namespace) -> int:
    """Rebuild the weights file from a CI timing artifact (gotestsum events)."""
    seconds: dict[str, dict[str, float]] = {package: {} for package in args.package}
    light: dict[str, list[float]] = {package: [] for package in args.package}
    lines = [line for path in args.timings for line in Path(path).read_text(encoding="utf-8").splitlines()]
    for line in lines:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        package, test = event.get("Package"), event.get("Test")
        if package in seconds and test and "/" not in test and event.get("Action") in ("pass", "fail"):
            elapsed = float(event.get("Elapsed", 0))
            if elapsed >= WEIGHT_FLOOR_SECONDS:
                seconds[package][test] = max(seconds[package].get(test, 0.0), round(elapsed, 1))
            else:
                light[package].append(elapsed)
    payload = {
        "version": 1,
        "source": args.source,
        "packages": {
            package: {
                # Most tests are too quick to list one by one, but there are
                # thousands of them: costing them at their real average keeps
                # the slices even.
                "default_seconds": round(sum(light[package]) / len(light[package]), 4) if light[package] else DEFAULT_SECONDS,
                "tests": dict(sorted(tests.items(), key=lambda item: (-item[1], item[0]))),
            }
            for package, tests in sorted(seconds.items())
        },
    }
    Path(args.output).write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)

    def layout_arguments(command: argparse.ArgumentParser) -> None:
        command.add_argument("--split", action="append", default=[], metavar="PKG=N")
        command.add_argument("--group", action="append", default=[], metavar="NAME=PKG,PKG")

    run_parser = sub.add_parser("run", help="run the full tier, or one slot of it")
    layout_arguments(run_parser)
    run_parser.add_argument("--only", metavar="SLOT", help="run one slot (see the slots command)")
    run_parser.add_argument("--go", default="go")
    run_parser.add_argument("--gotestsum", required=True, help="gotestsum command plus format flags")
    run_parser.add_argument("--weights")
    run_parser.add_argument("--work-dir", required=True)
    run_parser.add_argument("--coverprofile", required=True)
    run_parser.add_argument("--junit")
    run_parser.add_argument("--timings")
    run_parser.add_argument("--packages", nargs="+", default=["./..."])
    run_parser.add_argument("go_flags", nargs=argparse.REMAINDER, help="-- then go test flags")

    slots_parser = sub.add_parser("slots", help="print the slot names as a JSON array (the CI matrix)")
    layout_arguments(slots_parser)

    merge_parser = sub.add_parser("merge-slots", help="merge coverage-<slot>.out parts, one per slot")
    layout_arguments(merge_parser)
    merge_parser.add_argument("--parts", required=True, help="directory holding coverage-<slot>.out")
    merge_parser.add_argument("--out", required=True)

    weights_parser = sub.add_parser("update-weights", help="rebuild the weights file from a timing artifact")
    weights_parser.add_argument("--timings", action="append", required=True)
    weights_parser.add_argument("--package", action="append", required=True, metavar="IMPORT_PATH")
    weights_parser.add_argument("--source", required=True, help="where the timings came from, e.g. a CI run id")
    weights_parser.add_argument("--output", required=True)

    args = parser.parse_args()
    if args.command == "run":
        if args.go_flags and args.go_flags[0] == "--":
            args.go_flags = args.go_flags[1:]
        return run(args)
    if args.command == "slots":
        print(json.dumps(Layout(args.split, args.group).slots()))
        return 0
    if args.command == "merge-slots":
        return merge_slots(args)
    return update_weights(args)


if __name__ == "__main__":
    raise SystemExit(main())
