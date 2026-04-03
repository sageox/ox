# ADR-011: Repository Structure — Separate `ox-adapters` Repo

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
- Adapter release cadence ≠ ox release cadence. A new agent adapter shouldn't require an ox release
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
  pkg/
    adapterprotocol/                    ← protocol types (public, importable by external adapters)
      types.go                          ← RawEntry, InfoResponse, request/response
      protocol.go                       ← version constant, validation
    ndjson/                             ← shared NDJSON scanner/encoder (1MB buffer, Err() checking)
  internal/
    progress/                           ← shared progress reporting (daemon IPC + adapter install)

github.com/sageox/ox-adapters           ← all official adapters
  cmd/
    ox-adapter-claude-code/
    ox-adapter-gemini/
    ox-adapter-codex/
    ox-adapter-amp/
    ox-adapter-cursor/
    ...
  internal/
    shared/                             ← shared adapter utilities (file helpers, JSONL parser)
  registry.yaml                         ← static catalog of all official adapters
  go.work                               ← Go workspace for multi-module dev
```

The `pkg/adapterprotocol` package is public (`pkg/`, not `internal/`) so external adapter authors
can import it directly: `import "github.com/sageox/ox/pkg/adapterprotocol"`. The protocol spec
is the canonical contract; the Go types are a convenience. See ADR-007.

## Third-Party Adapters

Third-party adapters live in their own repos (`github.com/user/ox-adapter-myagent`). They do not need to be in `sageox/ox-adapters`. Users install them explicitly.

A community registry (a section of `registry.yaml` or a separate `community-registry.yaml`) can list vetted third-party adapters for discoverability. PRs welcome.
