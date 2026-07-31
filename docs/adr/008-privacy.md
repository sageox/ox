# ADR-008: Privacy

**Status:** Accepted
**Date:** 2025-12-22
**Deciders:** SageOx Engineering

## Context

Privacy is foundational to `ox`. Users trust us with visibility into their infrastructure, development patterns, and team workflows. This trust must be earned and maintained.

**User spectrum:**
- Individual developers wanting convenience
- Privacy-conscious users who minimize data sharing
- Enterprises with strict data residency requirements
- Air-gapped environments with zero external communication

**Related ADRs:**
- [ADR-006: Offline Mode](006-offline-mode.md) - Privacy via no network
- [ADR-009: Repo Salt Security](009-repo-salt-security.md) - Identity isolation

## Decision

Adopt a **privacy-first architecture** with these principles:

1. **Local by default**: Process data locally whenever possible
2. **Explicit consent**: Cloud features require opt-in registration
3. **Minimal collection**: Only what's needed, nothing more
4. **Easy opt-out**: Single commands to disable any data sharing
5. **Transparency**: Document exactly what we collect and why

---

## Privacy Domains

### 1. Telemetry

Implement **opt-out telemetry** with transparent data collection and strict minimization.

#### Telemetry Principles

1. **Minimal collection**: Only what's needed to improve the product
2. **No PII**: Never collect names, emails, file contents, secrets
3. **Transparent**: Document exactly what's collected
4. **Easy opt-out**: Single command to disable completely
5. **Local-first**: Aggregate locally, transmit summaries

#### What We Collect

| Data | Purpose | Example |
|------|---------|---------|
| Command name | Usage patterns | `ox review` |
| Success/failure | Reliability | `exit_code: 0` |
| Duration | Performance | `1.2s` |
| ox version | Compatibility | `0.8.0` |
| OS/arch | Platform support | `darwin/arm64` |
| Agent detected | Integration usage | `claude-code` |
| Error type | Bug fixing | `config_parse_error` |

#### What We NEVER Collect

| Data | Why Excluded |
|------|--------------|
| File contents | Privacy, secrets |
| File paths | May contain usernames, project names |
| Command arguments | May contain secrets |
| Environment variables | Secrets |
| Git history | Code/IP |
| API responses | Team data |
| IP addresses | PII |
| Machine identifiers | Tracking |

#### Anonymization

```go
// internal/telemetry/event.go
type Event struct {
    Timestamp   time.Time // Rounded to hour
    Command     string    // e.g., "review"
    Duration    int       // Milliseconds
    Success     bool
    OxVersion   string
    OS          string    // e.g., "darwin"
    Arch        string    // e.g., "arm64"
    Agent       string    // e.g., "claude-code" or ""
    ErrorType   string    // Categorized, not raw message
    SessionID   string    // Random per-session, not persisted
}
```

**Session ID:** Generated fresh each `ox` invocation, not stored, not linkable across sessions.

#### Opt-Out Mechanisms

```bash
# Disable telemetry (immediate, permanent)
ox telemetry off

# Verify status
ox telemetry status

# Re-enable
ox telemetry on
```

**Storage:** `~/.config/sageox/telemetry.json`
```json
{
  "enabled": false,
  "disabled_at": "2025-12-22T00:00:00Z"
}
```

**Environment variable override:**
```bash
export SAGEOX_TELEMETRY=0  # Disable
export SAGEOX_TELEMETRY=1  # Enable (if not disabled in config)
```

**Precedence:** Config file > Environment variable > Default (enabled)

#### Enterprise Controls

For teams that prohibit telemetry:

1. **User-level disable**: Individual opt-out
2. **Project-level disable**: `.sageox/config.json`
   ```json
   {
     "telemetry": false
   }
   ```
3. **Network block**: Telemetry endpoint can be blocked at firewall
4. **Air-gap**: No network = no telemetry (obviously)

#### Data Flow

```
┌─────────────────────────────────────────────────────────────┐
│                    ox command execution                      │
└─────────────────────────────────┬───────────────────────────┘
                                  │
                                  ▼
┌─────────────────────────────────────────────────────────────┐
│                 Local Event Buffer                           │
│  - Batched (up to 10 events or 5 minutes)                   │
│  - Stored in memory only                                     │
└─────────────────────────────────┬───────────────────────────┘
                                  │
                                  ▼
                    ┌─────────────────────────┐
                    │  Telemetry enabled?     │
                    └────────────┬────────────┘
                                 │
              ┌──────────────────┴──────────────────┐
              ▼                                     ▼
      ┌──────────────┐                      ┌──────────────┐
      │     YES      │                      │      NO      │
      │  Send batch  │                      │   Discard    │
      └──────────────┘                      └──────────────┘
              │
              ▼
┌─────────────────────────────────────────────────────────────┐
│              SageOx Telemetry Endpoint                       │
│  POST /api/v1/telemetry                                     │
│  - TLS encrypted                                            │
│  - No cookies/tracking                                      │
│  - 202 Accepted (fire-and-forget)                          │
└─────────────────────────────────────────────────────────────┘
```

#### Data Retention

| Data | Retention | Justification |
|------|-----------|---------------|
| Raw events | 30 days | Debugging, recent analysis |
| Aggregated stats | 2 years | Trend analysis |
| Error patterns | 90 days | Bug fixing window |

**Deletion:** Users can request deletion via support (we have no user ID to self-serve).

#### Compliance

| Regulation | Compliance Method |
|------------|-------------------|
| GDPR | No PII, easy opt-out, deletion on request |
| CCPA | Same as GDPR |
| SOC 2 | Documented collection, access controls |

#### Transparency Report

We commit to publishing:
- What telemetry we collect (this document)
- Aggregate usage statistics (quarterly blog post)
- Any changes to telemetry (changelog)

## Consequences

### Positive
- Product improvement driven by real usage data
- Bug detection through error pattern analysis
- Feature prioritization based on actual usage
- Performance monitoring across platforms

### Negative
- Some users will disable (less data)
- Anonymization limits analysis depth
- Cannot correlate events across sessions
- Enterprise adoption friction

### Tradeoffs Accepted

| We Chose | Over | Because |
|----------|------|---------|
| Opt-out | Opt-in | Higher data quality, industry standard |
| No PII | Richer profiles | Privacy-first, GDPR compliance |
| Session-only IDs | Persistent IDs | No tracking, simpler compliance |
| Batched sends | Real-time | Lower overhead, better UX |

## Implementation Notes

### Failure Handling

```go
func sendTelemetry(events []Event) {
    // Fire-and-forget: never block user
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    resp, err := http.Post(telemetryURL, events)
    if err != nil {
        // Silently discard - telemetry is best-effort
        return
    }
    // Don't care about response beyond 2xx
}
```

### First-Run Notice

On first `ox` invocation, display:
```
ox collects anonymous usage telemetry to improve the product.
Run 'ox telemetry off' to disable. See 'ox telemetry --help' for details.
```

Display once, then store flag in config.

### Audit Logging

For `--dangerously-skip-security` flag usage:
- Always logged locally (even if telemetry disabled)
- Location: `~/.config/sageox/audit.log`
- Retention: 90 days
- Purpose: Security incident investigation

---

### 2. Local Caching

**What's cached:**
- Team guidance (conventions, patterns)
- Cloud provider version limits
- Signed prompts for offline use

**Privacy properties:**
- Cached in `.sageox/offline/` (project-local)
- User-level cache in `~/.config/sageox/cache/`
- Never contains code or file contents
- Automatically expires (see [ADR-006](006-offline-mode.md))

**User control:**
```bash
ox cache clear          # Remove all cached data
ox cache clear --user   # Remove user-level cache
```

---

### 3. Cloud Registration

**Opt-in only:** Cloud features require explicit `ox init` (without `--offline`).

**What's shared on registration:**
| Data | Purpose |
|------|---------|
| Repo ID (derived from salt) | Unique identifier |
| Git remote URL | Team association |
| ox version | Compatibility |

**What's NOT shared:**
- Code, commits, or file contents
- Developer identities
- Local file paths
- Environment variables

**Deregistration:**
```bash
ox deregister           # Remove from SageOx cloud
ox init --offline       # Re-init without cloud
```

---

### 4. Prompt Injection

When `ox` injects prompts into coding agents:

**Privacy guarantees:**
- Prompts are signed (see [ADR-005](005-signed-prompts.md))
- No user data embedded in prompts
- Prompts are generic + team conventions (not user-specific)

**What agents see:**
- Team coding conventions
- Infrastructure patterns
- Review checklists

**What agents DON'T see (from ox):**
- User identity
- Other users' activity
- Cross-repo information

---

### 5. Privacy Mode

For maximum privacy:

```bash
# Initialize without any cloud connection
ox init --offline

# Disable telemetry
ox telemetry off

# Use only bundled (signed) prompts
ox --offline review
```

**Result:**
- Zero network requests to SageOx
- All processing local
- Bundled prompts only (no personalization)
- No usage data transmitted

---

### 6. Auth Session Metadata

`ox login` sends a **device label** — `user@host`, e.g. `ryan@laptop` —
with the device-flow token exchange. It is stored against the PAT and shown
in `/settings/security` so a user can tell their laptop from their
devcontainer from their CI runner when deciding which session to revoke.

**This is deliberately outside the "What We NEVER Collect" rule above**, and
the distinction is the scope of that rule, not an exception to it:

| | Telemetry (§1) | Device label |
|---|---|---|
| Audience | SageOx, aggregated across users | The user, on their own account page |
| Purpose | Product analytics | Identifying a credential to revoke |
| Identity | Deliberately anonymous | Already authenticated — the server knows who |
| Risk of a machine id | **Correlates an anonymous user's sessions** | Low; identity is already established, so it adds no linkability |

A stable machine identifier is banned in telemetry precisely because it
de-anonymizes an otherwise anonymous stream. That hazard does not exist for
metadata attached to a credential the user is knowingly creating, stored on
their own account, and displayed back to them. Every comparable product
(GitHub, Google, 1Password, Slack) ships this, because the alternative is a
device list of indistinguishable rows and no safe way to revoke one.

**Be precise about what it contains.** The label is `user@host`, where `user`
is the **local OS account name**. So it identifies the machine *and* the
account on it — which for a single-user laptop is a person. It is not
anonymous, and this section does not claim otherwise.

What makes that acceptable here, and not in telemetry, is that the request is
already authenticated: the server knows exactly who is logging in before the
label arrives. The label therefore reveals nothing about *identity* it did not
already have; it only adds *which device*. The residual disclosure is the local
account name itself — e.g. that `r.snodgrass` rather than `ryan` is the account
on this host — which is why the opt-out exists and why the hostname is reduced
to its first DNS label.

**Opt-out**, per principle 4:

```bash
SAGEOX_NO_DEVICE_LABEL=1 ox login
```

The field is `omitempty`, so opting out makes the request byte-identical to
what ox sent before device labels existed.

**Data minimization applied:**
- Only the first DNS label of the hostname (`laptop`, not
  `laptop.corp.internal`) — the domain identifies an employer far more than
  it identifies a machine.
- The OS account name, not a git-config or profile name.
- Allowlisted to `[A-Za-z0-9._-]`, truncated to 32 (user) + 63 (host).
- Never the local machine id from `internal/signature` — that value is an
  HMAC key, and transmitting it would leak a local secret.

---

## Privacy Summary Matrix

| Feature | Data Transmitted | Opt-out Method |
|---------|------------------|----------------|
| Telemetry | Anonymous usage stats | `ox telemetry off` |
| Cloud registration | Repo ID, remote URL | `ox init --offline` |
| Guidance sync | None (download only) | `--offline` flag |
| Prompt delivery | None (download only) | `--offline` flag |
| Version check | ox version, OS | `ox config set check_updates false` |
| Device label (`ox login`) | `user@host` — local OS account + short hostname, shown only to that user | `SAGEOX_NO_DEVICE_LABEL=1` |
