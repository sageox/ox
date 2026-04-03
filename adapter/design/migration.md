# Migration Path: Built-in → External Adapters

## Principles

- No breaking changes for existing users at any phase
- Built-in adapters stay as fallback throughout
- External adapters silently supersede built-ins when present
- Each phase ships independently

## Phase 0: Protocol Foundation (no user-visible change)

- Define `internal/adapterprotocol/` package with all types and the protocol version constant
- Add `ExternalAdapter` struct implementing `Adapter` interface via binary subprocess calls
- Add `DiscoverExternalAdapters()` to scan for `ox-adapter-*` on known paths
- Add priority logic: external adapter with same name wins over built-in
- Compliance test suite skeleton
- **Result**: ox can use external adapters if they exist, but none ship yet. Existing behavior unchanged.

## Phase 1: Extract One Adapter (claude-code)

- Create `sageox/ox-adapters` repo
- Port `internal/session/adapters/claude_code.go` logic to `cmd/ox-adapter-claude-code/`
- Port `cmd/ox/hooks_claude.go` into adapter (adapter owns hook installation)
- Add compliance tests
- Ship `ox-adapter-claude-code` binary in ox release tarball
- `ox integrate install` installs the adapter binary if missing
- **Result**: Claude Code users get the external adapter. Built-in still works as fallback.

## Phase 2: Extract Remaining Official Adapters

- Port gemini, codex adapters to `sageox/ox-adapters`
- Port their hook installation code
- Add `ox adapter` subcommand (`list`, `install`, `upgrade`, `remove`)
- Publish `registry.yaml`
- **Result**: All official agents have external adapters.

## Phase 3: Thin the ox Binary

- Built-in adapters still present but marked deprecated in source
- `ox adapter list` shows deprecation notice if using built-in
- Ship `ox-full` (fat binary with built-ins) for users who can't use separate binaries
- **Result**: ox binary shrinks. `ox-full` available for simple installs.

## Phase 4: Community Ecosystem

- Document third-party adapter development (protocol spec, compliance test usage)
- Community registry section in `registry.yaml`
- `ox adapter install github.com/user/ox-adapter-myagent`
- **Result**: Anyone can publish and share adapters.

## Hook Installation Ownership

A key migration detail: hook installation code moves from ox core into adapter binaries.

**Today**: `cmd/ox/hooks_claude.go` knows how to write `.claude/settings.json`
**After**: `ox-adapter-claude-code install-hooks` does this

ox core becomes: "for each selected agent, call its adapter's `install-hooks`." Zero agent-specific code in ox core.

This also means **version-specific quirks** (e.g., Claude Code v2 changes its settings format) are handled in the adapter binary update — not an ox release.
