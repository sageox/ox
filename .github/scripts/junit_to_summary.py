#!/usr/bin/env python3
# Emits a compact Markdown digest of failed tests from a gotestsum JUnit
# XML report. Designed for $GITHUB_STEP_SUMMARY: humans see the failing
# tests at the top of the run page, AI coworkers reviewing via
# `gh run view --json` get a deterministic structured failure list.
#
# Usage: junit_to_summary.py test-results.xml [max_lines_per_failure]
#
# Exits 0 even if the XML is missing or unparseable so it never masks
# the underlying test failure.

import sys
import xml.etree.ElementTree as ET
from pathlib import Path

MAX_LINES_DEFAULT = 12
MAX_FAILURES_SHOWN = 50


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: junit_to_summary.py test-results.xml [max_lines]", file=sys.stderr)
        return 0

    path = Path(sys.argv[1])
    max_lines = int(sys.argv[2]) if len(sys.argv) > 2 else MAX_LINES_DEFAULT

    if not path.exists():
        print("### Test results\n\n_No JUnit report produced._\n")
        return 0

    try:
        root = ET.parse(path).getroot()
    except ET.ParseError as exc:
        print(f"### Test results\n\n_Could not parse JUnit XML: {exc}_\n")
        return 0

    suites = list(root.iter("testsuite"))
    failures = []
    total_tests = 0
    total_failed = 0
    total_skipped = 0
    total_time = 0.0

    for suite in suites:
        try:
            total_tests += int(suite.attrib.get("tests", "0"))
            total_failed += int(suite.attrib.get("failures", "0"))
            total_skipped += int(suite.attrib.get("skipped", "0"))
            total_time += float(suite.attrib.get("time", "0"))
        except ValueError:
            pass

        pkg = suite.attrib.get("name", "?")
        for case in suite.iter("testcase"):
            failure = case.find("failure")
            if failure is None:
                continue
            failures.append({
                "pkg": pkg,
                "test": case.attrib.get("name", "?"),
                "time": case.attrib.get("time", "0"),
                "message": (failure.attrib.get("message", "") or "").strip(),
                "body": (failure.text or "").strip(),
            })

    print("### Test results")
    print()
    print(f"- Total: **{total_tests}**")
    print(f"- Failed: **{total_failed}**")
    print(f"- Skipped: **{total_skipped}**")
    print(f"- Wall time: **{total_time:.1f}s**")
    print()

    if not failures:
        print("All tests passed.")
        return 0

    print("### Failures")
    print()
    for f in failures[:MAX_FAILURES_SHOWN]:
        print(f"#### `{f['pkg']}` · `{f['test']}` ({f['time']}s)")
        print()
        if f["message"]:
            # truncate single-line message
            msg = f["message"].splitlines()[0][:300]
            print(f"> {msg}")
            print()
        if f["body"]:
            lines = f["body"].splitlines()
            shown = lines[:max_lines]
            print("```")
            print("\n".join(shown))
            if len(lines) > max_lines:
                print(f"... ({len(lines) - max_lines} more lines — see JUnit artifact)")
            print("```")
            print()

    if len(failures) > MAX_FAILURES_SHOWN:
        print(f"_+{len(failures) - MAX_FAILURES_SHOWN} more failures — see JUnit artifact._")

    return 0


if __name__ == "__main__":
    sys.exit(main())
