#!/usr/bin/env python3
"""Summarize gotestsum timing events without flooding the test output."""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path


FINAL_ACTIONS = {"pass", "skip", "fail"}
SLOWEST_LIMIT = 10


def parse_time(value: str) -> datetime | None:
    if not value:
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


def duration(seconds: float) -> str:
    if seconds >= 60:
        return f"{seconds / 60:.1f}m"
    return f"{seconds:.2f}s"


def load_metrics(lines: list[str]) -> tuple[dict[str, int], float, float, list[tuple[float, str, str]]]:
    counts = {action: 0 for action in FINAL_ACTIONS}
    first_event: datetime | None = None
    last_event: datetime | None = None
    tests: dict[tuple[str, str], tuple[str, float]] = {}

    for line in lines:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue

        event_time = parse_time(event.get("Time", ""))
        if event_time:
            first_event = min(first_event, event_time) if first_event else event_time
            last_event = max(last_event, event_time) if last_event else event_time

        action = event.get("Action")
        test = event.get("Test")
        if action not in FINAL_ACTIONS or not test:
            continue

        package = event.get("Package", "?")
        elapsed = float(event.get("Elapsed", 0))
        key = (package, test)
        # Retries emit duplicate final events. Keep the final result and the
        # largest elapsed time so timing regressions are never hidden.
        previous = tests.get(key)
        if previous is None or elapsed >= previous[1]:
            tests[key] = (action, elapsed)

    slowest: list[tuple[float, str, str]] = []
    elapsed_sum = 0.0
    for (package, test), (action, elapsed) in tests.items():
        counts[action] += 1
        if action != "skip":
            elapsed_sum += elapsed
        slowest.append((elapsed, package, test))

    wall_seconds = (last_event - first_event).total_seconds() if first_event and last_event else 0.0
    return counts, wall_seconds, elapsed_sum, sorted(slowest, reverse=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path", help="gotestsum timing-event file, or - for stdin")
    parser.add_argument("--markdown", action="store_true", help="emit GitHub-flavored Markdown")
    args = parser.parse_args()

    if args.path == "-":
        lines = sys.stdin.read().splitlines()
    else:
        path = Path(args.path)
        if not path.is_file():
            print(f"test metrics: timing artifact not found: {path}", file=sys.stderr)
            return 1
        lines = path.read_text().splitlines()

    counts, wall_seconds, elapsed_sum, slowest = load_metrics(lines)
    total = sum(counts.values())
    if total == 0:
        print("test metrics: no final test-case events found", file=sys.stderr)
        return 1

    if args.markdown:
        print("### Test timing metrics")
        print()
        print(f"- Event-span wall clock: **{duration(wall_seconds)}**")
        print(f"- Tests: **{counts['pass']} passed**, **{counts['skip']} skipped**, **{counts['fail']} failed** (total **{total}**)")
        print(f"- Summed test time: **{duration(elapsed_sum)}**")
        print()
        print("#### Slowest test cases")
        print()
        for elapsed, package, test in slowest[:SLOWEST_LIMIT]:
            print(f"- `{package}` · `{test}` — **{duration(elapsed)}**")
        return 0

    print(
        "TEST_METRICS"
        f" wall={duration(wall_seconds)}"
        f" passed={counts['pass']}"
        f" skipped={counts['skip']}"
        f" failed={counts['fail']}"
        f" total={total}"
        f" test_time_sum={duration(elapsed_sum)}"
    )
    for elapsed, package, test in slowest[:SLOWEST_LIMIT]:
        print(f"  SLOWEST {duration(elapsed):>7} {package} {test}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
