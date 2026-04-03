# ADR-009: Naming Convention — `ox-adapter-*`

**Status**: Accepted
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

No `$PATH` scan. Executing arbitrary binaries from `$PATH` that happen to match `ox-adapter-*`
is an RCE vector — a malicious package could drop such a binary in `node_modules/.bin/`.
Homebrew-installed adapters are handled by symlinking into `~/.local/share/ox/adapters/` at
formula install time, or via `$OX_ADAPTER_PATH`. See ADR-006.

## Subcommand Naming

ox adapter subcommand names align with sibling tools where equivalent operations exist (e.g., `install`, `link`, `list`, `upgrade`, `remove` mirror conventions used by package managers and other ox subcommands). This makes future interoperability cheaper — shared mental models today, cheaper mechanical compatibility later — without requiring it now.

## Third-Party Adapters

Community-authored adapters follow the same convention: `ox-adapter-<agent-name>`. No registry approval required to use the prefix — ox discovers anything matching the pattern.
