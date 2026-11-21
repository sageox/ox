---
description: List recent sessions from the project ledger and offer to view one.
allowed-tools: Bash(ox:*)
argument-hint: [ox session list flags]
---
List recent sessions from the project ledger and offer to view one.

Use when:
- Reviewing what happened in recent sessions
- Viewing a teammate's session
- Checking session history for context

Keywords: session list, sessions, history, recent, view, ledger

## Steps

1. Run the command below to show recent sessions:

$ARGUMENTS

If no arguments are provided, run:

```
ox session list --limit 5
```

2. Present the results to the user and ask which session they'd like to view
3. If the selected session's status column shows "stub", run `ox session download <name>` first to fetch content from LFS
4. Run `ox session view <name>` to open in the user's default format
