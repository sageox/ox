<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
List recent sessions from the project ledger and offer to view one.

## When to Use
- Reviewing what happened in recent sessions
- Viewing a teammate's session
- Checking session history for context

## Steps

1. Run the command below to show recent sessions:

$ARGUMENTS

If no arguments are provided, run:

```
ox session list --limit 5
```

2. Present the results to the user and ask which session they'd like to view
3. If the selected session's status column shows "stub", run `ox session download <name>` first to fetch content from LFS
4. Run `ox session view <name>` to open in the user's default format (configurable via `ox config set view_format html|text|json`)
