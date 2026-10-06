#!/usr/bin/env bash
# security/scripts/orchestrate.sh — 6-phase AI security review driver for ox CLI.
#
# Source: https://www.synthesia.io/post/automating-code-security-reviews-with-claude-mythos-level-capabilities
# Phases: prep → map → hunt → dedup → validate → aggregate.
# Right-size models per phase: Haiku (cartographer), Sonnet (hunters, dedup, default validator),
# Opus only for the 5 hard validation classes (authz / cryptography / multi-hop-taint /
# agent-tool-abuse / exploitability-dispute).
#
# Usage:
#   bash security/scripts/orchestrate.sh                       # diff vs origin/main, default config
#   bash security/scripts/orchestrate.sh --full                # full-repo scan
#   bash security/scripts/orchestrate.sh --scope=cmd/ox/       # narrow to a path
#   bash security/scripts/orchestrate.sh --hunter=cli-input    # run only one hunter (debug)
#   bash security/scripts/orchestrate.sh --rerun               # re-run, dedupe vs previous run
#   bash security/scripts/orchestrate.sh --cap=10              # raise per-run cost cap (USD)
#   bash security/scripts/orchestrate.sh --since=<ref>         # diff against alternate base
#
# Exit codes:
#   0  every stage that ran completed (a scanner skipped as not installed still lands here)
#   1  a stage failed (scanner, cartographer, hunter or dedup error): partial coverage
#   2  the cost cap stopped the AI phases: partial coverage; re-run with --cap=<higher>
#   3  NO COVERAGE: a required stage did not run, so zero findings means nothing
#
# This driver is shelled to by:
#   - .claude/skills/security-review/SKILL.md (interactive Claude Code)
#   - make sec (non-interactive)
#   - .github/workflows/security-review.yml (fast tier only)
#
# What the AI phases read: every cartographer and hunter call gets the real change
# — `git diff <since>...HEAD`, test files excluded, chunked under diff.chunk_bytes —
# plus scope.md, as a packet built by pipeline.py; scanner JSON is supplementary.
# (They used to get only the scanner JSON and a map drawn from it. With no scanner
# installed, every hunter reviewed an empty map and the run reported "ran clean".)
# pipeline.py's aggregate step then applies a coverage gate: FINDINGS.md says
# "ran clean" only when every stage actually ran.
#
# AI subagents are spawned via the `claude` CLI (subsidized when run interactively, API-billed
# when run from CI). The cost cap is enforced from each call's reported `total_cost_usd`.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/security/.output"
SKILL="$ROOT/.claude/skills/security-review"
CONFIG="$ROOT/security/config.yml"
BIN="$ROOT/bin"
LIB="$ROOT/security/scripts/pipeline.py"
mkdir -p "$OUT"
export PATH="$BIN:$PATH"
# Git pathspecs and the subagents' Read/Grep paths are all repo-relative.
cd "$ROOT"

# --- Args -------------------------------------------------------------------
SCOPE_ARG=""
HUNTER_ARG=""
RERUN=0
CAP_USD=""
SCAN_FULL=0
SINCE="origin/main"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --full)             SCAN_FULL=1; shift ;;
    --scope=*)          SCOPE_ARG="${1#--scope=}"; shift ;;
    --hunter=*)         HUNTER_ARG="${1#--hunter=}"; shift ;;
    --rerun)            RERUN=1; shift ;;
    --cap=*)            CAP_USD="${1#--cap=}"; shift ;;
    --since=*)          SINCE="${1#--since=}"; shift ;;
    --help|-h)
      sed -n '/^# Usage:/,/^$/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

# config_value <key> <default> — first `key: value` line in config.yml, at any indent.
config_value() {
  local v=""
  if [[ -f "$CONFIG" ]]; then
    v="$(awk -v k="$1" '$1 == k":" {print $2; exit}' "$CONFIG" 2>/dev/null || true)"
  fi
  echo "${v:-$2}"
}
if [[ -z "$CAP_USD" ]]; then
  CAP_USD="$(config_value cost_cap_usd 2)"
fi
CHUNK_BYTES="$(config_value chunk_bytes 120000)"
MAX_CHUNKS="$(config_value max_chunks 8)"

# --- Reset per-run artifacts -------------------------------------------------
# A run that stops early must not leave the previous run's FINDINGS.md behind:
# a stale "ran clean" report is the failure this pipeline exists to prevent.
# run-log.md is the cross-run history and stays. deterministic.sh resets det-*.
rm -rf "$OUT/review"
rm -f "$OUT"/FINDINGS.md "$OUT"/findings.sarif "$OUT"/coverage.json "$OUT"/scope.md \
  "$OUT"/surface.md "$OUT"/surface.json "$OUT"/cartographer-*.json "$OUT"/hunter-*.jsonl \
  "$OUT"/hunter-status.tsv "$OUT"/findings-*.jsonl "$OUT"/dedup-status "$OUT"/.validate-* \
  "$OUT"/.cost "$OUT"/.cost-ledger "$OUT"/.cap-hit "$OUT"/.malformed.jsonl "$OUT"/.claude-raw.* \
  "$OUT"/.sanitize.*

# If anything kills the run before the aggregate step writes a report, leave a
# report that says so rather than none (or, before the reset above, a stale one).
PHASE="prep"
write_crash_report() {
  local rc=$?
  if [[ ! -f "$OUT/FINDINGS.md" ]]; then
    printf -- '---\ncoverage: none\n---\n\n# Findings\n\n> **NO COVERAGE** — the pipeline stopped during the %s phase (exit %s) before writing a report; nothing was reviewed to completion. See the orchestrator output and `run-log.md`.\n' \
      "$PHASE" "$rc" > "$OUT/FINDINGS.md"
  fi
}
trap write_crash_report EXIT

# --- JSONL sanitizer --------------------------------------------------------
# AI subagents sometimes return prose even when prompted for strict JSONL
# (markdown headers, code fences, "Here are the findings:" prefaces, etc.).
# Filter a file in place: keep only lines that parse as JSON objects, dump
# the rest to .malformed.jsonl for debugging. Never crashes the pipeline.
sanitize_jsonl() {
  # sanitize_jsonl <input-file> <phase-tag>
  local infile="$1" tag="$2"
  local clean malformed
  clean="$(mktemp "$OUT/.sanitize.clean.XXXXXX")"
  malformed="$OUT/.malformed.jsonl"
  [[ -f "$infile" ]] || return 0
  # Strip code fences and common chat prefaces first, then per-line filter.
  python3 - "$infile" "$clean" "$malformed" "$tag" <<'PYEOF'
import json, sys, re
infile, clean, malformed, tag = sys.argv[1:5]
kept = dropped = 0
fence_re = re.compile(r"^\s*```")
with open(infile) as f, open(clean, "w") as out, open(malformed, "a") as bad:
    for raw in f:
        line = raw.rstrip("\n")
        if not line.strip():
            continue
        if fence_re.match(line):
            continue
        # Permit a JSON object even if the model wrapped it in trailing prose
        # by trying to slice from the first { to the last }.
        candidate = line
        try:
            obj = json.loads(candidate)
        except Exception:
            l, r = candidate.find("{"), candidate.rfind("}")
            if l != -1 and r != -1 and r > l:
                try:
                    obj = json.loads(candidate[l : r + 1])
                except Exception:
                    obj = None
            else:
                obj = None
        if isinstance(obj, dict):
            out.write(json.dumps(obj) + "\n")
            kept += 1
        elif isinstance(obj, list):
            for item in obj:
                if isinstance(item, dict):
                    out.write(json.dumps(item) + "\n")
                    kept += 1
                else:
                    bad.write(f"{tag}\t{json.dumps(item)}\n")
                    dropped += 1
        else:
            bad.write(f"{tag}\t{line}\n")
            dropped += 1
print(f"sanitize[{tag}]: kept={kept} dropped={dropped}", file=sys.stderr)
PYEOF
  mv "$clean" "$infile"
}

# --- Cost tracker -----------------------------------------------------------
# One line per subagent call: "<usd>\t<label>\t<model>". Appended, never
# rewritten — parallel hunters each append once, and an O_APPEND write this small
# is atomic (the old read-modify-write of a single total lost concurrent updates).
COST_LEDGER="$OUT/.cost-ledger"
CAP_FILE="$OUT/.cap-hit"
: > "$COST_LEDGER"
total_cost() { awk -F'\t' '{s += $1} END {printf "%.4f", s + 0}' "$COST_LEDGER"; }
remaining_budget() {
  awk -v cap="$CAP_USD" -v cur="$(total_cost)" 'BEGIN {r = cap - cur; printf "%.4f", (r > 0 ? r : 0)}'
}
# The first call to hit the cap names the phase; later calls see the file and stop.
mark_cap_hit() { [[ -f "$CAP_FILE" ]] || echo "$1" > "$CAP_FILE"; }
# budget_or_cap <label> — print the remaining budget, or record the cap hit and
# return 1 when less than a cent is left.
budget_or_cap() {
  local r
  r="$(remaining_budget)"
  if [[ -f "$CAP_FILE" ]] || awk -v r="$r" 'BEGIN {exit !(r < 0.01)}'; then
    mark_cap_hit "$1"
    echo "cost cap (\$$CAP_USD) reached; skipped $1" >> "$OUT/run-log.md"
    return 1
  fi
  echo "$r"
}

# invoke_claude <model> <prompt-file> <input-file> <output-file> <label> [json-schema-path] [wave-budget]
#
# Runs one subagent in print mode: the playbook is the appended system prompt and
# <input-file> (a packet from pipeline.py) is stdin. The payload — structured_output
# under --json-schema — lands in <output-file>; when there is none, a
# {"verdict": "error"|"cap"|"skipped"} stub does, which pipeline.py classifies
# instead of mistaking for output.
#
# Cost: the CLI reports what each call spent in `total_cost_usd`, and every call's
# cost goes into the ledger — including a call the CLI stopped at --max-budget-usd,
# which exits 1 but still spent money. The cap is checked before each call, and
# each call gets the remaining budget as its --max-budget-usd. A hunt wave is
# checked once, by the parent, and every hunter in it gets the same
# [wave-budget]: the wave can overshoot the cap by at most its own cost, and
# whether a hunter runs never depends on which sibling finished first.
# CC_SUBSIDIZED=1 (interactive Claude Code subsidy) records cost for the summary
# but enforces nothing.
#
# Returns 1 when the cap stopped this call, 0 otherwise: a failed call reports
# through its stub and never aborts the pipeline.
invoke_claude() {
  local model="$1" prompt="$2" input="$3" output="$4" label="$5" schema="${6:-}" wave_budget="${7:-}"

  if ! command -v claude >/dev/null 2>&1; then
    echo "WARNING: claude CLI not installed; skipping $label (see claude.ai/code)" | tee -a "$OUT/run-log.md"
    echo '{"verdict":"skipped","reason":"claude CLI missing"}' > "$output"
    return 0
  fi

  local budget=""
  if [[ "${CC_SUBSIDIZED:-0}" != "1" ]]; then
    if [[ -n "$wave_budget" ]]; then
      budget="$wave_budget"
    elif ! budget="$(budget_or_cap "$label")"; then
      echo '{"verdict":"cap","reason":"cost cap reached"}' > "$output"
      return 1
    fi
  fi

  # Build the command. --print = non-interactive; --append-system-prompt loads
  # the playbook; --output-format json gives us cost + token usage we can
  # attribute back to the run-log.
  local cli_args=(
    --print
    --model "$model"
    --append-system-prompt "$(cat "$prompt")"
    --output-format json
    # NOTE: --bare strips keychain auth too, breaking subagent login. Run without it
    # and tolerate hooks/auto-memory noise rather than no findings at all.
    #
    # --setting-sources user: only load ~/.claude/settings.json, not project
    # .claude/settings.json. The project SessionStart hook runs `ox agent prime`, which
    # may exit 1 in some environments and can derail subagent startup. User settings
    # still give us OAuth/keychain auth.
    --setting-sources user
    # --no-session-persistence: subagent invocations are one-shot. We don't want
    # them appearing in /resume pickers or polluting session history.
    --no-session-persistence
    # --permission-mode dontAsk: subagents must not pop a permission prompt
    # mid-run (we're piping stdout to a parser).
    --permission-mode dontAsk
    # --tools: read-only file tools and nothing else, whatever the user's
    # permission rules allow. Subagents read the repo to trace what the diff
    # doesn't show; they never run commands or edit files.
    --tools "Read,Grep,Glob"
  )
  if [[ -n "$budget" ]]; then
    cli_args+=(--max-budget-usd "$budget")
  fi
  if [[ -n "$schema" ]] && [[ -f "$schema" ]]; then
    # --json-schema forces structured output: the parsed object lands in the
    # envelope's .structured_output.
    cli_args+=(--json-schema "$(cat "$schema")")
  fi

  # mktemp + $BASHPID so parallel hunters don't clobber each other's raw output —
  # $$ is the parent shell's PID, shared across the subshells of one hunt wave.
  local raw rc=0 parsed cost subtype is_error denials
  raw="$(mktemp "$OUT/.claude-raw.${BASHPID:-$$}.$(basename "$prompt").XXXXXX")"
  claude "${cli_args[@]}" < "$input" > "$raw" 2>>"$OUT/run-log.md" || rc=$?
  # Parse the envelope whatever the exit code: a budget stop exits 1 but still
  # reports what it spent.
  parsed="$(python3 "$LIB" envelope --raw "$raw" --output "$output" ${schema:+--structured})" \
    || parsed=$'0\tenvelope-parse-crash\ttrue\t0'
  rm -f "$raw"
  IFS=$'\t' read -r cost subtype is_error denials <<< "$parsed"
  printf '%s\t%s\t%s\n' "${cost:-0}" "$label" "$model" >> "$COST_LEDGER"
  if [[ "${denials:-0}" != "0" ]]; then
    echo "$label: $denials tool call(s) denied" >> "$OUT/run-log.md"
  fi
  if [[ "$subtype" == "error_max_budget_usd" ]]; then
    mark_cap_hit "$label"
    echo "WARNING: $label stopped at the cost cap (\$$CAP_USD)" | tee -a "$OUT/run-log.md"
    echo '{"verdict":"cap","reason":"stopped at --max-budget-usd"}' > "$output"
    return 1
  fi
  if (( rc != 0 )) || [[ "$is_error" == "true" ]]; then
    echo "WARNING: claude CLI failed (exit $rc, ${subtype:-no envelope}) for $label; see run-log" | tee -a "$OUT/run-log.md"
  fi
  return 0
}

# run_hunter <hunter> <chunk> <wave-budget> — one hunter on one chunk. Runs in a
# background subshell; its status row in hunter-status.tsv is the only thing the
# parent reads.
run_hunter() {
  local h="$1" n="$2" wave_budget="$3" nn
  nn="$(printf '%02d' "$n")"
  invoke_claude "claude-sonnet-5" "$SKILL/prompts/hunter-${h}.md" "$OUT/review/packet-hunter-c${nn}.md" \
    "$OUT/hunter-${h}-c${nn}.jsonl" "hunt:${h}:c${nn}" "$SKILL/schemas/hunter.json" "$wave_budget" || true
  python3 "$LIB" hunter-result --out "$OUT" --hunter "$h" --chunk "$n"
  sanitize_jsonl "$OUT/hunter-${h}-c${nn}.jsonl" "hunter-${h}-c${nn}"
}

# --- Phase 1: PREP ----------------------------------------------------------
echo
echo "[1/6] prep ........................................"
if [[ "$SCAN_FULL" == "1" ]]; then
  MODE="full"
elif [[ -n "$SCOPE_ARG" ]]; then
  MODE="scope"
else
  MODE="diff"
fi
{
  echo "# Scope"
  echo
  if [[ "$SCAN_FULL" == "1" ]]; then
    echo "- mode: **full repo scan**"
  elif [[ -n "$SCOPE_ARG" ]]; then
    echo "- mode: **narrowed scope** to \`$SCOPE_ARG\`"
  else
    echo "- mode: **diff vs $SINCE**"
  fi
  echo "- branch: $(git rev-parse --abbrev-ref HEAD)"
  echo "- head: $(git rev-parse --short HEAD)"
  echo "- date: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  echo
  echo "## Touched files"
  echo
  if [[ "$SCAN_FULL" == "1" ]]; then
    echo "(full scan — every tracked file)"
  elif [[ -n "$SCOPE_ARG" ]]; then
    git ls-files "$SCOPE_ARG" | sed 's/^/- /'
  else
    { git diff --name-only "$SINCE"...HEAD 2>/dev/null || git diff --name-only "$SINCE" 2>/dev/null \
        || echo "(cannot diff against $SINCE)"; } | sed 's/^/- /'
  fi
} > "$OUT/scope.md"
echo "       wrote $OUT/scope.md"

CHUNKS=0
if python3 "$LIB" build-input --out "$OUT/review" --mode "$MODE" --since "$SINCE" \
    ${SCOPE_ARG:+--scope "$SCOPE_ARG"} --chunk-bytes "$CHUNK_BYTES" --max-chunks "$MAX_CHUNKS" \
    --scope-md "$OUT/scope.md"; then
  CHUNKS="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["chunks_kept"])' "$OUT/review/manifest.json")"
else
  echo "WARNING: could not build the review input; AI phases skipped" | tee -a "$OUT/run-log.md"
fi

# --- Phase 2: MAP -----------------------------------------------------------
PHASE="map"
echo "[2/6] map (deterministic + cartographer) .........."
det_args=()
[[ "$SCAN_FULL" == "1" ]] && det_args+=(--full)
[[ "$SINCE" != "origin/main" ]] && det_args+=(--since "$SINCE")
bash "$ROOT/security/scripts/deterministic.sh" ${det_args[@]+"${det_args[@]}"} > "$OUT/det-runner.log" 2>&1 || true
grep -E '^  [a-z-]+: ' "$OUT/det-runner.log" | sed 's/^/     /' || true
echo "       deterministic: see $OUT/det-runner.log + $OUT/findings-deterministic.json"

for (( n = 1; n <= CHUNKS; n++ )); do
  nn="$(printf '%02d' "$n")"
  python3 "$LIB" packet --role cartographer --review "$OUT/review" --chunk "$n" \
    --scope-md "$OUT/scope.md" --det "$OUT/findings-deterministic.json" \
    --out "$OUT/review/packet-cartographer-c${nn}.md"
  invoke_claude "claude-haiku-4-5" "$SKILL/prompts/cartographer.md" "$OUT/review/packet-cartographer-c${nn}.md" \
    "$OUT/cartographer-c${nn}.json" "map:cartographer:c${nn}" "$SKILL/schemas/cartographer.json" || true
done
python3 "$LIB" surface --out "$OUT"
echo "       wrote $OUT/surface.md"

# --- Phase 3: HUNT ----------------------------------------------------------
# One wave per chunk: the five hunters run in parallel on it, each with an
# explicit perspective frame to fight finding convergence. A wave finishes
# before the next starts, so the cap is re-checked between waves.
PHASE="hunt"
echo "[3/6] hunt (parallel hunters) ....................."
HUNTERS=(cli-input secrets-redaction daemon-ipc supply-chain llm-trust)
[[ -n "$HUNTER_ARG" ]] && HUNTERS=("$HUNTER_ARG")

: > "$OUT/hunter-status.tsv"
for (( n = 1; n <= CHUNKS; n++ )); do
  nn="$(printf '%02d' "$n")"
  python3 "$LIB" packet --role hunter --review "$OUT/review" --chunk "$n" \
    --scope-md "$OUT/scope.md" --surface-md "$OUT/surface.md" --det "$OUT/findings-deterministic.json" \
    --out "$OUT/review/packet-hunter-c${nn}.md"
  wave_budget=""
  wave_capped=0
  if [[ "${CC_SUBSIDIZED:-0}" != "1" ]] && ! wave_budget="$(budget_or_cap "hunt:c${nn}")"; then
    wave_capped=1
  fi
  hunter_pids=()
  for h in "${HUNTERS[@]}"; do
    if [[ ! -f "$SKILL/prompts/hunter-${h}.md" ]]; then
      echo "       (skipping $h — no playbook at $SKILL/prompts/hunter-${h}.md)"
      printf '%s\t%s\tskipped-no-prompt\t0\n' "$h" "$n" >> "$OUT/hunter-status.tsv"
      continue
    fi
    if (( wave_capped )); then
      printf '%s\t%s\tcap\t0\n' "$h" "$n" >> "$OUT/hunter-status.tsv"
      continue
    fi
    run_hunter "$h" "$n" "$wave_budget" &
    hunter_pids+=($!)
  done
  if (( ${#hunter_pids[@]} > 0 )); then
    wait "${hunter_pids[@]}" || true
  fi
done
# Concat after every wave so appends from racing subshells never interleave.
: > "$OUT/findings-raw.jsonl"
for f in "$OUT"/hunter-*-c*.jsonl; do
  if [[ -f "$f" ]]; then cat "$f" >> "$OUT/findings-raw.jsonl"; fi
done
echo "       wrote $OUT/findings-raw.jsonl ($(wc -l < "$OUT/findings-raw.jsonl" | tr -d ' ') findings before dedup)"

# --- Phase 4: DEDUP ---------------------------------------------------------
PHASE="dedup"
echo "[4/6] dedup (root-cause merge) ...................."
if [[ ! -s "$OUT/findings-raw.jsonl" ]]; then
  python3 "$LIB" dedup-result --out "$OUT" --skipped empty
elif [[ ! -f "$SKILL/prompts/dedup.md" ]]; then
  python3 "$LIB" dedup-result --out "$OUT" --skipped no-prompt
else
  invoke_claude "claude-sonnet-5" "$SKILL/prompts/dedup.md" \
    "$OUT/findings-raw.jsonl" "$OUT/findings-deduped.jsonl" "dedup" \
    "$SKILL/schemas/dedup.json" || true
  python3 "$LIB" dedup-result --out "$OUT"
  sanitize_jsonl "$OUT/findings-deduped.jsonl" "dedup"
fi
echo "       wrote $OUT/findings-deduped.jsonl ($(wc -l < "$OUT/findings-deduped.jsonl" | tr -d ' ') after dedup)"

# --- Phase 5: VALIDATE -----------------------------------------------------
# Per-finding loop. Sonnet for the default ~90%; Opus for the 5 hard classes.
# Synthesia's validator is "deliberately stricter than hunters" — discards ~60% of
# hunter findings as false positives. A finding that cannot be validated (cap hit,
# CLI failure) is kept and marked UNVALIDATED, never dropped.
PHASE="validate"
echo "[5/6] validate (Sonnet ~90% / Opus on hard classes)"
OPUS_CLASSES="authz cryptography multi-hop-taint agent-tool-abuse exploitability-dispute"
: > "$OUT/findings-validated.jsonl"
vi=0
while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  vi=$((vi + 1))
  printf '%s\n' "$line" > "$OUT/.validate-input"
  if [[ ! -f "$SKILL/prompts/validator.md" ]]; then
    python3 "$LIB" validator-result --finding "$OUT/.validate-input" --unvalidated "no validator playbook" \
      >> "$OUT/findings-validated.jsonl"
    continue
  fi
  cls=$(echo "$line" | jq -r '.class // ""' 2>/dev/null || echo "")
  model="claude-sonnet-5"
  for opus_cls in $OPUS_CLASSES; do
    [[ "$cls" == "$opus_cls" ]] && { model="claude-opus-5-5"; break; }
  done
  if ! python3 "$LIB" packet --role validator --review "$OUT/review" --finding "$OUT/.validate-input" \
      --out "$OUT/.validate-packet.md"; then
    python3 "$LIB" validator-result --finding "$OUT/.validate-input" --unvalidated "could not build the validator input" \
      >> "$OUT/findings-validated.jsonl"
    continue
  fi
  invoke_claude "$model" "$SKILL/prompts/validator.md" \
    "$OUT/.validate-packet.md" "$OUT/.validate-output" "validate:$vi" \
    "$SKILL/schemas/validator.json" || true
  python3 "$LIB" validator-result --finding "$OUT/.validate-input" --output "$OUT/.validate-output" \
    >> "$OUT/findings-validated.jsonl"
done < "$OUT/findings-deduped.jsonl"
echo "       wrote $OUT/findings-validated.jsonl"

# --- Phase 6: AGGREGATE ----------------------------------------------------
# Coverage gate, ranking, FINDINGS.md + SARIF, the run-log entry and the SUMMARY
# block all come from pipeline.py; its exit code is this script's (see header).
PHASE="aggregate"
echo "[6/6] aggregate (coverage gate + rank + emit) ....."
if [[ "$SCAN_FULL" == "1" ]]; then
  scope_line="full"
elif [[ -n "$SCOPE_ARG" ]]; then
  scope_line="narrowed:$SCOPE_ARG"
else
  scope_line="diff vs $SINCE"
fi
agg_rc=0
python3 "$LIB" aggregate --out "$OUT" --cap "$CAP_USD" --subsidized "${CC_SUBSIDIZED:-0}" \
  --hunters "${HUNTERS[*]}" --skill "$SKILL" --scope-line "$scope_line" \
  --head "$(git rev-parse --short HEAD)" || agg_rc=$?

level="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["level"])' "$OUT/coverage.json" 2>/dev/null || echo unknown)"
echo "READY: orchestrate.sh completed 6-phase security review (coverage: $level)"
exit "$agg_rc"
