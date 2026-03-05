<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.).
     Exception: Post-Command sections that require agent-side actions (e.g.,
     displaying a notice, generating a summary) are legitimate here. -->
Start recording this agent session to the project ledger.

Use when:
- Beginning a new coding session you want captured
- Starting work on a feature, bug fix, or investigation worth recording
- You want decisions and context preserved for teammates
- Resuming work after a break and want to record the new session

Keywords: session start, record, capture, begin, log, track, ledger, start recording

## Common Issues

### Already recording
**Symptom:** `session already active` or similar error
**Solution:** A session is already in progress. Run `ox agent <id> session stop` first if you need to restart

### No agent detected
**Symptom:** Agent ID is missing or unrecognized
**Solution:** Run `ox agent prime` first to register this agent instance

### Not initialized
**Symptom:** No SageOx configuration found
**Solution:** Run `ox init` to initialize SageOx in this repository

## Post-Command: Show Notice (REQUIRED)

After the command completes, check the JSON output for a `notice` field.
If present, you MUST display the notice text to the user verbatim. This is a one-time
transparency notice about session recording that the user needs to see.

$ox agent session start
