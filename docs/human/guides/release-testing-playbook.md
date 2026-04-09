---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# Release Testing Playbook

Quick reference for all testing gates before shipping an ox release.
For the full release workflow, see `.claude/commands/release.md`.

---

## Quick Checklist

Run top-to-bottom. Items marked ⚡ can run in parallel.

```
[ ] ⚡ make lint                    (~10s, automated)
[ ] ⚡ make test-all                (~1-2min, automated)
[ ] ⚡ make test-slow               (~2-3min, automated, needs make build first)
[ ]   make test-digital-twin        (~2min, automated)
[ ] ⚡ make smoke-test              (~2-3min, automated, needs SAGEOX_CI_PASSWORD)
[ ]   make test-integration         (variable, needs claude CLI + ANTHROPIC_API_KEY)
[ ]   Run Walks                     (human-driven, Slack channel)
```

Optional (not a release gate):
```
[ ]   make test-benchmark           (~80min, needs claude CLI)
```

---

## Tier Details

### 1. Lint

| | |
|---|---|
| **What** | Static analysis, formatting, vet |
| **Command** | `make lint` |
| **Time** | ~10s |
| **Prerequisites** | `golangci-lint` installed |
| **CI** | Runs on every push and PR |

### 2. Fast Unit Tests

| | |
|---|---|
| **What** | Unit tests <500ms, race detection |
| **Command** | `make test` |
| **Time** | <30s |
| **Prerequisites** | None |
| **CI** | Push to main |

Use during development. Not a release gate on its own — `test-all` subsumes it.

### 3. Full Unit Tests

| | |
|---|---|
| **What** | All unit tests including expensive ones (git clone, SQLite concurrent, LFS) |
| **Command** | `make test-all` |
| **Time** | ~1-2min |
| **Prerequisites** | None |
| **CI** | PR gate (with race detection) |

### 4. Slow Tests

| | |
|---|---|
| **What** | Tests requiring the real ox binary (build tag: `slow`) |
| **Command** | `make build && make test-slow` |
| **Time** | ~2-3min |
| **Prerequisites** | Built ox binary in `bin/` |
| **CI** | Not in CI (requires binary) |

### 5. Preflight (combo target)

| | |
|---|---|
| **What** | lint + test-all + test-slow in sequence |
| **Command** | `make test-preflight` |
| **Time** | ~3-5min |
| **Prerequisites** | Built ox binary |

Recommended pre-PR gate. Equivalent to running tiers 1, 3, and 4.

### 6. Digital Twin Tests

| | |
|---|---|
| **What** | Generates fake ledger and team-context data for structural inspection |
| **Command** | `make test-digital-twin` |
| **Time** | ~2min |
| **Prerequisites** | None |
| **Build tags** | `ledger_twin`, `team_context_twin` |

Verifies the shape of generated data structures. Useful when changing ledger or team-context formats.

### 7. Smoke Tests

| | |
|---|---|
| **What** | E2E against real SageOx cloud (test.sageox.ai) |
| **Command** | `make smoke-test` |
| **Time** | ~2-3min |
| **Prerequisites** | `SAGEOX_CI_PASSWORD` env var |

**Covers:** auth, init, doctor, status, re-init, agent prime, session list, clone-without-ox, import video.

Uses test account `test-ox-cli@sageox.ai`. Backs up and restores your local `auth.json`.

### 8. Integration Tests (Hard Release Gate)

| | |
|---|---|
| **What** | Real Claude Code sessions, real hooks, real SIGINT signals |
| **Command** | `make test-integration` |
| **Time** | Variable |
| **Prerequisites** | `claude` CLI, `ANTHROPIC_API_KEY` |
| **Location** | Private `sageox/ox-test-harness` repo |

**Do NOT ship if integration tests fail.** These verify the full session recording and anti-entropy pipelines end-to-end.

### 9. Benchmark Tests (Optional)

| | |
|---|---|
| **What** | Prime efficiency regression detection |
| **Command** | `make test-benchmark` |
| **Time** | ~80min, ~40 API calls |
| **Prerequisites** | `claude` CLI |

Not a release gate. Run when you suspect prime performance regressions.

### 10. Run Walks (Human-Driven)

| | |
|---|---|
| **What** | Human installs the release candidate and exercises real workflows |
| **Command** | None — manual |
| **Time** | 15-30min |
| **Prerequisites** | RC binary installed locally |
| **Coordination** | Slack channel |

**This is the "does it actually feel right" gate.**

A team member installs the release candidate and walks through core workflows on a real machine. Post results and any issues to the designated Slack channel.

**Suggested walk-through:**

1. **Fresh setup** — `ox login` → `ox init` on a new repo → `ox doctor` → `ox status`
2. **Agent prime** — open Claude Code in the repo, verify `ox agent prime` loads team context
3. **Session recording** — start a session, do some work, stop it, verify `ox session list` shows it
4. **Team context** — verify team knowledge appears in the session via `ox agent team-ctx`
5. **Clone experience** — clone the repo on a different machine (or fresh directory), run `ox init`, verify smooth onboarding
6. **Doctor recovery** — intentionally break something (delete `.sageox/config.json`), run `ox doctor`, verify it auto-repairs
7. **Upgrade path** — if upgrading from a previous version, verify `ox version` shows new version and existing sessions/config survive

Report back in Slack with: pass/fail per step, OS/arch, ox version, and any rough edges noticed.

---

## CI Automation

| Workflow | Trigger | What it runs |
|----------|---------|-------------|
| `ci.yml` | Push to main | Fast tests (`-short`) |
| `ci.yml` | Pull request | Full tests with race detection |
| `release.yml` | Published release / manual dispatch | GoReleaser (build, sign, upload binaries) |
| `release-verify.yml` | *Currently disabled* | Post-release binary verification |

---

## Release Sequence

For the complete release workflow, see `.claude/commands/release.md`. The testing portion:

```
1. Parallel:       make lint  |  make test-all  |  make smoke-test
2. Sequential:     make test-slow (needs make build)
3. Sequential:     make test-digital-twin
4. Sequential:     make test-integration (hard gate)
5. Human:          Run Walks (Slack channel)
6. Proceed:        Version bump, changelog, PR, tag, publish
```

**Wall-clock time (parallel):** ~10-15min automated + 15-30min Run Walks.

---

## Environment Variables

| Variable | Required by | Where to find it |
|----------|------------|-----------------|
| `SAGEOX_CI_PASSWORD` | smoke-test | Team secrets (test account password) |
| `SAGEOX_ENDPOINT` | smoke-test (optional) | Defaults to `https://test.sageox.ai` |
| `OX_BINARY` | smoke-test (optional) | Defaults to `bin/ox` or `ox` in PATH |
| `ANTHROPIC_API_KEY` | test-integration, test-benchmark | Anthropic dashboard |
| `SAGEOX_CLI_SIGNING_KEY` | release.yml (CI only) | GitHub repo secrets |
