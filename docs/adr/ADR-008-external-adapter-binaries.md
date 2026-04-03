# ADR-008: External Adapter Binaries

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox hardcodes all agent adapters inside the ox binary. `internal/session/adapters/` contains `ClaudeCodeAdapter`, `GeminiAdapter`, `CodexAdapter`, and `GenericJSONLAdapter` — all compiled in. Adding Amp, Cursor, Windsurf, or any future agent requires:

1. A PR to ox source
2. A new ox release
3. Users waiting for the release

With 10+ agents anticipated, this compounds: ox grows with each agent's idiosyncratic parsing logic, and the release coupling becomes a bottleneck.

## Decision

Extract per-agent logic into standalone binaries named `ox-adapter-<name>`. ox discovers them at startup, wraps them in an `ExternalAdapter` struct that implements the existing `Adapter` interface, and registers them in the adapter registry. The rest of ox is unchanged.

The process boundary IS the dependency boundary. `ox-adapter-claude-code` can pull in whatever it needs (JSONL parsing, SQLite, fsnotify) without adding those dependencies to ox. ox only needs the protocol.

## Consequences

**Positive**
- New agent support ships without an ox release
- ox binary size stops growing with agent count
- Adapter authors only need to implement a small JSON protocol — any language
- Community agents are possible (anyone can publish `ox-adapter-myagent`)
- Adapters can version independently of ox

**Negative**
- Multiple binaries to distribute (mitigated by bundling in release tarball)
- Protocol versioning adds ongoing maintenance burden
- Built-in adapters need a deprecation path
- Daemon must manage adapter process lifecycle (non-trivial)

## Alternatives Considered

**Keep built-ins, add a Go plugin interface**: Go plugins (`.so` files) are fragile — must be compiled with exact same Go version and build flags as the host. Not viable for community adapters.

**Lua/WASM scripting**: Good isolation, but high barrier for adapter authors. Considered for future — see open questions.

**Stay with built-ins**: Doesn't scale past ~5 agents without significant ox bloat and release coupling.
