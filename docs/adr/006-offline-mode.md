# ADR-006: Offline Mode Architecture

**Status:** Accepted
**Date:** 2025-12-22
**Deciders:** SageOx Engineering

## Context

`ox` must function in environments with limited or no network connectivity:
- Air-gapped enterprise networks
- Airplane/transit development
- Flaky conference WiFi
- Privacy-conscious users who block outbound requests
- CI environments with restricted egress

The challenge: provide maximum functionality offline while ensuring users understand the limitations of stale data.

## Decision

Implement a **graceful degradation** model with explicit staleness warnings.

### Connectivity Detection

```
┌─────────────────────────────────────────────────────────────┐
│                    Startup Sequence                         │
└─────────────────────────────┬───────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │  Check API      │ ← 3s timeout
                    │  connectivity   │
                    └────────┬────────┘
                             │
              ┌──────────────┴──────────────┐
              ▼                             ▼
      ┌──────────────┐              ┌──────────────┐
      │   ONLINE     │              │   OFFLINE    │
      │   Full       │              │   Cached     │
      │   features   │              │   data only  │
      └──────────────┘              └──────────────┘
```

**Detection method:** HEAD request to `{SAGEOX_API}/health` with 3s timeout

### Cache Architecture

```
.sageox/
├── offline/
│   ├── guidance.json       # Cached conventions/patterns
│   ├── cloud-versions.json # Provider version limits
│   ├── prompts/            # Signed prompt envelopes
│   │   ├── review-api.json
│   │   └── generate-tests.json
│   └── .last_sync          # Timestamp of last successful sync
└── config.json
```

### Staleness Model

| Data Type | Fresh | Stale Warning | Expired |
|-----------|-------|---------------|---------|
| Guidance | < 24h | 24h - 7d | > 7d |
| Cloud versions | < 7d | 7d - 30d | > 30d |
| Prompts | < 30d | 30d - 90d | > 6 months |

**Stale behavior:**
- Yellow warning: "Using cached data from X days ago"
- Continue with cached data

**Expired behavior:**
- Red warning: "Cached data is X days old and may be inaccurate"
- Continue but recommend `ox sync` when online

### Offline-Capable Commands

| Command | Offline Support | Limitations |
|---------|-----------------|-------------|
| `ox doctor` | Full | No update check, no API health |
| `ox agent prime` | Full | Uses cached guidance |
| `ox review` | Partial | Uses bundled/cached prompts only |
| `ox init` | Partial | `--offline` flag skips registration |
| `ox login` | None | Requires network |
| `ox sync` | None | Requires network |

### Sync Strategy

```go
// Opportunistic background sync
func MaybeSync(ctx context.Context) {
    if !IsOnline() {
        return
    }
    if time.Since(LastSync()) < 1*time.Hour {
        return // don't spam
    }
    go func() {
        SyncGuidance(ctx)
        SyncCloudVersions(ctx)
        SyncPrompts(ctx)
        UpdateLastSync()
    }()
}
```

**Sync triggers:**
- On `ox init` (if online)
- On `ox sync` (explicit)
- Opportunistically on other commands (background, non-blocking)

### Explicit Offline Mode

```bash
ox --offline review    # Never attempt network
ox init --offline      # Skip API registration
```

When `--offline` flag is set:
- Skip all network requests
- No connectivity check
- Use cached/bundled data only
- Faster startup (no network timeout)

## Consequences

### Positive
- Works in air-gapped environments
- Graceful degradation vs hard failure
- Clear user feedback about data freshness
- Privacy-friendly option

### Negative
- Stale data may cause incorrect recommendations
- Cache management complexity
- Disk space usage (~1-5MB typical)
- Users may not realize they're offline

### Mitigations

| Risk | Mitigation |
|------|------------|
| Stale cloud versions | Warn prominently, recommend sync |
| Missed security updates | Prompt expiration enforces refresh |
| Cache corruption | Verify signatures, fallback to bundled |
| Disk bloat | Automatic cleanup of data > 90 days |

## Implementation Notes

### Cache Invalidation

```go
// internal/offline/cache.go
type CacheEntry struct {
    Data      json.RawMessage
    FetchedAt time.Time
    ExpiresAt time.Time
    Signature string
}

func (c *CacheEntry) IsFresh() bool {
    return time.Now().Before(c.FetchedAt.Add(FreshDuration))
}

func (c *CacheEntry) IsExpired() bool {
    return time.Now().After(c.ExpiresAt)
}
```

### Network Detection Edge Cases

- Captive portals: Detect by checking response content
- Proxy servers: Respect `HTTP_PROXY`/`HTTPS_PROXY`
- IPv6-only: Support both address families
- DNS failures: Treat as offline
