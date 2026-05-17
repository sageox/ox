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

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/security/.output"
RULES="$ROOT/security/rules"
BIN="$ROOT/bin"
mkdir -p "$OUT"

# Use workspace bin/ first so we get our pinned versions, not whatever's on $PATH.
export PATH="$BIN:$PATH"

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
  $OUT/findings-deterministic.json   merged JSON (jq-friendly)
  $OUT/findings-deterministic.sarif  merged SARIF for GitHub Security tab
  $OUT/det-<tool>.{json,sarif,log}   per-tool raw output (debugging)
EOF
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

# --- Compute touched files (diff scope) ------------------------------------
TOUCHED_FILE="$OUT/det-touched.txt"
if [[ "$SCOPE" == "diff" ]]; then
  git diff --name-only "$SINCE"...HEAD > "$TOUCHED_FILE" || git diff --name-only "$SINCE" > "$TOUCHED_FILE"
  if [[ ! -s "$TOUCHED_FILE" ]]; then
    echo "deterministic: no changes vs $SINCE — exiting cleanly with empty findings"
    echo '{"findings":[]}' > "$OUT/findings-deterministic.json"
    exit 0
  fi
else
  : > "$TOUCHED_FILE"  # full scan; tools default to whole repo
fi

# --- Parallel runners -------------------------------------------------------
# Each tool writes to its own log + output file, then we merge.
# Failures don't stop other tools; merge phase notes which tool was missing.

run_opengrep() {
  if ! command -v opengrep >/dev/null; then
    echo "opengrep: not installed (run make sec-install)" > "$OUT/det-opengrep.log"
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

  local args=("${rules_args[@]}" --sarif --output "$OUT/det-opengrep.sarif")
  if [[ "$SCOPE" == "diff" ]]; then
    # Pass each touched path as its own argv entry — file paths with spaces or
    # glob metacharacters break under unquoted `$(cat …)` word-splitting.
    local files=()
    while IFS= read -r f; do
      [[ -n "$f" ]] && files+=("$f")
    done < "$TOUCHED_FILE"
    opengrep "${args[@]}" "${files[@]}" > "$OUT/det-opengrep.log" 2>&1 || true
  else
    opengrep "${args[@]}" "$ROOT" > "$OUT/det-opengrep.log" 2>&1 || true
  fi
}

run_govulncheck() {
  if ! command -v govulncheck >/dev/null; then
    echo "govulncheck: not installed" > "$OUT/det-govulncheck.log"
    return 0
  fi
  # Reachability is the whole point — run from the repo root where go.mod lives.
  ( cd "$ROOT" && govulncheck -json ./... > "$OUT/det-govulncheck.json" ) 2> "$OUT/det-govulncheck.log" || true
}

run_osv_scanner() {
  if ! command -v osv-scanner >/dev/null; then
    echo "osv-scanner: not installed" > "$OUT/det-osv.log"
    return 0
  fi
  osv-scanner --format json --recursive "$ROOT" > "$OUT/det-osv.json" 2> "$OUT/det-osv.log" || true
}

run_syft_grype() {
  if ! command -v syft >/dev/null || ! command -v grype >/dev/null; then
    echo "syft/grype: not installed" > "$OUT/det-grype.log"
    return 0
  fi
  syft "$ROOT" -o cyclonedx-json="$OUT/det-sbom.cdx.json" > "$OUT/det-syft.log" 2>&1 || return 0
  grype sbom:"$OUT/det-sbom.cdx.json" -o sarif > "$OUT/det-grype.sarif" 2> "$OUT/det-grype.log" || true
}

run_gosec() {
  if ! command -v golangci-lint >/dev/null; then
    echo "golangci-lint: not installed (run make lint)" > "$OUT/det-gosec.log"
    return 0
  fi
  # Extract gosec findings from golangci-lint output
  ( cd "$ROOT" && golangci-lint run --enable=gosec --out-format=json ./... > "$OUT/det-gosec.json" 2> "$OUT/det-gosec.log" ) || true
}

# Launch all in parallel.
PIDS=()
run_opengrep    & PIDS+=($!)
run_govulncheck & PIDS+=($!)
run_osv_scanner & PIDS+=($!)
run_syft_grype  & PIDS+=($!)
run_gosec       & PIDS+=($!)
wait "${PIDS[@]}" || true

# --- Merge into a single findings JSON -------------------------------------
# Minimal merge: enumerate each tool's output, flatten to a common shape.
# The orchestrator's dedup phase does the heavy normalization; this is just
# "everything in one place" so the AI map phase has a single artifact to load.
python3 - <<'PYEOF' > "$OUT/findings-deterministic.json"
import json, glob, os, pathlib, sys
out_dir = pathlib.Path(os.environ.get("OUT", "security/.output"))
findings = []

def load_sarif(path, tool):
    try:
        d = json.loads(pathlib.Path(path).read_text())
    except Exception:
        return
    for run in d.get("runs", []):
        for r in run.get("results", []):
            findings.append({
                "tool": tool,
                "ruleId": r.get("ruleId", ""),
                "level": r.get("level", "warning"),
                "message": (r.get("message", {}) or {}).get("text", ""),
                "locations": [
                    {
                        "file": (loc.get("physicalLocation", {}).get("artifactLocation", {}) or {}).get("uri", ""),
                        "line": (loc.get("physicalLocation", {}).get("region", {}) or {}).get("startLine", 0),
                    }
                    for loc in r.get("locations", [])
                ],
            })

# Each *.sarif from the per-tool runs.
for sarif in sorted(out_dir.glob("det-*.sarif")):
    tool = sarif.stem.replace("det-", "").split("-")[0]
    load_sarif(sarif, tool)

# govulncheck JSON streaming — simplified ingest.
gv = out_dir / "det-govulncheck.json"
if gv.exists():
    for line in gv.read_text().splitlines():
        try:
            obj = json.loads(line)
        except Exception:
            continue
        if "finding" in obj:
            f = obj["finding"]
            findings.append({
                "tool": "govulncheck",
                "ruleId": f.get("osv", ""),
                "level": "error",
                "message": f.get("summary", ""),
                "locations": [],
                "reachable": True,  # govulncheck only reports reachable vulns
            })

# osv-scanner JSON.
osv = out_dir / "det-osv.json"
if osv.exists():
    try:
        d = json.loads(osv.read_text())
    except Exception:
        d = {}
    for r in d.get("results", []):
        for pkg in r.get("packages", []):
            for v in pkg.get("vulnerabilities", []):
                findings.append({
                    "tool": "osv-scanner",
                    "ruleId": v.get("id", ""),
                    "level": "warning",
                    "message": v.get("summary", ""),
                    "locations": [{"file": r.get("source", {}).get("path", ""), "line": 0}],
                })

# gosec via golangci-lint JSON.
gosec = out_dir / "det-gosec.json"
if gosec.exists():
    try:
        d = json.loads(gosec.read_text())
    except Exception:
        d = {}
    for issue in d.get("Issues", []):
        if issue.get("FromLinter") == "gosec":
            findings.append({
                "tool": "gosec",
                "ruleId": issue.get("RuleId", ""),
                "level": "warning",
                "message": issue.get("Text", ""),
                "locations": [{
                    "file": issue.get("Pos", {}).get("Filename", ""),
                    "line": issue.get("Pos", {}).get("Line", 0),
                }],
            })

print(json.dumps({"findings": findings, "count": len(findings)}, indent=2))
PYEOF

# --- SUMMARY block ----------------------------------------------------------
# Single grep-friendly block at the end, per ~/.claude/rules/human-time.md
# ("script should output a structured SUMMARY block ... so the human can scan").
echo
echo "[security-review] phase=deterministic status=complete scope=$SCOPE since=$SINCE"
echo "scope: $SCOPE (since=$SINCE)"
echo "touched_files: $(wc -l < "$TOUCHED_FILE" | tr -d ' ')"
for tool in opengrep govulncheck osv-scanner syft grype gosec; do
  log="$OUT/det-${tool%%-*}.log"
  if [[ ! -f "$log" ]]; then
    printf "  %-14s %s\n" "$tool:" "(no log)"
  elif grep -q "not installed" "$log" 2>/dev/null; then
    printf "  %-14s %s\n" "$tool:" "skipped (not installed)"
  else
    printf "  %-14s %s\n" "$tool:" "ran"
  fi
done
findings_count=$(jq -r '.count' "$OUT/findings-deterministic.json" 2>/dev/null || echo "?")
echo "merged_findings: $findings_count"

echo "READY: deterministic.sh completed parallel security scanner run"
