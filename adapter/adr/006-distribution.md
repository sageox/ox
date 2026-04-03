# ADR-006: Distribution — Separate Repo, `ox adapter install`, Registry File

**Status**: Proposed
**Date**: 2026-04-02

## Context

With 10+ official adapters and third-party community adapters, users need a clear answer to:
- How do I get the adapters I need?
- How do I know which adapters exist?
- How do I install a third-party adapter?

## Decision

### Official adapters: `sageox/ox-adapters` repo + `ox adapter install`

Official adapters live in `github.com/sageox/ox-adapters`. Each release of that repo publishes per-platform binaries for all adapters as GitHub release assets.

ox ships an `ox adapter` subcommand:

```
ox adapter list                              # show installed + available (from registry.yaml)
ox adapter install claude-code              # install specific adapter
ox adapter install claude-code kiro amp     # install multiple
ox adapter install --detected               # install adapters for all detected agents
ox adapter upgrade                          # upgrade all installed adapters
ox adapter upgrade claude-code              # upgrade specific adapter
ox adapter remove gemini                    # uninstall
ox adapter which claude-code                # show binary path
```

Adapters install to `~/.local/share/ox/adapters/` — user-owned, no sudo.

### `ox integrate install` triggers adapter install

When a user runs `ox integrate install` (hook installation):
1. ox detects which coding agents are present on the machine
2. Checks which adapters are already installed
3. If gaps: "Claude Code detected but ox-adapter-claude-code is not installed. Install? [Y/n]"
4. Installs missing adapters, then installs hooks

This is the primary install path. Most users never run `ox adapter install` directly.

### Catalog: static `registry.yaml` in `sageox/ox-adapters`

No API server. The registry is a YAML file in the adapters repo:

```yaml
adapters:
  - name: claude-code
    display_name: Claude Code
    description: Reads Claude Code sessions, installs Claude Code hooks
    detect_commands: [claude]
    binary: ox-adapter-claude-code
    repo: sageox/ox-adapters
    capabilities: [session_reader, hook_installer, incremental_reader]

  - name: kiro
    display_name: Kiro
    description: Reads Kiro sessions via SQLite, installs Kiro hooks
    detect_commands: [kiro]
    binary: ox-adapter-kiro
    repo: sageox/ox-adapters
    capabilities: [session_reader, hook_installer]

  # ... 10+ adapters

community:
  - name: my-agent
    display_name: My Agent
    description: Community adapter for My Agent
    repo: github.com/user/ox-adapter-my-agent
    capabilities: [session_reader]
```

`ox adapter list` fetches this file (cached locally for 24h) and cross-references with installed binaries.

### Third-Party Adapters

Third-party adapters live in their own GitHub repos. Install by full repo URL:

```bash
ox adapter install github.com/user/ox-adapter-myagent
```

ox fetches the latest GitHub release, downloads the platform-appropriate binary, verifies it calls `info` and returns a valid `protocol_version`, installs it.

To get into the `community:` section of the official registry, submit a PR to `sageox/ox-adapters`. Requirements:
- Passes the compliance test suite
- Has a GitHub release with platform binaries
- Has a README with usage instructions

### Bundled Adapters (ship with ox)

`claude-code` and `codex` are bundled in every ox release tarball and Homebrew formula:

```
ox_darwin_arm64.tar.gz
  ox
  ox-adapter-claude-code   ← bundled
  ox-adapter-codex         ← bundled
```

These two cover the highest-volume users and are treated as first-class. All other adapters
(gemini, kiro, amp, cursor, windsurf, etc.) live in `sageox/ox-adapters` and are installed via
`ox adapter install`.

### Homebrew

```bash
brew install sageox/tap/ox
```

The Homebrew formula installs ox + bundled adapters (claude-code, codex). Others install via
`ox adapter install kiro`.

### Binary Discovery

The daemon discovers adapters in this order (first wins):
1. `$OX_ADAPTER_PATH` (local dev override, CI)
2. `~/.local/share/ox/adapters/` (user-installed via `ox adapter install` or `ox adapter link`)

**No `$PATH` scan.** Executing arbitrary binaries from `$PATH` that happen to be named `ox-adapter-*`
is an RCE vector (a malicious npm package could drop such a binary in `node_modules/.bin/`).
Discovery is restricted to explicit, user-controlled directories only.

Homebrew-installed adapters land in `/opt/homebrew/bin/`. Users who install via Homebrew will have
that directory in `$OX_ADAPTER_PATH` or the ox Homebrew formula will symlink adapters into
`~/.local/share/ox/adapters/` at install time.

### Registry Integrity

Registry is served from GitHub release artifacts on `sageox/ox-adapters`. HTTPS and GitHub's
content-addressed releases provide integrity for Phase 1. No additional signing needed until the
ecosystem is public and high-value enough to warrant a Sigstore integration (Phase 2).
