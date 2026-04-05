# Uninstall Package

This package provides functionality for cleanly removing SageOx from repositories and user environments.

## Components

### Repository-Level Uninstall

- `sageox_dir.go` - Handles removal of `.sageox/` directory and its contents
- `hooks.go` - Manages removal of SageOx git hooks from `.git/hooks/`

### User-Level Uninstall

- `user_integrations.go` - Handles removal of user-level SageOx integrations

## User Integration Removal

The user integrations removal system finds and removes SageOx hooks and plugins from user-level configuration, affecting only the current user's agent setup.

### Supported Integrations

The system detects and removes SageOx integrations from:

1. **Claude Code**
   - Hooks in `~/.claude/settings.json` (SessionStart, PreCompact)
   - User-level config in `~/.claude/CLAUDE.md`

2. **OpenCode**
   - Plugin at `~/.config/opencode/plugin/ox-prime.ts`

3. **Gemini CLI**
   - Hooks in `~/.gemini/settings.json`

4. **User Git Hooks**
   - SageOx hooks in `~/.config/git/hooks/` (XDG-compliant)

### Usage

```go
import "github.com/sageox/ox/internal/uninstall"

// Create finder
finder, err := uninstall.NewUserIntegrationsFinder()
if err != nil {
    return err
}

// Find all user-level integrations
items, err := finder.FindAll()
if err != nil {
    return err
}

// Remove integrations (dryRun=false for actual removal)
err = uninstall.RemoveUserIntegrations(items, false)
```

### Platform Support

The implementation follows XDG Base Directory specification and works across:
- macOS (using `~/.config/` for consistency)
- Linux (XDG-compliant paths)
- Windows (respects `XDG_CONFIG_HOME` if set)

### Hook Removal Behavior

For settings files (Claude, Gemini):
- Edits the JSON file to remove only SageOx hooks
- Preserves other hooks and user content
- Removes empty hook event sections after cleanup

For plugin files (OpenCode):
- Removes the entire plugin file/directory
- Only removes if it matches SageOx plugin signature

### Safety Features

1. **Detection**: Only removes items confirmed to contain SageOx references
2. **Dry Run**: Preview mode to show what would be removed
3. **Confirmation**: Separate confirmation required for user integrations
4. **Scope Warning**: Clearly indicates this only affects current user
5. **Granular**: Edits settings files rather than deleting them
6. **Logging**: Comprehensive slog logging for troubleshooting

### Command Integration

The `ox uninstall --user-integrations` flag enables user integration removal:

```bash
# Preview user integrations that would be removed
ox uninstall --user-integrations --dry-run

# Remove user integrations (with confirmation)
ox uninstall --user-integrations

# Non-interactive removal
ox uninstall --user-integrations --force
```

### User Config Directory

The system intentionally does NOT remove `~/.config/sageox/` by default. This directory contains:
- User preferences (tips enabled/disabled)
- Telemetry settings
- Attribution data

Users are shown a message suggesting manual removal if desired.

## Testing

All components have comprehensive test coverage:

```bash
# Run all uninstall tests
go test ./internal/uninstall/

# Run user integrations tests only
go test ./internal/uninstall/ -run "TestIsOxPrimeCommand|TestContainsOxPrime|TestFind.*|TestRemoveUserIntegrations"
```

## Design Principles

1. **Conservative**: Never delete more than necessary
2. **Transparent**: Show exactly what will be removed
3. **Reversible**: Allow dry-run previews
4. **Safe**: Multiple confirmation layers
5. **Informative**: Clear messaging about scope and impact
6. **Cross-platform**: XDG-compliant with platform awareness
