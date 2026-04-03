# ADR-004: Repository Structure — Separate `ox-adapters` Repo

**Status**: Proposed
**Date**: 2026-04-02

## Context

With 10+ official adapters anticipated, plus third-party community adapters, we need a clear repository strategy.

Options:
- **A**: All adapters in `github.com/sageox/ox` monorepo under `cmd/`
- **B**: Separate `github.com/sageox/ox-adapters` repo
- **C**: Each adapter in its own repo

## Decision

**Option B: Separate `github.com/sageox/ox-adapters` repo.**

## Reasoning

**Against Option A (in ox monorepo)**:
- 10+ adapters would clutter the ox repo with per-agent quirks
- Adapter release cadence ≠ ox release cadence. Adapter for Kiro v2 shouldn't require an ox release
- Contributors to adapter code need ox repo access — creates friction for community
- `cmd/` with 10+ `ox-adapter-*` directories buries the main binary

**Against Option C (each adapter own repo)**:
- Shared protocol types have to be a third module both sides import
- 10+ repos to maintain CI/CD for
- Community adapters naturally live in their own repos anyway — official adapters should be consolidated

**For Option B**:
- Adapters release independently of ox
- One place for official adapter contributions
- Shared protocol types in one place within the adapters repo
- `github.com/sageox/ox` stays focused on core

## Structure

```
github.com/sageox/ox                    ← core binary + daemon
  internal/
    adapterprotocol/                    ← protocol types (shared via go module)
      types.go                          ← RawEntry, InfoResponse, request/response
      protocol.go                       ← version constant, validation

github.com/sageox/ox-adapters           ← all official adapters
  cmd/
    ox-adapter-claude-code/
    ox-adapter-gemini/
    ox-adapter-codex/
    ox-adapter-kiro/
    ox-adapter-amp/
    ox-adapter-cursor/
    ...
  internal/
    shared/                             ← shared adapter utilities (file helpers, JSONL parser)
  registry.yaml                         ← static catalog of all official adapters
  go.work                               ← Go workspace for multi-module dev
```

The `internal/adapterprotocol` package in ox is published as a Go module so adapter authors can import the types. Alternatively, the types are simple enough to just copy — the protocol spec is the contract, not the Go types.

## Third-Party Adapters

Third-party adapters live in their own repos (`github.com/user/ox-adapter-myagent`). They do not need to be in `sageox/ox-adapters`. Users install them explicitly.

A community registry (a section of `registry.yaml` or a separate `community-registry.yaml`) can list vetted third-party adapters for discoverability. PRs welcome.
