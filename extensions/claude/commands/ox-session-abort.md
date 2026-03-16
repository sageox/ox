<!-- Keep this file thin. Behavioral guidance (use-when, post-command, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
Abort a session, discarding all local data without uploading to the ledger.
This is destructive and cannot be undone. Use `/ox-session-stop` to save instead.

To abort the current session:
$ox agent session abort --force

To abort a specific session by name (useful for orphaned or stale sessions):
$ox agent session abort <session-name> --force

The session name can be the full name or a partial suffix (e.g., the agent ID like "OxKMZN").
Run `ox session list` to see session names and their status.
