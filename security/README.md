# `security/` — ox security review pipeline

This directory holds ox's security review pipeline: the threat model the AI hunters load, the OpenGrep rule sets, and the scripts that drive the deterministic and AI tiers. ox is a local-first CLI that holds your OAuth tokens, runs a Unix-socket daemon, and downloads adapter binaries from GitHub — the threat surface is your workstation, and the pipeline is tuned to that.

The pipeline is a port of the [Synthesia-style 6-phase AI security review](https://www.synthesia.io/post/automating-code-security-reviews-with-claude-mythos-level-capabilities), adapted for an OSS Go CLI. It combines deterministic OSS scanners with optional parallel Claude hunter/validator subagents.

**Never blocks merge.** Every tier is advisory. The contributor decides.

> Looking for **what ox redacts from your data before it leaves your machine**? That's the user-facing redaction reference in [`docs/security/`](../docs/security/). This directory is the contributor-facing review process and threat model.

## Two tiers, when to use which

| Tier | When | How | Cost | Wallclock |
|---|---|---|---|---|
| **Fast** | Every PR. Runs deterministically. No AI. | `make sec-fast` | $0 | <60s |
| **AI** | Before merging anything touching auth, daemon IPC, redaction, adapter install, or `go.mod` | `make sec` | ~$4 per 100 KB of change (cost-capped at $8) | 5–15 min |

The fast tier is what every contributor runs every commit and what CI runs on every PR. The AI tier requires `ANTHROPIC_API_KEY` (or a Claude Code subsidized session) and is opt-in. It is **not** wired into CI by default — see [Why no AI tier in CI](#why-no-ai-tier-in-ci) below.

## Quick start

```bash
make sec-install   # one-time: installs OpenGrep, govulncheck, OSV-Scanner, Syft, Grype → ./bin/
make sec-fast      # every PR — runs deterministic scanners on your diff vs origin/main
make sec           # optional, AI tier — requires ANTHROPIC_API_KEY
make sec-test      # changing the pipeline itself? its tests — fake claude + scanners, no cost
```

Output lands in `security/.output/FINDINGS.md`. In CI, the fast tier writes `det-summary.md` to the job summary and saves `security/.output/` as the run's `security-findings-<run id>` artifact; nothing is uploaded to the Security tab.

## Layout

```text
security/
├── README.md            you are here
├── SECURITY.md          threat model — the context the AI consumes; safe to read in <15 min
├── VERIFICATION.md      how to prove a closed finding stays closed
├── config.yml           single knob source (cost cap, sensitive paths, enabled hunters)
├── golangci-gosec.yml   gosec rule set for the deterministic tier (not the lint gate)
├── rules/
│   └── ox-entry-points.yml    OpenGrep custom rules — tuned to ox's chokepoints
├── scripts/
│   ├── deterministic.sh       parallel OSS scanners → SARIF
│   ├── orchestrate.sh         6-phase driver (only used by AI tier)
│   ├── pipeline.py            review input, packets, scanner status, coverage gate, report
│   ├── tests/                 pipeline tests (`make sec-test`)
│   └── install-bins.sh        scanner installer
└── .output/             GITIGNORED — runtime artifacts only
    ├── FINDINGS.md
    └── findings.sarif
```

The AI skill itself lives at `.claude/skills/security-review/` if you have Claude Code installed; the slash command `/security-review` is equivalent to `make sec`.

## Reading `security/.output/FINDINGS.md`

Read the coverage line first — the frontmatter's `coverage:` and the banner under the title. It says what the run actually reviewed:

| Coverage | Meaning | Exit |
|---|---|---|
| `full` | Every stage ran. Zero findings is a clean result. | 0 |
| `partial` | A stage was skipped, failed, truncated, or stopped at the cost cap. Findings are real; their absence is not, for the parts listed. | 0 (skips), 1 (a failure), 2 (cap) |
| `none` | A required stage did not run: no hunter completed, every scanner was skipped or failed, or a `cmd/ox/`, `internal/daemon/`, `internal/session/` or `internal/auth/` change produced no entry points. Zero findings means nothing; listed findings are still real. | 3 |

Scanner findings for the touched files, plus dependency advisories, get their own section: unvalidated tool output, minus anything an AI finding reports at the same line. They are in `findings.sarif` too, as `<tool>/<rule>`. A `## Coverage` table at the end shows each stage: chunks reviewed, each scanner's status (`ran` / `skipped` / `failed`, with the reason), entry points mapped, hunter runs completed, findings validated, and cost.

Findings are grouped by hunter section (`#hunter-cli-input`, `#hunter-secrets-redaction`, `#hunter-daemon-ipc`, `#hunter-supply-chain`, `#hunter-llm-trust`). Each finding has:

- **severity** (`critical` / `high` / `medium` / `low` / `info` — see the rubric in `SECURITY.md`)
- **verdict** (`confirmed` / `likely` / `informational` / `false-positive`)
- **location** (file:line)
- **why** (one paragraph)
- **how to verify** (link to `VERIFICATION.md` recipe for the matching hunter class)

Confirmed `critical` and `high` findings should land an issue before you merge. The pipeline never adds the issues itself — that's a deliberate human gate.

## Adding a custom rule

Edit `security/rules/ox-entry-points.yml`. The file is a standard OpenGrep rule set, scoped to ox's chokepoints. Each rule should reference the hunter section it belongs to (via `metadata.hunter`) so the orchestrator can route matches.

Maintainers with private rule sets (vendor-internal patterns, embargoed CVE detectors) can point the pipeline at an additional directory via `OX_PRIVATE_RULES_DIR=/path/to/rules`. Rules in that directory are loaded after the public set and can override severities or add patterns without touching this repo.

## Cost model

The AI tier is the only tier with a marginal cost. Spend is what each subagent call reports in `total_cost_usd` — a call the CLI stops at its budget still counts. The cap (`cost_cap_usd` in `config.yml`, or `--cap=N`) is checked before every call, and a wave of parallel hunters splits what is left evenly, so together they can't spend past it (the CLI can still let a single call finish the model turn that crossed its budget). When it is reached, the remaining AI calls are skipped, findings not yet validated are kept and marked `UNVALIDATED`, and the run exits 2 with PARTIAL COVERAGE. `CC_SUBSIDIZED=1` keeps the tally but stops enforcing the cap.

If you're running the AI tier as a routine pre-commit check on every diff, you're holding it wrong — use `make sec-fast` for that, and `make sec` only when the diff touches one of the sensitive paths (auth, daemon, redaction, adapter install, `go.mod`).

## Why no AI tier in CI

The CI workflow at `.github/workflows/security-review.yml` runs the fast deterministic tier on every PR but **does not** run the AI tier. This is a deliberate choice:

- **Cost-DoS risk.** Any contributor who can land a PR can label one as `needs-security-review`; ~$4/run × N labels drains the budget.
- **Public finding disclosure.** CI output on a public repo is public: anyone can read a run's job summary and PR annotations, and anyone signed in to GitHub can download its artifacts. A real exploitable finding from the AI tier would become a 0-day announcement before it could be patched. (The fast tier's findings come from public scanners on public code, so the job summary carries them.)
- **Prompt injection via PR content.** A diff can carry adversarial strings that hijack the hunter prompts. Locally, the branch's commit messages, branch name, `CLAUDE.md`, rules and project hooks are kept out of every subagent; the diff itself can't be ([what reaches a subagent](SECURITY.md#review-pipeline-subagents)).
- **Marginal value.** Maintainers can run `make sec` locally for free via the Claude Code subsidy.

If a maintainer or fork wants the AI tier in CI, it's a small addition: a label-gated job conditional on `secrets.ANTHROPIC_API_KEY != ''`. Open an issue if you have a use case for it.

## Toolchain

| Layer | Tool | Notes |
|---|---|---|
| SAST + entry points | OpenGrep | FOSS Semgrep fork (LGPL-2.1) |
| Go reachability | govulncheck | Lowest false-positive rate of any Go SCA |
| Multi-eco SCA | OSV-Scanner | Backed by Google's OSV.dev |
| SBOM / container | Syft + Grype | Anchore. Not Trivy (compromised twice March 2026) |
| Secrets (in-process) | built-in gitleaks-derived detector | `internal/session/gitleaks_generated.go` — used by the session redactor, not run separately here |
| AI hunters / validators | Claude (via Claude Code or `ANTHROPIC_API_KEY`) | Opt-in tier only |

## Reporting a real finding

If you found a security issue in ox that this pipeline didn't catch (or that it caught and is real and exploitable), please **don't open a public issue**. Two options, either works:

- Email `security@sageox.ai`
- Use GitHub's [private vulnerability reporting](https://github.com/sageox/ox/security/advisories/new) (the "Report a vulnerability" button on the Security tab)

Please include: reproduction steps, the version (`ox version`), your OS, and what you'd expect ox to do instead. We aim to acknowledge within 72 hours.

## License

MIT, same as ox itself. This pipeline, the threat model, and the rules are all public — fork them, adapt them, propose changes.

## Don't

- Don't run the AI tier on every commit; that's what the fast tier is for.
- Don't bypass the cost cap "just for one PR" — re-tune `config.yml` if findings legitimately need more depth.
- Don't open a public issue for a real vulnerability — use the disclosure paths above.
- Don't add private rules to `security/rules/` and expect them to stay private; use `OX_PRIVATE_RULES_DIR` outside the repo.
