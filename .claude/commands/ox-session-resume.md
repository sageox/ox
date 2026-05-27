<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
Resume a previously suspended session recording. Closes the open pause range; the suspended interval will be excluded from upload at stop time.

## Post-Command

After the command completes, check the JSON output for the `guidance`, `excluded_entries`, and `paused_duration` fields. Tell the user that recording is resumed and briefly mention how much was excluded (e.g., "Recording resumed. 12 entries over 4m 30s excluded.").

$ox agent session resume
