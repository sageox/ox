# ADR-013: Distribution — Separate Repo, `ox adapter install`, Registry File

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
ox adapter install claude-code gemini amp   # install multiple
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

  - name: amp
    display_name: Amp
    description: Reads Amp sessions, installs Amp hooks
    detect_commands: [amp]
    binary: ox-adapter-amp
    repo: sageox/ox-adapters
    capabilities: [session_reader, hook_installer, incremental_reader]

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

**Governance**: No additional compliance policy beyond these requirements. A badge system or stricter
certification tier will be added when the community adapter ecosystem actually exists and warrants
it. Governing a community that doesn't exist yet adds friction with no benefit.

### Bundled Adapters (ship with ox)

`claude-code`, `gemini`, and `codex` are bundled in every ox release tarball and Homebrew formula:

```
ox_darwin_arm64.tar.gz
  ox
  ox-adapter-claude-code   ← bundled
  ox-adapter-gemini        ← bundled
  ox-adapter-codex         ← bundled
```

These three cover the highest-volume users and are treated as first-class. All other adapters
(amp, cursor, windsurf, etc.) live in `sageox/ox-adapters` and are installed via
`ox adapter install`.

### Homebrew

```bash
brew install sageox/tap/ox
```

The Homebrew formula installs ox + bundled adapters (claude-code, gemini, codex). Others install via
`ox adapter install <name>`.

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

### Adapter Scaffold / Template

No scaffold tooling or `ox adapter new` command for Phase 1. When there are external adapter
authors, the right approach is a GitHub template repo (`sageox/ox-adapter-template`): clone, rename,
pass the compliance test suite. Until then, the protocol spec and the adapter author guide are the
documentation.

## Phase 2 Considerations

Items deferred from initial design reviews, to revisit when the adapter ecosystem has external authors:

- **Registry signing (Sigstore/cosign)**: GitHub release integrity is sufficient while all adapters are first-party. When third-party adapters are common and the registry is high-value, sign `registry.yaml` with Sigstore cosign (keyless, GitHub Actions OIDC). Verify signature before trusting any URL or checksum.
- **Community adapter index**: A curated index file in `sageox/ox-adapters` listing community adapters by name, repo URL, and last-verified-compliance date. Community authors submit PRs to get listed. Low ops overhead, no infrastructure.
- **License and trademark clarity**: `CONTRIBUTING.md` in `sageox/ox-adapters` clarifying expected licenses (MIT, Apache 2.0, or commercial) and whether the `ox-adapter-*` naming convention implies any trademark grant.
- **Release pipeline specification**: Document what triggers adapter releases, minimum protocol version policy, breaking-change freeze windows before ox major releases, and whether `registry.yaml` updates are automated or manual.

## See also

**ADR-022 (Adapter Security Posture)** governs the trust model for the install
flow described here. It refines the "GitHub release integrity is sufficient while
all adapters are first-party" assumption above: the curated short-name path (where
SageOx is the trust anchor) requires a pinned `tag` + per-platform `sha256` in
`registry.yaml`, verified before exec, because a compromised maintainer release can
otherwise install a substituted binary under a trusted name. The arbitrary-repo
path (user as trust anchor) stays frictionless. See ADR-022 for the full posture.
