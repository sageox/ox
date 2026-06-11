# ADR-009: Repository Salt Security Model

**Status:** Accepted
**Date:** 2025-12-22
**Deciders:** SageOx Engineering

## Context

SageOx uses a **repository salt** to derive unique identifiers and encryption keys for each repository. This salt must be:

1. **Consistent**: All collaborators derive the same salt for the same repo
2. **Unique**: Different repos have different salts (even forks)
3. **Stable**: Salt doesn't change as repo evolves
4. **Secret-ish**: Not trivially guessable by external parties

**Use cases for repo salt:**
- Derive team-specific encryption keys for cached data
- Generate unique repo IDs for SageOx cloud registration
- Namespace offline data to prevent cross-repo leakage
- Enable secure repo merging between teams

## Decision

Implement a **hierarchical salt resolution** with user override capability.

### Salt Resolution Order

```
┌─────────────────────────────────────────────────────────────┐
│                    Salt Resolution                          │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  1. Explicit config: .sageox/config.json → repo_salt       │
│     User-specified, highest priority                        │
└────────────────────────────┬────────────────────────────────┘
                             │ not set
                             ▼
┌─────────────────────────────────────────────────────────────┐
│  2. DNS lookup: salt.sageox.<org-domain>.com TXT record    │
│     Organization-managed, enables policy control            │
└────────────────────────────┬────────────────────────────────┘
                             │ not found / timeout
                             ▼
┌─────────────────────────────────────────────────────────────┐
│  3. Initial commit hash: git rev-list --max-parents=0 HEAD │
│     Automatic, stable, shared by all clones                 │
└────────────────────────────┬────────────────────────────────┘
                             │ not available (no commits)
                             ▼
┌─────────────────────────────────────────────────────────────┐
│  4. Random generation: crypto/rand 32 bytes                 │
│     Saved to .sageox/config.json, committed                 │
└─────────────────────────────────────────────────────────────┘
```

### Salt Storage

```json
// .sageox/config.json
{
  "version": 2,
  "repo_salt": "optional-explicit-salt-here",
  "repo_salt_source": "config|dns|commit|generated"
}
```

### Salt Derivation

```go
// internal/security/salt.go
func DeriveRepoSalt(gitRoot string) ([]byte, SaltSource, error) {
    cfg := config.Load(gitRoot)

    // 1. Explicit config
    if cfg.RepoSalt != "" {
        return decode(cfg.RepoSalt), SaltSourceConfig, nil
    }

    // 2. DNS lookup (if git remote has domain)
    if domain := extractOrgDomain(gitRoot); domain != "" {
        if salt := lookupDNSSalt(domain); salt != nil {
            return salt, SaltSourceDNS, nil
        }
    }

    // 3. Initial commit
    if commit := getInitialCommit(gitRoot); commit != "" {
        // Hash the commit to get fixed-length salt
        hash := sha256.Sum256([]byte(commit))
        return hash[:], SaltSourceCommit, nil
    }

    // 4. Generate and persist
    salt := make([]byte, 32)
    crypto.Read(salt)
    cfg.RepoSalt = encode(salt)
    cfg.RepoSaltSource = "generated"
    cfg.Save()
    return salt, SaltSourceGenerated, nil
}
```

## Security Analysis

### Threat: Template Repository Attack

**Scenario:**
1. Attacker creates popular public template repo
2. Victims clone template to start new projects
3. All cloned repos share the same initial commit hash
4. Attacker knows the salt for all victim repos

**Impact:**
- Attacker could predict repo IDs
- Potential for cache poisoning if salt is used for integrity
- Cross-repo data leakage in multi-tenant scenarios

**Mitigation:**
```bash
# After cloning template, regenerate salt
ox salt regenerate

# Or specify custom salt
ox salt set "my-unique-salt-value"
```

**Detection:**
```bash
ox doctor
# Warns if initial commit matches known public templates
```

### Threat: Commit Hash Collision

**Scenario:** Two unrelated repos happen to have same initial commit hash.

**Probability:** Astronomically low (SHA-1: 2^80 collision resistance)

**Mitigation:** Not needed, but DNS/explicit salt available if paranoid.

### Threat: DNS Spoofing

**Scenario:** Attacker spoofs DNS to inject malicious salt.

**Mitigation:**
- DNSSEC validation where available
- Salt only used for derivation, not direct secrets
- DNS salt is convenience, not security-critical
- Users can override with explicit config

### Why Salt Matters for Teams

#### Repo Adoption Between Teams

When Team A hands off a repo to Team B:

```
Team A repo (salt: abc123)
    │
    ▼ transfer ownership
Team B repo (same salt: abc123)
    │
    ▼ optional: regenerate salt
Team B repo (new salt: xyz789)
```

**Same salt:**
- SageOx cloud sees continuity
- Historical data preserved
- Conventions carry over

**New salt:**
- Clean break from Team A
- New repo ID in SageOx cloud
- Fresh start for telemetry/analytics

#### Repo Merging (Monorepo Consolidation)

When merging repos into monorepo:

```
Repo A (salt: aaa)  ─┐
                     ├──► Monorepo (new salt: mmm)
Repo B (salt: bbb)  ─┘
```

- New salt prevents ID collision
- Historical data from A and B preserved in cloud
- Monorepo gets fresh identity

#### Fork Differentiation

```
Original repo (salt from commit: abc)
    │
    ├──► Fork 1 (same initial commit, same salt)
    │       └──► Should regenerate if truly independent
    │
    └──► Fork 2 (same initial commit, same salt)
            └──► Should regenerate if truly independent
```

**Guidance:** Forks that remain connected (PRs back to upstream) should keep salt. Independent forks should regenerate.

## Salt Sources Deep Dive

### 1. Explicit Config (`repo_salt` in config.json)

**Pros:**
- Full user control
- Survives repo recreation
- Can be shared out-of-band

**Cons:**
- Must be manually managed
- Could be accidentally committed with sensitive value

**Use when:**
- Migrating from another system
- Strict security requirements
- Template-based repos

### 2. DNS Lookup (`salt.sageox.<domain>.com`)

**Format:** TXT record containing base64-encoded 32 bytes

```bash
# Example DNS record
salt.sageox.acme-corp.com. 3600 IN TXT "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
```

**Pros:**
- Organization-wide policy
- No per-repo configuration
- Centrally rotatable

**Cons:**
- Requires DNS control
- Adds network dependency
- May not work in air-gapped environments

**Use when:**
- Enterprise deployment
- Centralized security policy
- Multi-repo consistency needed

### 3. Initial Commit Hash (Default)

**Derivation:**
```bash
git rev-list --max-parents=0 HEAD | head -1
# e.g., a1b2c3d4e5f6...
```

Then SHA-256 hashed to get fixed 32-byte salt.

**Pros:**
- Automatic, no configuration
- Stable across clones
- All collaborators derive same salt

**Cons:**
- Vulnerable to template attack
- Not unique for forks
- Exposed in git history

**Use when:**
- Default for most repos
- Original project (not from template)
- Low-sensitivity projects

### 4. Random Generation (Fallback)

**When triggered:**
- New repo with no commits
- Git not available
- All other methods fail

**Behavior:**
- Generate 32 random bytes
- Save to `.sageox/config.json`
- Commit to repo

**Pros:**
- Always works
- Cryptographically strong

**Cons:**
- Must be committed to share
- First user "wins"

## CLI Commands

```bash
# Show current salt info
ox salt status
# Salt source: commit (a1b2c3d4...)
# Salt hash: 7f8e9d...

# Regenerate salt (random)
ox salt regenerate
# ⚠ This will change your repo's SageOx identity
# Continue? [y/N]

# Set explicit salt
ox salt set "my-custom-salt"

# Set from DNS (forces DNS lookup)
ox salt from-dns

# Check for template vulnerability
ox doctor
# ⚠ Initial commit matches known template: create-react-app
#   Consider running 'ox salt regenerate'
```

## Implementation Notes

### Known Template Detection

Maintain list of common template initial commits:

```go
var knownTemplates = map[string]string{
    "abc123...": "create-react-app",
    "def456...": "vue-cli",
    "ghi789...": "rails-template",
    // Updated periodically
}
```

### Salt Rotation

Salt rotation is **breaking** - it changes repo identity. Require explicit confirmation:

```go
func RotateSalt() error {
    if !confirmDestructive("rotate salt") {
        return ErrAborted
    }
    // Generate new salt
    // Update config
    // Warn about cloud identity change
}
```

### Caching Salt

Salt is computed once and cached in memory per `ox` invocation. Not persisted beyond config file.

## Consequences

### Positive
- Unique repo identity across SageOx ecosystem
- Secure key derivation foundation
- User control over identity
- Template attack mitigation available

### Negative
- Complexity in salt resolution
- DNS adds network dependency
- Template repos need manual intervention
- Salt rotation is breaking change

### Risks Accepted

| Risk | Acceptance Rationale |
|------|----------------------|
| Template attack window | Mitigated by detection + easy regeneration |
| DNS spoofing | Salt is derivation input, not direct secret |
| Commit hash exposure | Public info anyway, hashed before use |
