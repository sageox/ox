---
paths:
  - "internal/endpoint/**"
  - "internal/config/**"
  - "cmd/ox/doctor*.go"
  - "cmd/ox/init*.go"
  - "cmd/ox/login*.go"
---

# Endpoint Subdomain Normalization (MANDATORY)

All common subdomain prefixes (`www.`, `api.`, `app.`, `git.`) MUST be stripped from endpoints before storing, comparing, or displaying.

Examples: `www.test.sageox.ai` → `test.sageox.ai`, `api.sageox.ai` → `sageox.ai`

Stripped prefixes (defined in `endpoint.go:stripPrefixes`): `api.`, `www.`, `app.`, `git.`

| Context | Rule |
|---------|------|
| Config files (`.sageox/config.json`) | NEVER store prefixed endpoints |
| Auth store (`auth.json` token keys) | NEVER store prefixed endpoint keys |
| Marker files (`.repo_*`) | NEVER store prefixed endpoints |
| CLI `--endpoint` flag | Normalize immediately on parse |
| `SAGEOX_ENDPOINT` env var | Normalize in `Get()`/`GetForProject()` |
| Endpoint comparisons | Use `NormalizeEndpoint()` or `NormalizeSlug()` |
| Display/output | Show normalized form |
| `ox doctor --fix` | Detect and repair any stored prefixed endpoints |

```go
// WRONG: Raw endpoint comparison
if cfg.Endpoint == currentEndpoint { ... }

// RIGHT: Normalize before comparing
if endpoint.NormalizeEndpoint(cfg.Endpoint) == endpoint.NormalizeEndpoint(currentEndpoint) { ... }

// WRONG: Store endpoint from flag/env as-is
store.Tokens[rawEndpoint] = token

// RIGHT: Normalize before storing
store.Tokens[endpoint.NormalizeEndpoint(rawEndpoint)] = token
```

Canonical functions in `internal/endpoint/endpoint.go`:
- `endpoint.NormalizeEndpoint()` — strips prefixes from full URLs (preserves scheme/path/port)
- `endpoint.NormalizeSlug()` — calls `NormalizeEndpoint()`, then extracts host and removes port for filesystem-safe path slugs

## Single Endpoint Source of Truth

A project has ONE endpoint from `ProjectConfig.Endpoint` only.

| Pattern | Status |
|---------|--------|
| `config.LoadProjectContext(root)` then `ctx.Endpoint()` | Preferred |
| `endpoint.GetForProject(root)` | OK |
| `localCfg.Ledger.Endpoint` | REMOVED — Use ProjectContext |
| `tc.Endpoint` (TeamContext) | REMOVED — Use ProjectContext |

```go
// PREFERRED: Use ProjectContext for consistent endpoint
ctx, _ := config.LoadProjectContext(projectRoot)
teamDir := ctx.TeamContextDir(teamID)
ledgerPath := ctx.DefaultLedgerPath()

// ALSO OK: When you just need the endpoint
ep := endpoint.GetForProject(projectRoot)
```
