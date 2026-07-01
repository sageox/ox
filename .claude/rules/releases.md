---
paths:
  - "internal/version/**"
  - ".goreleaser.yml"
  - "CHANGELOG.md"
  - ".github/workflows/release*"
---

# Release Workflow

## Versioning

Beads-style: `0.<release>.0`. Middle number increments each release. Patch releases are VERY RARE. One version per day max.

**Canonical source:** `internal/version/version.go`. Must stay in sync with `CHANGELOG.md`.

```bash
make verify-version        # check consistency
make bump-version NEW_VERSION=0.10.0
```

## Release Process

1. **Agent prepares release notes** — update `CHANGELOG.md`. User experience first. Group by: Added, Changed, Fixed. NO commit hashes. NO auto-generated changelogs. See v0.7.0 for gold standard.
2. **Agent asks human for version confirmation** — always ask. Default: bump middle number.
3. **Human creates draft release** at github.com/sageox/ox/releases/new (tag: `v0.X.0`)
4. **Human publishes** — automation handles binaries and signing

## Release Notes Guidelines

**Lead with customer value — always.** Every entry opens with what a coworker can now *do*, or the pain that's gone. The benefit is the first thing read; the mechanism comes second and stays short, or is dropped. This applies to every release, not just polished ones.

**Good:** "**Publish a plan as a Claude Code Artifact** — a self-contained page that renders with no network access." Benefit first, plain language.

**Bad:** "**CSP-safe artifact render** — drops the SSE loop and inlines the Mermaid CDN so the page passes Content-Security-Policy." Mechanism first, internal jargon.

**Cut internal jargon.** No protocol/implementation acronyms or symbols in customer-facing notes: SSE, CSP, OTLP, askpass, 401/403, HTTP status codes, struct/field names, signal names, file paths. Name the command and any customer-facing env var (`SAGEOX_*`); translate everything else into the effect the user sees.

**Crisp.** One or two sentences per entry. If a reader can't tell why they'd care by the end of the first line, rewrite it.

**Agent rules:** DO write benefit-first, human-focused notes; propose the version; confirm with human. DO NOT auto-generate changelogs from commits, include hashes/PR numbers, lead with the mechanism, or create tags without approval.

**Release infra changes:** Consult `@oss-release-engineer` subagent first.

## Release Gates

ALL test tiers must pass before release:
```bash
make test-all          # all unit tests including expensive ones
make test-slow         # tests requiring real ox binary
make test-integration  # sageox/ox-test-harness repo
```

E2E tests MUST use real agent CLI instances. Never simulate agent entries or use fake JSONL.

## Friction Telemetry (MVP-Critical)

The friction telemetry system (`github.com/sageox/frictionax`, `internal/daemon/friction.go`, `cmd/ox/friction.go`) is MVP-critical. All friction telemetry tests MUST pass before release.
