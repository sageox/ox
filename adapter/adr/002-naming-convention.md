# ADR-002: Naming Convention — `ox-adapter-*`

**Status**: Proposed
**Date**: 2026-04-02

## Context

External adapter binaries need a discoverable name. The established CLI convention is `<tool>-<name>` (git-lfs, kubectl-ctx, cargo-watch). Applied here that would be `ox-claude-code`.

## Decision

Use `ox-adapter-<name>`, not `ox-<name>`.

## Reasoning

Users never type the binary name directly — they type `ox adapter install claude-code`. The binary name is an implementation detail that the daemon resolves internally.

Given that, clarity beats brevity:
- `ox-claude-code` could be confused with a wrapper that *invokes* Claude Code
- `ox-gemini` could be confused with a Gemini CLI wrapper
- `ox-adapter-claude-code` is unambiguous: this is an ox adapter for Claude Code

With 10+ adapters anticipated, the longer prefix also prevents PATH namespace collisions with unrelated tools.

## Discovery

The daemon scans for binaries matching `ox-adapter-*` in:
1. `$OX_ADAPTER_PATH` (dev override, highest priority)
2. `~/.local/share/ox/adapters/` (user-installed)
3. All entries on `$PATH` (Homebrew, system installs)

## Third-Party Adapters

Community-authored adapters follow the same convention: `ox-adapter-<agent-name>`. No registry approval required to use the prefix — ox discovers anything matching the pattern.
