<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.).
     Exception: Post-Command sections that require agent-side actions (e.g.,
     displaying a notice, generating a summary) are legitimate here. -->
Stop recording and save this agent session to the project ledger.

Use when:
- Finishing a coding session and want to save the recording
- Wrapping up a feature, investigation, or bug fix
- Ending work for the day and want context preserved
- Before switching to a different task or repository

Keywords: session stop, save, finish, end, done, wrap up, stop recording, upload, ledger

## Common Issues

### Not recording
**Symptom:** `no active session` or similar error
**Solution:** No session is currently active. Run `ox agent <id> session start` first

### LFS upload failed
**Symptom:** Session saved locally but upload to ledger failed
**Solution:** Check network connectivity and retry. The session data is saved locally and can be pushed later

### Summary generation slow
**Symptom:** Command hangs during "Generating summary..."
**Solution:** Summarization runs client-side. Wait for completion or check network if it stalls

$ox agent session stop
