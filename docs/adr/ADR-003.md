# ADR-003: ProjectContext Single-Endpoint Source of Truth

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox connects to a SageOx server endpoint for authentication, ledger sync, team context, and LFS uploads. In multi-environment setups (staging, production, self-hosted), a single project must consistently use one endpoint — otherwise resource paths diverge: credentials stored under one endpoint, ledger cloned from another, team context fetched from a third.

Early bugs came from multiple code paths each resolving the endpoint independently (environment variable, local config, project config, flag). When these disagreed, resources ended up in the wrong directories, auth tokens didn't match, and doctor reported phantom "missing" repos that were actually stored under a different endpoint slug.

## Decision

### Single Source of Truth

Every project has **one** authoritative endpoint, stored in `.sageox/config.json` as `ProjectConfig.Endpoint`. All resource paths derive from this single value:

```go
ctx, _ := config.LoadProjectContext(projectRoot)
ctx.Endpoint()           // the one endpoint
ctx.TeamContextDir(id)   // derived from endpoint
ctx.DefaultLedgerPath()  // derived from endpoint
```

No other code path should independently resolve an endpoint for path construction. The canonical access patterns:

| Pattern | Status |
|---------|--------|
| `config.LoadProjectContext(root)` then `ctx.Endpoint()` | Preferred |
| `endpoint.GetForProject(root)` | OK (reads same config) |
| `localCfg.Ledger.Endpoint` | Removed |
| `tc.Endpoint` (TeamContext struct) | Removed |

### Endpoint Normalization

All common subdomain prefixes are stripped before storing, comparing, or displaying:

| Input | Normalized |
|-------|-----------|
| `api.sageox.ai` | `sageox.ai` |
| `www.test.sageox.ai` | `test.sageox.ai` |
| `app.sageox.ai` | `sageox.ai` |
| `git.test.sageox.ai` | `test.sageox.ai` |

Stripped prefixes (defined in `endpoint.go:stripPrefixes`): `api.`, `www.`, `app.`, `git.`

Normalization is applied at every boundary:
- CLI `--endpoint` flag: normalized immediately on parse
- `SAGEOX_ENDPOINT` env var: normalized in `Get()`/`GetForProject()`
- Config files: never store prefixed endpoints
- Auth store token keys: never store prefixed keys
- Comparisons: always use `NormalizeEndpoint()` or `NormalizeSlug()`
- `ox doctor --fix`: detects and repairs stored prefixed endpoints

### Slug Derivation

For filesystem paths, `NormalizeSlug()` extracts the host and removes the port, producing a path-safe string used in directory structures:

```
~/.local/share/sageox/{endpoint_slug}/ledgers/{repo_id}/
~/.local/share/sageox/{endpoint_slug}/team-contexts/{team_id}/
```

## Consequences

**Benefits**:
- Impossible to accidentally mix staging and production resources
- `ox doctor` can detect endpoint drift and repair it
- Credential lookup is deterministic — one endpoint, one token
- Directory structure is predictable and inspectable

**Tradeoffs**:
- A project cannot simultaneously interact with two endpoints. This is intentional — split-endpoint workflows create more problems than they solve.
- Normalization must be applied everywhere. Missing a single call site causes silent path mismatches. Canonical functions (`NormalizeEndpoint`, `NormalizeSlug`) and doctor checks mitigate this.
- Changing a project's endpoint requires re-cloning the ledger. Acceptable — endpoint changes are rare (typically only staging to production migration).
