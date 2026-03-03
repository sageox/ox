<!-- ox-hash: placeholder ver: 0.17.0 -->
Check the status of all active session recordings in this project.

Use when:
- Checking if a recording is currently in progress
- Verifying which agents have active recordings
- Debugging session state before starting or stopping a recording
- Confirming your agent's recording is active after context compaction

Keywords: session status, check, recording, active, inflight, am I recording, is recording

## Common Issues

### Not recording
**Symptom:** `Not recording` message
**Solution:** No sessions are active. Run `/ox-session-start` to begin recording

### Multiple recordings shown
**Symptom:** Multiple recordings displayed
**Explanation:** This is normal when multiple worktrees or agents are recording simultaneously. Use `--current` to see only your agent's recording.

### --current shows nothing
**Symptom:** `--current` returns "Not recording" even though recordings exist
**Solution:** SAGEOX_AGENT_ID environment variable may not be set. Run `ox agent prime` first.

## Post-Command

After the command completes, check the JSON output:
- **`recording: true`** — A session is active. Continue working normally.
- **`recording: false`** — No active session. Consider running `/ox-session-start` if you want to record.
- **`count > 1`** — Multiple concurrent recordings. The `sessions` array shows each one.
- **`agent_id`** — The agent ID of the recording. Compare with your own to identify your session.

$ox session status --json --current
