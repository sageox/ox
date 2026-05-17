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
# This driver is shelled to by:
#   - .claude/skills/security-review/SKILL.md (interactive Claude Code)
#   - make sec (non-interactive)
#   - .github/workflows/security-review.yml (fast tier only)
#
# AI subagents are spawned via the `claude` CLI (subsidized when run interactively, API-billed
# when run from CI). The cost cap is enforced by tracking each subagent invocation's reported
# token usage and bailing the pipeline at the budget.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/security/.output"
SKILL="$ROOT/.claude/skills/security-review"
CONFIG="$ROOT/security/config.yml"
BIN="$ROOT/bin"
mkdir -p "$OUT"
export PATH="$BIN:$PATH"

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

# Read cap from config if not overridden on CLI.
if [[ -z "$CAP_USD" ]]; then
  if [[ -f "$CONFIG" ]]; then
    CAP_USD=$(awk '/^cost_cap_usd:/ {print $2; exit}' "$CONFIG" 2>/dev/null || echo "2")
  else
    CAP_USD="2"
  fi
fi

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
COST_FILE="$OUT/.cost"
echo "0.0" > "$COST_FILE"
spend() {
  # spend <usd>; returns nonzero if cap exceeded.
  local amount="$1"
  local cur new
  cur="$(cat "$COST_FILE")"
  new="$(awk -v a="$cur" -v b="$amount" 'BEGIN {printf "%.4f", a+b}')"
  echo "$new" > "$COST_FILE"
  awk -v n="$new" -v c="$CAP_USD" 'BEGIN {exit (n>c) ? 1 : 0}'
}

# Helper: invoke a Claude subagent with a prompt file via the `claude` CLI in
# print (non-interactive) mode. Uses the CLI's native --max-budget-usd flag
# instead of synthesizing per-call estimates — that's both the per-invocation
# cap AND the run-wide cap (we pass the *remaining* budget each call so a
# single huge call can't blow through what's left for later phases).
#
# CC_SUBSIDIZED=1 (set in interactive Claude Code sessions) skips cost
# accounting entirely — manual /security-review runs are effectively free.
#
# Returns nonzero if the cap is hit; the caller decides whether to continue
# with a partial pipeline or abort.
invoke_claude() {
  # invoke_claude <model> <prompt-file> <input-file> <output-file> <est-usd-fallback> [json-schema-path]
  # If <json-schema-path> is provided, the CLI's --json-schema flag is set,
  # which forces the model to emit a single JSON object matching that schema.
  # The .result of the CLI envelope is then that JSON object, serialized as a
  # string. The orchestrator unwraps as usual.
  local model="$1" prompt="$2" input="$3" output="$4" est="$5"
  local schema="${6:-}"

  if ! command -v claude >/dev/null 2>&1; then
    echo "WARNING: claude CLI not installed; skipping $(basename "$prompt") (run \`brew install claude\` or see claude.ai/code)" | tee -a "$OUT/run-log.md"
    echo "{\"verdict\":\"skipped\",\"reason\":\"claude CLI missing\"}" > "$output"
    return 0
  fi

  # Compute remaining budget for this call. CLI enforces the cap natively per
  # invocation; we still track total spend across calls so we can stop early.
  local remaining_budget=""
  if [[ "${CC_SUBSIDIZED:-0}" != "1" ]]; then
    local cur; cur="$(cat "$COST_FILE")"
    remaining_budget="$(awk -v cap="$CAP_USD" -v cur="$cur" 'BEGIN {r=cap-cur; if (r<=0) {print "0"} else {printf "%.4f", r}}')"
    if [[ "$remaining_budget" == "0" ]] || (( $(awk -v r="$remaining_budget" 'BEGIN {print (r<0.01)}') )); then
      echo "ERROR: cost cap (\$$CAP_USD) reached before invoking $model on $(basename "$prompt")." | tee -a "$OUT/run-log.md"
      echo "       partial findings preserved at $OUT/findings-raw.jsonl" | tee -a "$OUT/run-log.md"
      echo "       re-run with --cap=<higher> to continue." | tee -a "$OUT/run-log.md"
      return 1
    fi
  fi

  # Build the command. --print = non-interactive; --append-system-prompt loads
  # the playbook; --max-budget-usd caps THIS invocation; --output-format json
  # gives us cost + token usage we can attribute back to the run-log.
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
    # mid-run (we're piping stdout to a parser). If a tool isn't pre-allowed
    # the model gets a permission-denied and continues — instead of stalling
    # or wandering into a "please approve this edit" prose response.
    --permission-mode dontAsk
  )
  if [[ -n "$remaining_budget" ]]; then
    cli_args+=(--max-budget-usd "$remaining_budget")
  fi
  if [[ -n "$schema" ]] && [[ -f "$schema" ]]; then
    # --json-schema forces structured output. The CLI enforces the model
    # produces a single object conforming to the schema; the .result field of
    # the envelope contains that object serialized as a string.
    cli_args+=(--json-schema "$(cat "$schema")")
  fi

  # Capture both the model output and the cost metadata. The CLI emits a JSON
  # envelope with `result` (text) and `usage`/`cost` fields. Use mktemp +
  # $BASHPID so parallel hunters in Phase 3 don't clobber each other's raw
  # output — $$ is the parent shell's PID and is shared across the 5 subshell
  # invocations spawned in the hunt loop. (Race observed: 4 of 5 hunter
  # outputs silently lost prior to this fix.)
  local raw
  raw="$(mktemp "$OUT/.claude-raw.${BASHPID:-$$}.$(basename "$prompt").XXXXXX")"
  if claude "${cli_args[@]}" < "$input" > "$raw" 2>>"$OUT/run-log.md"; then
    # Extract the model's actual output payload. When --json-schema is set,
    # the CLI places the parsed object in `.structured_output` and leaves
    # `.result` as an empty string. Prefer structured_output when present.
    if command -v jq >/dev/null 2>&1; then
      if [[ -n "$schema" ]]; then
        jq -c '.structured_output // (.result | fromjson? // .result)' "$raw" > "$output"
      else
        jq -r '.result // .' "$raw" > "$output"
      fi
      # Track real cost if the CLI reports it.
      local actual_cost
      actual_cost="$(jq -r '.usage.cost_usd // .cost_usd // empty' "$raw" 2>/dev/null)"
      if [[ -n "$actual_cost" ]] && [[ "${CC_SUBSIDIZED:-0}" != "1" ]]; then
        spend "$actual_cost" || true   # spend already updates the file; budget enforced next call
      fi
    else
      cp "$raw" "$output"
      # Fall back to the synthetic estimate when jq unavailable.
      [[ "${CC_SUBSIDIZED:-0}" != "1" ]] && spend "$est" || true
    fi
    rm -f "$raw"
  else
    local rc=$?
    echo "WARNING: claude CLI failed (exit $rc) for $(basename "$prompt"); see run-log for stderr" | tee -a "$OUT/run-log.md"
    echo "{\"verdict\":\"error\",\"reason\":\"claude CLI exit $rc\"}" > "$output"
    rm -f "$raw"
    return 0   # don't abort the whole pipeline on one subagent failure
  fi
}

# --- Phase 1: PREP ----------------------------------------------------------
echo
echo "[1/6] prep ........................................"
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
    echo "(full scan — see security/scripts/deterministic.sh output)"
  elif [[ -n "$SCOPE_ARG" ]]; then
    git ls-files "$SCOPE_ARG" | sed 's/^/- /'
  else
    git diff --name-only "$SINCE"...HEAD | sed 's/^/- /'
  fi
} > "$OUT/scope.md"
echo "       wrote $OUT/scope.md"

# --- Phase 2: MAP -----------------------------------------------------------
echo "[2/6] map (deterministic + cartographer) .........."
det_args=()
[[ "$SCAN_FULL" == "1" ]] && det_args+=(--full)
[[ "$SINCE" != "origin/main" ]] && det_args+=(--since "$SINCE")
bash "$ROOT/security/scripts/deterministic.sh" "${det_args[@]}" > "$OUT/det-runner.log" 2>&1 || true
echo "       deterministic: see $OUT/det-runner.log + $OUT/findings-deterministic.json"

if [[ -f "$SKILL/prompts/cartographer.md" ]]; then
  # Cartographer emits free-form markdown to stdout — no --json-schema, no tool
  # use (tool calls fail under --permission-mode dontAsk). invoke_claude pipes
  # stdin → claude → .result → surface.md. We pass findings-deterministic.json
  # as stdin so the prompt can reference deterministic matches.
  invoke_claude "claude-haiku-4-5" "$SKILL/prompts/cartographer.md" \
    "$OUT/findings-deterministic.json" "$OUT/surface.md" "0.05" \
    || { echo "cap hit during map phase; aborting"; exit 2; }

  # Sanity-check the surface map. Failure modes we've seen:
  #   - invoke_claude wrote a {"verdict":"error",...} stub (CLI exit nonzero)
  #   - Haiku returned an empty/near-empty response
  #   - Haiku returned a refusal or a single sentence without structure
  # Any of these starve the hunters of context. Fall back to a minimal
  # placeholder so the pipeline doesn't crash, and log a warning.
  surface_bytes=$(wc -c < "$OUT/surface.md" | tr -d ' ')
  surface_ok=1
  if head -c 64 "$OUT/surface.md" | grep -q '"verdict":"error"'; then
    surface_ok=0
    echo "WARNING: cartographer returned an error stub (CLI failure)" | tee -a "$OUT/run-log.md"
  elif [[ "$surface_bytes" -lt 500 ]]; then
    surface_ok=0
    echo "WARNING: cartographer output is trivially short ($surface_bytes bytes); using fallback" | tee -a "$OUT/run-log.md"
  elif ! grep -qi 'entry point' "$OUT/surface.md" || ! grep -qi 'sink' "$OUT/surface.md"; then
    surface_ok=0
    echo "WARNING: cartographer output missing 'entry point' or 'sink' sections; using fallback" | tee -a "$OUT/run-log.md"
  fi
  if [[ "$surface_ok" == "0" ]]; then
    {
      echo "# Attack surface map"
      echo
      echo "> Cartographer subagent failed or returned non-trivial output; deterministic findings only."
      echo
      echo "See \`findings-deterministic.json\` for the raw scanner output."
      echo "Treat every entry point as unknown-auth; hunters should look at the diff directly."
    } > "$OUT/surface.md"
  fi
  echo "       wrote $OUT/surface.md ($surface_bytes bytes, ok=$surface_ok)"
else
  echo "       (cartographer.md not yet authored; map phase emits empty surface.md)" > "$OUT/surface.md"
fi

# --- Phase 3: HUNT ----------------------------------------------------------
echo "[3/6] hunt (parallel hunters) ....................."
HUNTERS=(cli-input secrets-redaction daemon-ipc supply-chain llm-trust)
[[ -n "$HUNTER_ARG" ]] && HUNTERS=("$HUNTER_ARG")

: > "$OUT/findings-raw.jsonl"
hunter_pids=()
for h in "${HUNTERS[@]}"; do
  prompt="$SKILL/prompts/hunter-${h}.md"
  if [[ ! -f "$prompt" ]]; then
    echo "       (skipping $h — playbook not yet authored at $prompt)"
    continue
  fi
  (
    invoke_claude "claude-sonnet-4-6" "$prompt" "$OUT/surface.md" "$OUT/hunter-${h}.jsonl" "0.05" \
      "$SKILL/schemas/hunter.json"
    # Expand the {"findings":[...]} envelope into bare JSONL lines.
    python3 -c "
import json, sys
try:
    obj = json.loads(open('$OUT/hunter-${h}.jsonl').read())
except Exception:
    sys.exit(0)
if isinstance(obj, dict) and isinstance(obj.get('findings'), list):
    with open('$OUT/hunter-${h}.jsonl', 'w') as f:
        for item in obj['findings']:
            f.write(json.dumps(item) + '\n')
"
    sanitize_jsonl "$OUT/hunter-${h}.jsonl" "hunter-${h}"
  ) &
  hunter_pids+=($!)
done
wait "${hunter_pids[@]}" || true
# Concat after wait so we don't interleave appends from racing subshells.
: > "$OUT/findings-raw.jsonl"
for h in "${HUNTERS[@]}"; do
  [[ -f "$OUT/hunter-${h}.jsonl" ]] && cat "$OUT/hunter-${h}.jsonl" >> "$OUT/findings-raw.jsonl"
done
echo "       wrote $OUT/findings-raw.jsonl ($(wc -l < "$OUT/findings-raw.jsonl" | tr -d ' ') findings before dedup)"

# --- Phase 4: DEDUP ---------------------------------------------------------
echo "[4/6] dedup (root-cause merge) ...................."
if [[ -f "$SKILL/prompts/dedup.md" ]] && [[ -s "$OUT/findings-raw.jsonl" ]]; then
  invoke_claude "claude-sonnet-4-6" "$SKILL/prompts/dedup.md" \
    "$OUT/findings-raw.jsonl" "$OUT/findings-deduped.jsonl" "0.05" \
    "$SKILL/schemas/dedup.json" \
    || { echo "cap hit during dedup; aborting"; exit 2; }
  python3 -c "
import json, sys
try:
    obj = json.loads(open('$OUT/findings-deduped.jsonl').read())
except Exception:
    sys.exit(0)
if isinstance(obj, dict) and isinstance(obj.get('findings'), list):
    with open('$OUT/findings-deduped.jsonl', 'w') as f:
        for item in obj['findings']:
            f.write(json.dumps(item) + '\n')
"
  sanitize_jsonl "$OUT/findings-deduped.jsonl" "dedup"
else
  cp "$OUT/findings-raw.jsonl" "$OUT/findings-deduped.jsonl"
fi
echo "       wrote $OUT/findings-deduped.jsonl ($(wc -l < "$OUT/findings-deduped.jsonl" | tr -d ' ') after dedup)"

# --- Phase 5: VALIDATE -----------------------------------------------------
# Per-finding loop. Sonnet for the default ~90%; Opus for the 5 hard classes.
# Synthesia's validator is "deliberately stricter than hunters" — discards ~60% of
# hunter findings as false positives.
echo "[5/6] validate (Sonnet ~90% / Opus on hard classes)"
OPUS_CLASSES="authz cryptography multi-hop-taint agent-tool-abuse exploitability-dispute"
: > "$OUT/findings-validated.jsonl"
while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  cls=$(echo "$line" | jq -r '.class // ""' 2>/dev/null || echo "")
  model="claude-sonnet-4-6"
  est="0.05"
  for opus_cls in $OPUS_CLASSES; do
    [[ "$cls" == "$opus_cls" ]] && { model="claude-opus-4-7"; est="0.30"; break; }
  done
  if [[ -f "$SKILL/prompts/validator.md" ]]; then
    echo "$line" > "$OUT/.validate-input"
    invoke_claude "$model" "$SKILL/prompts/validator.md" \
      "$OUT/.validate-input" "$OUT/.validate-output" "$est" \
      "$SKILL/schemas/validator.json" \
      || { echo "cap hit during validation; partial results saved"; break; }
    sanitize_jsonl "$OUT/.validate-output" "validator"
    cat "$OUT/.validate-output" >> "$OUT/findings-validated.jsonl"
  else
    echo "$line" >> "$OUT/findings-validated.jsonl"
  fi
done < "$OUT/findings-deduped.jsonl"
echo "       wrote $OUT/findings-validated.jsonl"

# --- Phase 6: AGGREGATE ----------------------------------------------------
echo "[6/6] aggregate (rank + emit) ....................."
export OUT
python3 - <<'PYEOF'
import json, pathlib, os, datetime
out = pathlib.Path(os.environ.get("OUT", "security/.output"))
src = out / "findings-validated.jsonl"
findings = []
malformed_count = 0
if src.exists():
    for line in src.read_text().splitlines():
        if not line.strip():
            continue
        try:
            f = json.loads(line)
        except Exception:
            # sanitize_jsonl should have already filtered these; if any survived,
            # log them and continue — aggregator MUST NOT crash on bad input.
            malformed_count += 1
            with open(out / ".malformed.jsonl", "a") as bad:
                bad.write(f"aggregate-json-decode\t{line}\n")
            continue
        if not isinstance(f, dict):
            malformed_count += 1
            with open(out / ".malformed.jsonl", "a") as bad:
                bad.write(f"aggregate-not-dict\t{json.dumps(f)}\n")
            continue
        if f.get("verdict") == "false-positive":
            continue
        findings.append(f)

sev_order = {"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4}
def _exploit(f):
    e = f.get("exploitability", 0)
    try:
        return -float(e)
    except (TypeError, ValueError):
        return 0.0
findings.sort(key=lambda f: (sev_order.get(f.get("severity", "info"), 9), _exploit(f)))
counts = {s: sum(1 for f in findings if f.get("severity") == s) for s in sev_order}

md = []
md.append("---")
md.append(f"generated: {datetime.datetime.utcnow().isoformat()}Z")
md.append(f"counts: {counts}")
if malformed_count:
    md.append(f"malformed_input_lines: {malformed_count}  # see .malformed.jsonl")
md.append("---")
md.append("")
md.append("# Findings")
md.append("")
if not findings:
    md.append("_No confirmed findings. Pipeline ran clean._")
else:
    for f in findings:
        md.append(f"## [{f.get('severity','?').upper()}] {f.get('title','(no title)')}")
        md.append("")
        md.append(f"- **class**: `{f.get('class','?')}`")
        md.append(f"- **file**: `{f.get('file','?')}`")
        md.append(f"- **verdict**: `{f.get('verdict','?')}`")
        md.append("")
        if f.get("attack"):
            md.append(f"**Attack**: {f['attack']}")
            md.append("")
        if f.get("fix"):
            md.append(f"**Fix**: {f['fix']}")
            md.append("")

(out / "FINDINGS.md").write_text("\n".join(md))

# Minimal SARIF — one run, results from validated findings.
sarif = {
    "version": "2.1.0",
    "$schema": "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json",
    "runs": [{
        "tool": {"driver": {"name": "ox-security-review", "informationUri": "https://github.com/sageox/ox"}},
        "results": [
            {
                "ruleId": f.get("class", "unknown"),
                "level": {"critical": "error", "high": "error", "medium": "warning", "low": "note", "info": "note"}.get(f.get("severity"), "warning"),
                "message": {"text": f.get("title", "")},
                "locations": [{"physicalLocation": {"artifactLocation": {"uri": (f.get("file") or "").split(":")[0]}}}],
            }
            for f in findings
        ],
    }],
}
(out / "findings.sarif").write_text(json.dumps(sarif, indent=2))
print(f"aggregate: {sum(counts.values())} findings → {out}/FINDINGS.md + findings.sarif")
print(f"counts: {counts}")
PYEOF

# --- Append to run-log -----------------------------------------------------
if [[ "${SCAN_FULL:-0}" == "1" ]]; then
  scope_line="full"
elif [[ -n "${SCOPE_ARG:-}" ]]; then
  scope_line="narrowed:$SCOPE_ARG"
else
  scope_line="diff vs $SINCE"
fi
{
  echo
  echo "## $(date -u +'%Y-%m-%dT%H:%M:%SZ') — head $(git rev-parse --short HEAD)"
  echo "- scope: $scope_line"
  echo "- cost: \$$(cat "$COST_FILE")"
  echo "- cap: \$$CAP_USD"
  echo "- output: $OUT/FINDINGS.md"
} >> "$OUT/run-log.md"

echo
echo "==== SUMMARY ===="
echo "report:  $OUT/FINDINGS.md"
echo "sarif:   $OUT/findings.sarif"
echo "cost:    \$$(cat "$COST_FILE") (cap \$$CAP_USD)"
echo "log:     $OUT/run-log.md"

echo "READY: orchestrate.sh completed 6-phase security review"
