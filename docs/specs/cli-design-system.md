# CLI Design System

Unified design language for ox terminal output.

## Output Streams

`cli.PrintError` and `cli.PrintWarning` write diagnostics to stderr in both text
and JSON modes. JSON diagnostics keep the `status` and `message` fields. This
keeps stdout available for the command result, so a warning cannot turn a JSON
document into multiple concatenated objects.

`cli.PrintJSON` writes the command result to stdout, syntax-colored when a
human is reading it at a terminal and byte-exact plain JSON whenever the output
is piped, redirected, or captured by an AI coworker. It is the only JSON
printer; see `.claude/rules/json-output.md`. Writer-aware helpers such as
`PrintWarningTo` honor their supplied writer. Command-specific JSON response
envelopes remain part of the command's stdout contract.

## Spinner Cancellation

`cli.WithSpinner` returns `tea.ErrInterrupted` when dismissed before the operation
finishes. Callers must preserve that error instead of retrying the operation or
downgrading it to a warning. The command runner prints `Interrupted.` to stderr
and exits with status 130, without attempting command correction.

Cancellation stops waiting for a result; it does not roll back work or cancel a
job already submitted to the daemon. Return results through the spinner callback
instead of mutating caller-owned variables that could be read after interruption.

## Prompts and Automation

`--no-input` disables prompts, spinners, and terminal editors. Required choices
must come from arguments or flags; missing choices fail with guidance. The shared
`cli.ConfirmYesNo` helper declines optional confirmations unless `--yes` was
supplied. Commands retain their existing unattended behavior and defaults, such
as sending an invitation when its team and recipients are provided.

Where supported, `--yes` authorizes yes/no confirmations. Commands such as
`ox uninstall` and `ox coworker remove` still require their own `--force` flag.
`--yes` does not choose an endpoint or grant first-use endpoint trust. `ox session redact`
requires interactive decisions; use `ox session audit` for an unattended scan.

`--no-interactive` only disables spinners and terminal UI; numbered and text
prompts still accept piped answers. Preserve that behavior for existing scripts.
`--no-input` also leaves explicit stdin data and protocol input available, such
as session imports and Git credential requests.

```sh
ox logout --all --yes --no-input
ox uninstall --local-only --force --no-input
ox config --no-input
```

## Argument Validation

Flag-only commands that change state (`init`, `login`, `logout`, `uninstall`,
`sync`, `upgrade`, and `doctor`) use `cobra.NoArgs` to reject unused positional
arguments before their handlers run. A stray path must not silently select the
current repository, and `--force false` must not proceed with force enabled.
Use `--force=false` to explicitly disable a boolean flag.

## Color Palette

Colors are sourced from `sageox-design` and generated into `internal/theme/generated.go`.

### Brand Colors

| Token | Dark Mode | Light Mode | Usage |
|-------|-----------|------------|-------|
| Primary | `#7AAA77` | `#3D643B` | Brand identity, headers, spinners |
| Secondary | `#ADCFA3` | `#324F31` | Commands, interactive elements; strongest sage emphasis |
| Accent | `#99C693` | `#4E7D4C` | File paths, callouts, highlights |

### Semantic Colors

| Token | Dark Mode | Light Mode | Usage |
|-------|-----------|------------|-------|
| Success | `#7AAA77` | `#3D643B` | Passed checks, confirmations |
| Warning | `#D9B654` | `#A5842F` | Cautions, dirty builds |
| Error | `#D77E6C` | `#9F4838` | Failures, critical issues |
| Info | `#7FA7C8` | `#5580A0` | Informational, flags, links |
| Dim | `#8F99A3` | `#6B7580` | Secondary text, descriptions |

### Adaptive Colors

All colors use `lipgloss.AdaptiveColor` for automatic light/dark terminal detection.

```go
ColorPrimary = compat.AdaptiveColor{
    Light: lipgloss.Color("#3D643B"),
    Dark:  lipgloss.Color("#7AAA77"),
}
```

## Semantic Styles

Defined in `internal/cli/styles.go` (primary) and `internal/ui/styles.go` (UI-specific styles). Use these instead of raw colors.

### Text Styles

| Style | Color | Weight | Usage |
|-------|-------|--------|-------|
| `StyleBrand` | Primary | Bold | App name, major headers |
| `StyleSecondary` | Secondary | Normal | Commands, actions |
| `StyleAccent` | Accent | Normal | File paths, highlights |
| `StyleDim` | Dim | Normal | Descriptions, secondary text |
| `StyleBold` | Inherit | Bold | Emphasis within text |

### Semantic Styles

| Style | Color | Usage |
|-------|-------|-------|
| `StyleSuccess` | Success | Passed checks, confirmations |
| `StyleWarning` | Warning | Cautions, dirty builds |
| `StyleError` | Error | Failures, blocked items |
| `StyleInfo` | Info | Flags, informational elements |

### Component Styles

| Style | Color | Weight | Usage |
|-------|-------|--------|-------|
| `StyleCommand` | Secondary | Normal | Command names in help |
| `StyleFlag` | Info | Normal | CLI flags |
| `StyleGroupHeader` | Primary | Bold | Section headers |
| `StyleFile` | Accent | Normal | File/directory paths |
| `StyleCallout` | Accent | Normal | Stars, step indicators |
| `StyleCalloutBold` | Secondary | Bold | Highlighted command names |

## Contextual Highlighting

Guide users toward logical next actions based on current state.

### Philosophy

- Help output adapts to context, not static
- Highlight the most useful next action
- Reduce cognitive load by surfacing what matters now
- Works for both human users AND AI agents

### Highlight Components

| Component | Style | Example |
|-----------|-------|---------|
| Star prefix | `StyleCallout` | `★ ` |
| Command name | `StyleCalloutBold` | `doctor` |
| Description | `StyleCallout` | `Run diagnostics...` |
| Step indicator | `StyleCallout` | `(Step 1)` |

### State-Based Rules

```
State: No .sageox/ directory
→ Highlight: init (Step 1)

State: .sageox/ exists, not authenticated
→ Highlight: login (Step 2)
→ Highlight: doctor (star only, always useful)

State: Fully initialized and authenticated
→ Highlight: doctor (star only)
```

### Implementation

```go
// cmd/ox/root.go
func getContextualHighlight(cmdName string) *commandHighlight {
    gitRoot := findGitRoot()
    hasSageox := dirExists(filepath.Join(gitRoot, ".sageox"))
    isLoggedIn, _ := auth.IsAuthenticated()

    switch cmdName {
    case "init":
        if !hasSageox {
            return &commandHighlight{hasStar: true, step: 1}
        }
    case "login":
        if hasSageox && !isLoggedIn {
            return &commandHighlight{hasStar: true, step: 2}
        }
    case "doctor":
        if hasSageox {
            return &commandHighlight{hasStar: true, step: 0}
        }
    }
    return nil
}
```

## Flag Scoping

Only show flags in help where they apply.

### Global Flags (shown on all commands)

| Flag | Description |
|------|-------------|
| `--verbose`, `-v` | Enable verbose output |
| `--quiet`, `-q` | Suppress non-error output |
| `--json` | Output in JSON format |
| `--config`, `-c` | Config file path |
| `--no-input` | Disable prompts and terminal UI; require explicit input |
| `--no-interactive` | Disable spinners and terminal UI; allow piped answers |
| `--yes` | Authorize yes/no confirmations |

### Command-Specific Flags

| Flag | Scope | Description |
|------|-------|-------------|
| `--review` | `agent` subcommands | Security audit mode |
| `--text` | `agent` subcommands | Human-readable output |
| `--fix` | `doctor` | Auto-fix issues |
| `--short` | `review` | Condensed output |

### Principle

Avoid polluting global help with command-specific options. If a flag only applies to one command group, scope it there.

## Version Output

Semantic colors for version information:

```
ox v0.6.0              ← StyleSuccess (or StyleWarning if dirty)
   (dirty)             ← StyleWarning (shown separately if dirty)
Built: 2026-01-16      ← StyleDim
Commit: aa06735        ← StyleInfo
Go: go1.25.5           ← StyleDim
Platform: darwin/arm64 ← StyleDim
```

## Help Output Structure

```
SageOx (ox) - Shared team context for AI coworkers            ← StyleBrand + StyleDim

Usage
─────
  ox [command]                                                ← StyleCommand

Software Development                                          ← StyleGroupHeader
────────────────────                                          ← StyleDim
★ init             Initialize SageOx... (Step 1)             ← Contextual highlight
  team             Manage team settings...                    ← StyleCommand + StyleDim

Diagnostics
───────────
  daemon           Manage background sync daemon              ← Normal
★ doctor           Run diagnostics...                         ← Highlighted (no step)
  version          Print version information                  ← Normal

Flags
─────
  -c, --config     config file path...                        ← StyleFlag + StyleDim

Run ox [command] --help for more information.                 ← StyleDim + StyleCommand
```

## Agent UX Parallel

The same progressive disclosure pattern applies to agent guidance:

1. `ox agent prime` returns suggested next actions based on repo state
2. Guidance paths are ordered by relevance to detected project context
3. Both humans and agents benefit from state-aware recommendations

## Adding New Styles

1. Add color constant to `internal/theme/generated.go` (or regenerate from sageox-design)
2. Create style in `internal/cli/styles.go`
3. Document in this file
4. Use semantic style, never raw colors in commands
