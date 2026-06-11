# Daemon Status UX Improvements

## Overview

The `ox daemon status` command has been redesigned with modern TUI best practices and Tufte-inspired data visualization principles.

## Key Improvements

### 1. Health Status at a Glance

**Before:**
- No overall health indicator
- User had to scan all sections to assess daemon state

**After:**
- Leading health indicator with semantic color and symbol
- Three states: Healthy (●), Warning (◐), Critical (○)
- Uptime shown inline with health status

```
● Daemon Status: Healthy (uptime: 2h30m)
```

**Health determination logic:**
- **Critical**: 5+ errors in last hour OR sync stale (>3x expected interval)
- **Warning**: 1-4 errors OR 10+ pending changes
- **Healthy**: No issues

### 2. Semantic Color Usage

Following ANSI color conventions:
- **Green (2)**: Healthy operations
- **Yellow (3)**: Warnings requiring attention
- **Red (1)**: Critical issues
- **Gray (8)**: Muted secondary info
- **Blue (4)**: Accent for data visualization

### 3. Information Hierarchy

**Progressive disclosure pattern:**
- Essential info shown by default
- Detailed diagnostics available via `--verbose` flag
- Error section only appears when errors exist

**Default view priority:**
1. Health status (most important)
2. Core configuration (PID, ledger, intervals)
3. Recent activity (last sync, sync count)
4. Errors (if any)

### 4. Actionable Error Context

**Before:**
```
Last error: git push failed: authentication required
```

**After:**
```
Last error: git push failed: authentication required
  Hint: Run: git config credential.helper store
  View logs: ox daemon logs
```

Error hints for common issues:
- Authentication failures → credential helper setup
- Permission denied → SSH key verification
- Push failures → suggest pull/sync

### 5. Human-Friendly Time Formatting

**Durations:**
- `5s` - under 1 minute
- `2m30s` - under 1 hour
- `1h30m` - hours and minutes
- `2h` - even hours only

**Relative times:**
- `15s ago`
- `5m ago`
- `2h ago`
- `3d ago`

### 6. Verbose Mode with Sync History

```bash
ox daemon status --verbose
```

Shows detailed sync history table:
```
Recent Syncs (last 10)

Time                 Type     Duration     Files
15:04:23            pull     234ms        0
15:06:45            push     1.2s         3
15:10:12            pull     156ms        0
```

Columns:
- **Time**: HH:MM:SS format for easy scanning
- **Type**: pull, push, or full
- **Duration**: Human-readable duration
- **Files**: Number of files changed (for push operations)

### 7. Consistent Visual Language

**Label-value pairs:**
```
  PID:            12345
  Ledger:         /Users/name/.cache/sageox/context
  Pull interval:  15m
```

- Labels: 14 characters wide, muted color
- Values: Default color for emphasis
- 2-space indent for visual grouping

### 8. Tufte-Inspired Data Density

**Sparkline implementation** (future enhancement):
```go
func formatSyncSparkline(history []SyncEvent, width int) string
```

Creates compact visualization of sync frequency over time using Unicode block characters:
```
Sync activity (last hour): ▁▂▃▄▅▄▃▂▁
```

Principles applied:
- Maximize data-to-ink ratio
- Small multiples for pattern recognition
- No chart junk - pure information
- Self-documenting visualization

## Implementation Details

### File Structure

**New file:** `/Users/ryan/Documents/Code/sageox/ox/internal/daemon/status_display.go`

Contains:
- `FormatStatus()` - default view
- `FormatStatusVerbose()` - detailed view with history
- Health calculation logic
- Error hint generation
- Time/duration formatters

**Updated files:**
- `cmd/ox/daemon.go` - added `--verbose` flag
- `internal/daemon/ipc.go` - added sync history IPC message type
- `internal/daemon/sync.go` - exported `SyncEvent` type

### IPC Protocol Extension

New message type: `MsgTypeSyncHistory`

Request: `{"type": "sync_history"}`
Response: `{"success": true, "data": [{"time": "...", "type": "pull", ...}]}`

### Color Profile

Using Charm Lipgloss v2 with ANSI colors:
- Automatic terminal capability detection
- Graceful degradation for limited color terminals
- NO_COLOR environment variable respected

## Usage Examples

### Basic status check
```bash
ox daemon status
```

Output:
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

### Verbose with history
```bash
ox daemon status --verbose
# or
ox daemon status -v
```

### JSON output (for scripting)
```bash
ox daemon status --json
```

Unchanged - still returns structured JSON for programmatic use.

## Design Principles Applied

### Edward Tufte's Visual Design
1. **Show the data** - No decorative elements
2. **Maximize data-to-ink ratio** - Every character has purpose
3. **Erase non-data-ink** - Removed unnecessary borders/separators
4. **Small multiples** - Sparklines show patterns efficiently

### Modern TUI Best Practices (2025)
1. **Semantic tokens** - Colors convey meaning, not just decoration
2. **Progressive disclosure** - Basic → detailed via flags
3. **Actionable information** - Don't just report errors, suggest fixes
4. **Context efficiency** - Relevant info fits in single screen

### Developer Experience (DX)
1. **Scan-friendly** - Find info in <3 seconds
2. **No cognitive overload** - Right amount of info at each level
3. **Self-documenting** - Clear labels, obvious meanings
4. **Helpful hints** - Guide user toward resolution

## Future Enhancements

### 1. Sparkline Integration
Add to default view when sufficient history exists:
```
Sync activity (last hour): ▁▂▃▄▅▄▃▂▁ (12 syncs)
```

### 2. Duration Trend Visualization
Show if syncs are getting faster or slower:
```
Avg duration: 1.2s ↓ (improving)
```

### 3. Interactive Mode
Using Bubbletea for real-time updates:
```bash
ox daemon status --watch
```

### 4. Error Pattern Detection
Identify repeated errors and suggest deeper diagnosis:
```
⚠ Same error occurred 5 times in last hour
  Consider: ox doctor daemon
```

## Testing Scenarios

### Healthy Daemon
- Green indicator
- All syncs successful
- No errors or warnings

### Warning State
- Yellow indicator
- 1-4 errors in last hour
- OR 10+ pending changes building up

### Critical State
- Red indicator
- 5+ errors in last hour
- OR sync stale (last sync > 3x interval)

### First Run
- Never synced before
- Shows "Last sync: never"
- Graceful handling of empty history

## Accessibility

- ANSI colors compatible with most terminal themes
- Symbols (●, ◐, ○) render in most fonts
- Falls back to ASCII if Unicode unavailable
- High contrast color choices
- NO_COLOR environment variable support

## Performance

- Single IPC call for default view
- Two IPC calls for verbose (status + history)
- History limited to last 100 events
- Minimal CPU for formatting
- No blocking operations

## References

- Edward Tufte, *The Visual Display of Quantitative Information*
- Charm Lipgloss v2 documentation
- XDG Base Directory Specification
- ANSI color codes and terminal capabilities
