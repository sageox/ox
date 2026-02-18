Abort the current session, discarding all local data without uploading to the ledger.

Use when:
- The user wants to throw away the current session recording
- A session was started by accident
- The planning or approach was wrong and the session should not be shared
- Session data is corrupted and a clean start is needed

Keywords: session abort, discard, cancel, throw away, delete session, abandon

## Important

This is a DESTRUCTIVE operation. The session data will be permanently deleted
and will NOT be uploaded to the ledger. This cannot be undone.

## Pre-Command: Confirm with User (REQUIRED)

Before running abort, you MUST confirm with the user that they want to discard
the session. The abort command requires `--force` when called programmatically.

Example: "Are you sure you want to abort and discard this session? This cannot be undone."

## Common Issues

### Not recording
**Symptom:** `no active session to abort`
**Solution:** No session is currently active. Nothing to abort.

### Want to keep the data
**Symptom:** You want to stop recording but save the data
**Solution:** Use `/ox-session-stop` instead - that processes and uploads to the ledger

$ox agent session abort --force
