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

1. **Agent prepares release notes** — update `CHANGELOG.md`. Follow Release Notes Guidelines below. NO commit hashes. NO auto-generated changelogs.
2. **Agent asks human for version confirmation** — always ask. Default: bump middle number.
3. **Human creates draft release** at github.com/sageox/ox/releases/new (tag: `v0.X.0`)
4. **Human publishes** — automation handles binaries and signing

## Release Notes Guidelines

Release notes are not commit history, not engineering documentation, not implementation
summaries. They answer one question: **"As a user, what can I now do that I couldn't do
before?"** Assume the reader is an engineer who uses SageOx every day — they care about
capabilities, not implementation, unless the implementation changes behavior they can see.

**Target quality bar:** Conductor, Wispr Flow, Raycast, Linear, Arc Browser. Calm, concise,
confident, product-focused, implementation-light. A reader should understand an entire
release in under a minute.

**The test for every bullet:** would a customer ever describe the feature this way? If not,
rewrite it. Never expose internal architecture, implementation details, algorithms, storage,
migrations, daemon behavior, or internal IDs — those belong in the PR body, commit history,
or architecture docs, not here.

- ✅ **Every recording now gets a permanent session link from the moment it starts.**
- ❌ "Recordings now mint their stable session ID at recording start, persisted in the raw
  header carrier, reused after daemon restart."
- ✅ **`ox doctor` now catches Decision Record configuration issues before they affect your
  workflow.**
- ❌ "`ox doctor` now validates `decision.paths`."
- ✅ **Search is more resilient under heavy concurrent usage.**
- ❌ "Retries index corruption before self-heal fallback."
- ✅ **Publish a plan as a Claude Code Artifact** — a self-contained page that renders with no
  network access.
- ❌ "CSP-safe artifact render — drops the SSE loop and inlines the Mermaid CDN so the page
  passes Content-Security-Policy."

**Collapse related work into themes — ruthlessly.** Ten engineering fixes are often one
user-visible improvement. Don't write "fixed sync / fixed retries / fixed divergence / fixed
GPG / fixed daemon startup." Write **"Sync is significantly more reliable, including recovery
from interrupted sessions and diverged history."** If three bullets all improve the same
feature, make one bullet.

**Organize around product concepts, not internal subsystems.** Categories: `New`, `Improved`,
`Fixed`, `Security` (security only when relevant). Users think in workflows, not packages.

**Cut internal jargon.** No protocol/implementation acronyms or symbols: SSE, CSP, OTLP,
askpass, 401/403, HTTP status codes, struct/field names, signal names, file paths. Name the
command and any customer-facing env var (`SAGEOX_*`); translate everything else into the
effect the user sees.

**Crisp.** One or two sentences per entry, usually one. Never a paragraph bullet. If a reader
can't tell why they'd care by the end of the first line, rewrite it.

**Tone: confident and matter-of-fact, never marketing hype.** No "game changing,"
"revolutionary," or "best ever."

**Target size:** roughly 3–8 New, 2–5 Improved, 3–8 Fixed (often summarized from many
commits), Security only when relevant. More than ~20 bullets total means it hasn't been
distilled enough — go back and merge harder.

**Editing process:** read every change → group into themes → strip implementation detail →
rewrite each bullet from the user's perspective → merge aggressively → delete anything that
doesn't materially affect users → read the final release in under a minute → if it still
reads like a commit log, rewrite it again.

**Agent rules:** DO write capability-first, human-focused notes that pass the "would a
customer describe it this way" test; propose the version; confirm with human. DO NOT
auto-generate changelogs from commits, include hashes/PR numbers, narrate mechanism or
internal architecture, or create tags without approval.

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
