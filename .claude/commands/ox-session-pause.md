<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
Suspend the current session recording. Local cache continues to receive entries, but the upload at stop time will exclude the suspended range.

## Post-Command

After the command completes, check the JSON output for the `guidance` field. Tell the user the recording is suspended and how to resume (`/ox-session-resume`).

While suspended, ox will inject a one-line reminder on each user prompt so the user can't forget.

$ox agent session pause
