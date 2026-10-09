#!/usr/bin/env bash
# security/scripts/deterministic.sh — parallel OSS scanner runner for ox CLI.
#
# Runs all deterministic security scanners in parallel against the diff (or full repo if --full).
# Merges output into security/.output/findings-deterministic.{json,sarif} for the AI map phase to consume.
#
# Source for the 6-phase pipeline this is the deterministic input to:
#   https://www.synthesia.io/post/automating-code-security-reviews-with-claude-mythos-level-capabilities
# Synthesia's Phase 2 ("Mapping") uses Semgrep entry-point rules + parallel Cartographer subagents.
# This script is the OSS-tool half of that phase. The AI Cartographer is launched by orchestrate.sh.
#
# WHY THESE TOOLS AND NOT OTHERS (May 2026):
#
#   OpenGrep — FOSS Semgrep fork (LGPL-2.1) maintained by Aikido / Endor Labs / Jit / Orca after
#   Semgrep moved cross-function taint analysis behind their commercial platform in late 2024.
#   Rule format byte-compatible with Semgrep, so all community rules + our custom YAML run unchanged.
#   See https://appsecsanta.com/sast-tools/opengrep-vs-semgrep
#
#   govulncheck — official Go tool. Lowest false-positive rate of any SCA tool because it uses
#   call-graph reachability — only reports vulns in code paths actually invoked.
#
#   OSV-Scanner — Google. Uses OSV.dev advisories. Multi-ecosystem. Reachability annotations
#   for some ecosystems (more arriving).
#
#   Syft + Grype — Anchore. REPLACES TRIVY. Trivy was compromised twice in March 2026:
#     - 2026-03-19: TeamPCP force-pushed 76 of 77 tags in aquasecurity/trivy-action and published
#       a malicious Trivy v0.69.4 binary via the compromised aqua-bot account.
#       https://github.com/aquasecurity/trivy/security/advisories/GHSA-69fq-xp46-6x23
#     - Days later: a second compromise of the v0.69.4 release flow.
#       https://www.stepsecurity.io/blog/trivy-compromised-a-second-time---malicious-v0-69-4-release
#   Trivy was patched in both cases, but the May-2026 community position is that trust is broken
#   for a tool that ships in CI's critical path. Aqua's commercial fork is unaffected (controlled
#   integration lag), but it isn't OSS. Future readers tempted to "just add Trivy back": don't.
#
# STATUS IS EVIDENCE, NOT ASSUMPTION: each scanner's exit code and output decide
# whether it "ran", was "skipped" (not installed), or "failed" — pipeline.py
# det-merge does the classifying. This tier used to append `|| true` to every
# tool and grep the log for "not installed", so a gosec invocation that exited 3
# on a removed golangci-lint flag was still reported as "ran" with 0 findings.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/security/.output"
RULES="$ROOT/security/rules"
BIN="$ROOT/bin"
LIB="$ROOT/security/scripts/pipeline.py"
mkdir -p "$OUT"

# Use workspace bin/ first so we get our pinned versions, not whatever's on $PATH.
export PATH="$BIN:$PATH"
# Touched paths are repo-relative, and so are the scanners' targets.
cd "$ROOT"

# --- Args -------------------------------------------------------------------
SCOPE="diff"        # "diff" (default) or "full"
SINCE="origin/main" # diff base
while [[ $# -gt 0 ]]; do
  case "$1" in
    --full) SCOPE="full"; shift ;;
    --since) SINCE="$2"; shift 2 ;;
    --help|-h)
      cat <<EOF
Usage: $0 [--full] [--since <ref>]

  --full           Scan the entire repo, not just the diff.
  --since <ref>    Diff against this ref instead of origin/main.

Output:
  $OUT/findings-deterministic.json   merged JSON (jq-friendly), with per-scanner status
  $OUT/det-<tool>.{json,sarif,log}   per-tool raw output (debugging)
  $OUT/det-<tool>.exit               per-tool exit code ("missing" = not installed)

Exit codes: 0 every installed scanner ran · 1 a scanner failed · 3 no scanner ran (NO COVERAGE)
EOF
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

# Reset this tier's artifacts so a scanner that does not run this time cannot
# leave the last run's output behind to be merged as if it had. det-runner.log
# is spared: it is orchestrate.sh's capture of this very script's output.
for f in "$OUT"/det-* "$OUT"/findings-deterministic.json; do
  [[ "$f" == "$OUT/det-runner.log" ]] || rm -f "$f"
done

# --- Compute touched files (diff scope) ------------------------------------
TOUCHED_FILE="$OUT/det-touched.txt"
if [[ "$SCOPE" == "diff" ]]; then
  CHANGED="$(git diff --name-only "$SINCE"...HEAD 2>/dev/null || git diff --name-only "$SINCE")"
  if [[ -z "$CHANGED" ]]; then
    echo "deterministic: no changes vs $SINCE — nothing to scan"
    printf '{"findings": [], "count": 0, "tools": {}, "scope": "diff", "since": "%s", "coverage": "empty"}\n' \
      "$SINCE" > "$OUT/findings-deterministic.json"
    exit 0
  fi
  # A deleted file is a change, but there is nothing on disk to scan; passing
  # its path to opengrep fails the whole opengrep run.
  git diff --name-only --diff-filter=d "$SINCE"...HEAD > "$TOUCHED_FILE" 2>/dev/null \
    || git diff --name-only --diff-filter=d "$SINCE" > "$TOUCHED_FILE"
else
  : > "$TOUCHED_FILE"  # full scan; tools default to whole repo
fi

# --- Parallel runners -------------------------------------------------------
# Each tool writes its own log + output file and records its exit code, then
# det-merge classifies and merges. Failures don't stop other tools.

# record <tool> <exit-code | missing | nofiles>
record() { echo "$2" > "$OUT/det-$1.exit"; }

run_opengrep() {
  if ! command -v opengrep >/dev/null; then
    echo "opengrep: not installed (run make sec-install)" > "$OUT/det-opengrep.log"
    record opengrep missing
    return 0
  fi

  # Start with base rules
  local rules_args=()
  if [[ -f "$RULES/ox-entry-points.yml" ]]; then
    rules_args+=(--config "$RULES/ox-entry-points.yml")
  fi

  # Add overlay rules if OX_PRIVATE_RULES_DIR is set
  if [[ -n "${OX_PRIVATE_RULES_DIR:-}" ]] && [[ -d "$OX_PRIVATE_RULES_DIR" ]]; then
    for rule_file in "$OX_PRIVATE_RULES_DIR"/*.yml; do
      [[ -f "$rule_file" ]] && rules_args+=(--config "$rule_file")
    done
  fi

  local args=(${rules_args[@]+"${rules_args[@]}"} --sarif --output "$OUT/det-opengrep.sarif")
  local rc=0
  if [[ "$SCOPE" == "diff" ]]; then
    # Pass each touched path as its own argv entry — file paths with spaces or
    # glob metacharacters break under unquoted `$(cat …)` word-splitting.
    local files=()
    while IFS= read -r f; do
      [[ -n "$f" ]] && files+=("$f")
    done < "$TOUCHED_FILE"
    if (( ${#files[@]} == 0 )); then
      record opengrep nofiles
      return 0
    fi
    opengrep "${args[@]}" "${files[@]}" > "$OUT/det-opengrep.log" 2>&1 || rc=$?
  else
    opengrep "${args[@]}" "$ROOT" > "$OUT/det-opengrep.log" 2>&1 || rc=$?
  fi
  record opengrep "$rc"
}

run_govulncheck() {
  if ! command -v govulncheck >/dev/null; then
    echo "govulncheck: not installed" > "$OUT/det-govulncheck.log"
    record govulncheck missing
    return 0
  fi
  # Reachability is the whole point — run from the repo root where go.mod lives.
  local rc=0
  govulncheck -json ./... > "$OUT/det-govulncheck.json" 2> "$OUT/det-govulncheck.log" || rc=$?
  record govulncheck "$rc"
}

run_osv_scanner() {
  if ! command -v osv-scanner >/dev/null; then
    echo "osv-scanner: not installed" > "$OUT/det-osv.log"
    record osv-scanner missing
    return 0
  fi
  local rc=0
  osv-scanner --format json --recursive "$ROOT" > "$OUT/det-osv.json" 2> "$OUT/det-osv.log" || rc=$?
  record osv-scanner "$rc"
}

run_syft_grype() {
  if ! command -v syft >/dev/null || ! command -v grype >/dev/null; then
    echo "syft/grype: not installed" > "$OUT/det-grype.log"
    record grype missing
    return 0
  fi
  local rc=0
  syft "$ROOT" -o cyclonedx-json="$OUT/det-sbom.cdx.json" > "$OUT/det-syft.log" 2>&1 || rc=$?
  if (( rc != 0 )); then
    record grype "syft:$rc"
    return 0
  fi
  grype sbom:"$OUT/det-sbom.cdx.json" -o sarif > "$OUT/det-grype.sarif" 2> "$OUT/det-grype.log" || rc=$?
  record grype "$rc"
}

# Run gosec through golangci-lint v2, saving its JSON report and log under OUT.
# Record the exit code, or "missing" when golangci-lint is unavailable.
run_gosec() {
  if ! command -v golangci-lint >/dev/null; then
    echo "golangci-lint: not installed (gosec needs golangci-lint v2)" > "$OUT/det-gosec.log"
    record gosec missing
    return 0
  fi
  # golangci-lint v2 removed --out-format; JSON goes to --output.json.path. The v1
  # flag made every run exit 3 with empty output.
  # security/golangci-gosec.yml, not the lint config: it keeps the path and
  # permission rules `make lint` drops as noise, and leaves out the taint
  # analyzers that never finish on this module. det-merge keeps only issues in
  # touched files.
  local rc=0
  golangci-lint run -c "$ROOT/security/golangci-gosec.yml" --allow-parallel-runners --show-stats=false \
    --output.json.path="$OUT/det-gosec.json" ./... > "$OUT/det-gosec.log" 2>&1 || rc=$?
  record gosec "$rc"
}

# Launch all in parallel.
PIDS=()
run_opengrep    & PIDS+=($!)
run_govulncheck & PIDS+=($!)
run_osv_scanner & PIDS+=($!)
run_syft_grype  & PIDS+=($!)
run_gosec       & PIDS+=($!)
wait "${PIDS[@]}" || true

# --- Classify, merge, summarize ---------------------------------------------
# det-merge writes findings-deterministic.json (findings + per-scanner status for
# the AI map phase and the coverage gate) and prints the SUMMARY block, one
# grep-friendly line per scanner (per ~/.claude/rules/human-time.md: "script
# should output a structured SUMMARY block ... so the human can scan").
det_rc=0
python3 "$LIB" det-merge --out "$OUT" --root "$ROOT" --scope "$SCOPE" --since "$SINCE" \
  --touched "$TOUCHED_FILE" || det_rc=$?

echo "READY: deterministic.sh completed parallel security scanner run"
exit "$det_rc"
