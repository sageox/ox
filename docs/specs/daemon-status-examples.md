# Daemon Status Output Examples

This document shows concrete examples of the improved `ox daemon status` UX.

## Example 1: Healthy Daemon

### Output

```
● Daemon Status: Healthy (uptime: 2h30m)

  PID:            12345
  Ledger:         /Users/ryan/.cache/sageox/context
  Pull interval:  15m
  Push interval:  15m

Sync Activity
  Last sync:      5m ago
  Syncs:          47 total, 12 in last hour
  Avg duration:   1.2s
```

**Visual notes:**
- Green `●` indicates healthy state
- Clean, scannable layout
- No error section (not present when healthy)
- All essential info visible at once

---

## Example 2: Warning State (Some Errors)

### Output

```
◐ Daemon Status: Warning (uptime: 1h15m)

  PID:            98765
  Ledger:         /Users/ryan/.cache/sageox/context
  Pull interval:  15m
  Push interval:  15m
  Pending:        12 changes

Sync Activity
  Last sync:      2m ago
  Syncs:          23 total, 8 in last hour
  Avg duration:   980ms

Errors
  Recent:         2 in last hour
  Last error:     git push failed: authentication required
  Hint:           Run: git config credential.helper store
  View logs:      ox daemon logs
```

**Visual notes:**
- Yellow `◐` half-circle indicates warning
- Pending changes highlighted in yellow
- Error section appears with actionable hints
- Suggests specific command to resolve

---

## Example 3: Critical State (Sync Stale)

### Output

```
○ Daemon Status: Critical (uptime: 6h12m)

  PID:            54321
  Ledger:         /Users/ryan/.cache/sageox/context
  Pull interval:  15m
  Push interval:  15m

Sync Activity
  Last sync:      1h ago
  Syncs:          102 total, 3 in last hour
  Avg duration:   2.1s

Errors
  Recent:         8 in last hour
  Last error:     git push failed: permission denied
  Hint:           Check SSH key: ssh -T git@github.com
  View logs:      ox daemon logs
```

**Visual notes:**
- Red `○` empty circle indicates critical
- Last sync is stale (>3x the 15m interval)
- 8 errors in last hour
- Actionable hint for SSH authentication

---

## Example 4: Verbose Mode (Detailed History)

### Command
```bash
ox daemon status --verbose
```

### Output

```
● Daemon Status: Healthy (uptime: 2h30m)

  PID:            12345
  Ledger:         /Users/ryan/.cache/sageox/context
  Pull interval:  15m
  Push interval:  15m

Sync Activity
  Last sync:      5m ago
  Syncs:          47 total, 12 in last hour
  Avg duration:   1.2s

Recent Syncs (last 10)

Time                 Type     Duration     Files
15:04:23            pull     234ms        0
15:06:45            push     1.2s         3
15:10:12            pull     156ms        0
15:19:34            push     890ms        1
15:25:01            pull     198ms        0
15:34:56            push     1.5s         5
15:40:22            pull     176ms        0
15:49:33            pull     203ms        0
15:55:44            push     1.1s         2
16:04:23            pull     189ms        0
```

**Visual notes:**
- Same health status at top
- Additional table shows recent sync history
- Easy to spot patterns (pull vs push frequency)
- File change counts help identify activity level

---

## Example 5: First Run (Never Synced)

### Output

```
● Daemon Status: Healthy (uptime: 15s)

  PID:            11223
  Ledger:         /Users/ryan/.cache/sageox/context
  Pull interval:  15m
  Push interval:  15m

Sync Activity
  Last sync:      never
```

**Visual notes:**
- Still shows healthy (just started)
- Gracefully handles empty sync history
- No stats section (would show 0 syncs)

---

## Example 6: JSON Output (Unchanged)

### Command
```bash
ox daemon status --json
```

### Output
```json
{"running": true, "pid": 12345, "ledger_path": "/Users/ryan/.cache/sageox/context", "last_sync": "2026-01-10T15:04:23Z"}
```

**Notes:**
- JSON format unchanged for backward compatibility
- Scriptable output for automation
- Contains essential fields only

---

## Example 7: Daemon Not Running

### Output

```
Daemon is not running
```

**Notes:**
- Simple, clear message
- No confusing status output
- Suggests running `ox daemon start`

---

## Color Reference

The actual terminal output uses ANSI colors:

| Element | Color | Code | Usage |
|---------|-------|------|-------|
| Healthy status | Green | `\033[32m` | Positive indicators |
| Warning status | Yellow | `\033[33m` | Attention needed |
| Critical status | Red | `\033[31m` | Immediate action required |
| Labels | Gray | `\033[38;5;8m` | Secondary text |
| Values | Default | - | Primary information |
| Hints | Gray | `\033[38;5;8m` | Helpful suggestions |

---

## Progressive Disclosure Examples

### Level 1: Quick Check (default)
```bash
ox daemon status
```
Shows: health, basic config, recent activity

### Level 2: Detailed History (verbose)
```bash
ox daemon status -v
```
Shows: everything from Level 1 + sync history table

### Level 3: Raw Data (JSON)
```bash
ox daemon status --json
```
Shows: machine-readable structured data

---

## Error Hint Examples

### Authentication Error
```
Last error:     git push failed: authentication required
Hint:           Run: git config credential.helper store
```

### Permission Error
```
Last error:     permission denied (publickey)
Hint:           Check SSH key: ssh -T git@github.com
```

### Push Rejected
```
Last error:     git push failed: rejected
Hint:           Pull may be needed. Run: ox daemon sync
```

### Generic Error
```
Last error:     unknown error occurred
View logs:      ox daemon logs
```

All errors show the logs command for deeper investigation.

---

## Comparison: Before vs After

### Before
```
Daemon Status
─────────────
  Status:       Running
  PID:          12345
  Uptime:       2h30m15s
  Ledger:       /Users/ryan/.cache/sageox/context

Sync Configuration
──────────────────
  Interval (read):  15m0s
  Interval (write): 15m0s

Sync Activity
─────────────
  Last sync:    5m23s ago
  Total syncs:  47
  Last hour:    12 syncs
  Avg duration: 1.234s

Errors
──────
  Recent:     2 in last hour
  Last error: git push failed: authentication required
```

**Issues:**
- No health indicator
- Excessive decoration (box drawing)
- Millisecond precision unnecessary
- No actionable hints
- Equal visual weight to all sections

### After
```
● Daemon Status: Healthy (uptime: 2h30m)

  PID:            12345
  Ledger:         /Users/ryan/.cache/sageox/context
  Pull interval:  15m
  Push interval:  15m

Sync Activity
  Last sync:      5m ago
  Syncs:          47 total, 12 in last hour
  Avg duration:   1.2s
```

**Improvements:**
- Leading health indicator (●)
- Removed unnecessary decorations
- Sensible precision (no milliseconds)
- Better grouping and spacing
- Actionable hints in error section
- Errors only shown when present

---

## Design Goals Achieved

1. **At-a-glance health assessment** - Symbol and color convey state instantly
2. **Scan-friendly layout** - Find info in <3 seconds
3. **Actionable errors** - Don't just report, suggest fixes
4. **Progressive disclosure** - Basic → detailed on demand
5. **Data-dense but readable** - Maximum info, minimum clutter
6. **Tufte-inspired** - High data-to-ink ratio, no chart junk
