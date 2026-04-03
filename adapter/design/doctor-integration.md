# Doctor Integration

## The Problem

ox doctor currently has 80+ checks hard-coded across 77 files. Each agent's hook-installation check lives in ox core. As we add 10+ adapters of multiple types, this pattern breaks: ox can't know ahead of time what to check for agent-specific trusted-commands lists, Perforce credential files, or GitHub token scopes.

The adapter must own its own diagnostics — the same way it owns hook installation.

---

## Design: `diagnose` Subcommand

Every adapter implements a `diagnose` subcommand that returns a structured list of issues and their fixes. ox doctor calls `diagnose` on every registered adapter and aggregates results into the existing doctor display.

```bash
ox-adapter-claude-code diagnose --repo-root /path/to/repo --scope project
```

```json
{
  "ok": false,
  "issues": [
    {
      "slug":     "hooks-not-installed",
      "severity": "error",
      "title":    "Claude Code hooks not installed",
      "detail":   "ox hook commands are missing from .claude/settings.json",
      "fix":      "ox integrate install",
      "fix_safe": true
    },
    {
      "slug":     "hooks-stale-version",
      "severity": "warning",
      "title":    "Claude Code hook commands are outdated",
      "detail":   "Hook format changed in Claude Code v2. Current hooks use the v1 format.",
      "fix":      "ox integrate install --force",
      "fix_safe": true
    },
    {
      "slug":     "session-dir-large",
      "severity": "info",
      "title":    "Claude Code session directory is large",
      "detail":   "~/.claude/projects/ is 3.2 GB. Consider pruning old sessions.",
      "fix":      null,
      "fix_safe": false
    }
  ]
}
```

### Severity levels (match existing ox doctor system)

| Level | Maps to | Meaning |
|-------|---------|---------|
| `error` | `priority: "critical"` | Breaks recording/functionality |
| `warning` | `priority: ""` (Needs Attention) | Degraded but functional |
| `info` | `priority: "optional"` | Informational, no action required |

### Issue fields

- `slug` — stable identifier used by `ox doctor --fix-slug <slug>` for automated fixing
- `severity` — `"error"` (blocks recording), `"warning"` (degrades recording), or `"info"` (informational)
- `fix` — shell command users can run to resolve the issue (or `null` if no automated fix)
- `fix_safe` — `true` if ox may run `fix` automatically without user confirmation

---

## The Full Doctor Flow

```
ox doctor
  │
  ├── 1. Core ox checks (existing, unchanged)
  │       auth, daemon, project init, ledger, git, team context...
  │
  ├── 2. For each registered adapter: call `diagnose`
  │       (one-shot subprocess per adapter, no daemon needed)
  │       collect issues → merge into check result list
  │
  ├── 3. Daemon-level adapter health (if daemon running)
  │       IPC: daemon reports crashed/stale adapter processes,
  │            last error per session, restart counts
  │
  └── 4. Display unified results
          existing priority-based output
          adapter issues appear alongside core issues
          slugs prefixed: "claude-code:hooks-not-installed"
```

### Step 2 detail: calling `diagnose`

ox doctor iterates over all adapters in the registry, calls `diagnose` on each as a one-shot subprocess. This is independent of the daemon — doctor works even if the daemon is not running.

```go
func runAdapterDiagnostics(adapters []ExternalAdapter, repoRoot string) []checkResult {
    var results []checkResult
    for _, adapter := range adapters {
        issues, err := adapter.Diagnose(repoRoot, "project")
        if err != nil {
            results = append(results, checkResult{
                name:    adapter.Name() + " adapter",
                passed:  false,
                message: "diagnose failed: " + err.Error(),
                slug:    adapter.Name() + ":diagnose-error",
            })
            continue
        }
        for _, issue := range issues {
            results = append(results, adapterIssueToCheckResult(adapter.Name(), issue))
        }
    }
    return results
}
```

### Step 3 detail: daemon-level adapter health

The daemon tracks adapter process state. Doctor queries it via IPC:

```
IPC request:  {"type": "doctor.adapter-health"}
IPC response: {
  "sessions": [
    {
      "agent_id": "OxA1b2",
      "adapter": "claude-code",
      "process_pid": 1234,
      "state": "running",
      "restart_count": 0,
      "last_error": null
    },
    {
      "agent_id": "OxB3c4",
      "adapter": "amp",
      "process_pid": null,
      "state": "degraded",
      "restart_count": 3,
      "last_error": "session file not found after 3 attempts"
    }
  ]
}
```

Degraded adapter sessions surface in doctor as `warning` issues.

---

## Fix Execution Flow

When the user runs `ox doctor --fix` or `ox doctor --fix-slug claude-code:hooks-not-installed`:

```
1. doctor collects all issues (diagnose step above)
2. for each fixable issue where fix_safe is true (or user passed --fix):
     a. show fix command (from adapter's diagnose response)
     b. if not fix_safe: prompt user [Y/n]
        (respects --yes flag for non-interactive)
     c. run the fix command (e.g., `ox integrate install`)
     d. re-run that specific diagnose check to verify fix worked
     e. report success/failure
```

The adapter provides both the issue description AND the fix command. ox doctor is a pure orchestrator — it never hard-codes what the fix does. That knowledge stays in the adapter.

### Verification after fix

After running a fix command, doctor re-runs `diagnose` and checks whether the issue's slug is still present. If it is, it reports failure and surfaces the adapter's stderr.

```go
func applyAndVerify(adapter ExternalAdapter, issue AdapterIssue, repoRoot string) error {
    // run the fix command from the diagnose response
    err := runFixCommand(issue.Fix)
    if err != nil {
        return fmt.Errorf("fix failed: %w", err)
    }

    // verify by re-running diagnose
    remaining, _ := adapter.Diagnose(repoRoot, "project")
    for _, r := range remaining {
        if r.Slug == issue.Slug {
            return fmt.Errorf("fix applied but issue persists: %s", r.Detail)
        }
    }
    return nil
}
```

---

## Slug Namespacing

Adapter issue slugs are prefixed with the adapter name to avoid collisions with core slugs and other adapters:

```
claude-code:hooks-not-installed
claude-code:hooks-stale-version
amp:trusted-commands-missing
github:token-missing
github:token-insufficient-scope
git:large-files-untracked
```

Users can target specific fixes:
```bash
ox doctor --fix-slug claude-code:hooks-not-installed
ox doctor --fix-slug amp:trusted-commands-missing --yes
```

---

## Per-Session Status: `ox agent <id> status`

Session-specific recording state lives in `ox agent <id> status`, not `doctor`. The `doctor`
surface is for system-level health diagnostics with fixable issues. Session recording state is
operational status — no fixes, just facts.

```bash
ox agent OxA1b2 status
```

Returns daemon-held state for that session:

```json
{
  "agent_id":       "r7f3a2-OxA1b2",
  "adapter":        "claude-code",
  "recording":      "active",
  "mode":           "serve",
  "current_offset": 49152,
  "last_read_at":   "2026-04-02T10:45:01Z",
  "entries_captured": 312,
  "degraded":       false,
  "started_at":     "2026-04-02T10:30:00Z"
}
```

`mode` is `serve` (normal, using long-lived adapter process) or `one_shot` (degraded fallback,
spawning on each hook call). `degraded: true` means consecutive timeouts pushed this session
to one-shot mode — recording still works, just slower.

This is purely daemon state. No adapter subprocess call needed.

---

## Adapter Types and Doctor

Different adapter types contribute different categories of issues:

| Adapter type | Example issues surfaced |
|---|---|
| `session` | hooks missing, hooks stale, agent not installed, session dir large |
| `vcs` | credentials not configured, LFS not initialized, large untracked files |
| `indexer` | token missing, token expired, index stale (>7 days), rate limit hit |
| `test` | (used internally — surfaces controllable fake issues for testing doctor itself) |

The doctor display groups adapter issues by their `category` field alongside existing core check categories. The adapter tells doctor which category its issues belong to — no category hard-coding in ox core.

---

## Testing Doctor with the Test Adapter

The test adapter can inject controllable issues into doctor's output. This lets ox's own test suite verify the full doctor flow — display, fix confirmation, fix execution, re-verification — without a real agent:

```go
// in ox's test suite
func TestDoctorFixFlow(t *testing.T) {
    // configure test adapter to report one fixable issue
    os.Setenv("OX_TEST_DIAGNOSE_ISSUES", `[{
        "slug": "hooks-not-installed",
        "severity": "error",
        "title": "Test hooks not installed",
        "fix": "ox integrate install",
        "fix_safe": true
    }]`)

    // configure test adapter to succeed on install-hooks
    os.Setenv("OX_TEST_INSTALL_HOOKS_RESULT", `{"installed": true}`)

    out := runOxDoctor(t, "--fix", "--yes")
    assert.Contains(t, out, "✓ Test hooks not installed")  // fixed
}
```

This covers the full doctor → adapter → fix → verify cycle in CI without any real agent installed.
