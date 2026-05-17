---
name: security-review
description: ox 6-phase AI security review pipeline. Combines deterministic OSS scanners (OpenGrep, govulncheck, OSV-Scanner, Syft+Grype, gitleaks) with parallel Claude hunter/validator subagents to find CLI input handling bugs, secret/credential redaction bypasses, daemon IPC authz holes, supply-chain risks, and LLM trust-boundary issues. Diff-scoped (vs origin/main by default). Never blocks merge. Use when asked to "security review", "/security-review", "review this for security", "audit this PR", "check for vulns", or before merging anything touching auth, lockfiles, daemon IPC, public command surfaces, or secrets/tokens/redaction code.
---

# /security-review — ox AI security pipeline

You are orchestrating a [Synthesia-style 6-phase security review](https://www.synthesia.io/post/automating-code-security-reviews-with-claude-mythos-level-capabilities) over the user's diff against `origin/main`. The pipeline shape, the dedup-before-validate ordering, and the right-size-models-per-phase principle all come from that post; the ox specifics (threat model, CLI/daemon primitives, hunter perspective frames) are local.

## Trigger phrases

- `/security-review` (no args) — review the diff vs `origin/main`. Default.
- `/security-review --scope=<path-glob>` — narrow to a specific path.
- `/security-review --hunter=<name>` — run only one hunter (debug). Valid names: `cli-input`, `secrets-redaction`, `daemon-ipc`, `supply-chain`, `llm-trust`.
- `/security-review --rerun` — re-run on the same diff, dedupe against the previous run's findings.
- `/security-review --cap=<usd>` — raise the per-run cost cap (default $2; persisted in `security/config.yml`).

## What you do

You are not the pipeline. You are the dispatcher. **You shell out to `security/scripts/orchestrate.sh`** and surface its output to the user concisely. The pipeline runs the AI subagents itself; do not try to re-implement them in this skill body.

```bash
bash security/scripts/orchestrate.sh "$@"
```

The orchestrator drives all six phases:

1. **Prep** — compute scope (diff vs origin/main, language mix, touched packages), write `security/.output/scope.md`.
2. **Map** — run `security/scripts/deterministic.sh` (parallel OSS scanners) + spawn the Cartographer subagent (Haiku) to draw the call graph from entry points (CLI commands, daemon IPC handlers) to sinks. Writes `security/.output/surface.md`.
3. **Hunt** — spawn 5 hunter subagents in parallel (Sonnet). Each has an explicit perspective frame (`cli-input` / `secrets-redaction` / `daemon-ipc` / `supply-chain` / `llm-trust`) to fight finding convergence. Writes `security/.output/findings-raw.jsonl`.
4. **Dedup** — single Sonnet pass merges hunter findings + deterministic findings by root cause. Writes `security/.output/findings-deduped.jsonl`.
5. **Validate** — one call per finding, **model split**: Sonnet for ~90%, Opus for the hard classes (`secrets-redaction-bypass`, `daemon-ipc-authz-bypass`, `supply-chain-tampering`). Stricter than hunters; traces real call paths; checks existing mitigations.
6. **Aggregate** — drop false-positives, rank by severity, emit `security/.output/FINDINGS.md` (markdown) + `security/.output/findings.sarif` (machine).

## After the orchestrator returns

Show the user:

1. The headline counts: `N critical, M high, P medium, Q low` (from FINDINGS.md frontmatter).
2. The top 3 findings (by severity then exploitability).
3. The path to the full report: `security/.output/FINDINGS.md`.
4. The cost (from the orchestrator's run-log): `$X.XX, Yth-percentile vs last 30 runs`.

Do not paste the full FINDINGS.md into the chat — it can be hundreds of lines. Summarize, link. Keep the summary under 120 words.

## Cost behavior

- On-demand runs (this skill) via Claude Code subsidized tokens are effectively $0 marginal. The cost cap still applies as a budget signal, not a billing limit.
- If `ANTHROPIC_API_KEY` is unset and `CC_SUBSIDIZED` is not set, the AI tier won't run. Surface this with: "AI tier disabled (no `ANTHROPIC_API_KEY` and not running under Claude Code). Run `make sec-fast` for the deterministic-only pass."
- If a run hits the cap mid-pipeline, the orchestrator emits a partial `FINDINGS.md` and the run-log notes which phase paused. Re-run with `--cap=5` to continue, or accept the partial report.

## Sensitive paths (auto-elevate severity, always in scope)

- `internal/auth/**`
- `internal/session/**`
- `internal/daemon/**`
- `cmd/ox/adapter.go`
- `cmd/ox/redaction.go`
- `go.mod`, `go.sum`

## Specialized agents you can hand off to

If a finding needs deeper expertise, suggest the user route through one of these (don't auto-invoke — let the user decide):

- `@pentester` — confirm exploitability, build attack chain, write reproducer.
- `@threat-modeler` — broader STRIDE/LINDDUN model when a finding reveals a systemic gap.
- `@opengrep-rule-engineer` — encode a new pattern as an OpenGrep rule under `security/rules/` so the next run catches it deterministically.
- `@security-engineer` — for the structural fix design once a finding is confirmed.

## Don't

- Don't block the user. Even on critical findings, the merge button stays green; the user decides.
- Don't re-run the pipeline phases manually. Always shell to `security/scripts/orchestrate.sh`.
- Don't paste raw deterministic-tool output into the chat. The orchestrator merges it; show the synthesis.
- Don't ask the user to install tools. If `bin/opengrep` is missing, tell them to run `make sec-install` once — the script idempotently installs everything to workspace `bin/`.
- Don't quote OWASP without a concrete reproducer. The pentester agent enforces this; you should too.
