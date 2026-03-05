<!-- ox-hash: 9b5ef157c16c ver: v0.13.0-1-gf9fefd1 -->
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

## Post-Command: Generate and Push Summary (REQUIRED)

After the command completes, check the JSON output for a `summary_prompt` field.
This step is critical for session completeness — without it, the session has no rich summary.

**If `summary_prompt` is present:**
1. Read the prompt carefully — it references the raw session file on disk
2. Read the raw session file at the path specified in the prompt
3. Generate the summary JSON following the Output Format in the prompt
4. Save it to a temporary file (e.g., `.ox-summary.json` in the workspace root, or `/tmp/ox-summary.json`) — do NOT write to the session cache dir as it may be outside the workspace sandbox
5. If the prompt includes a `push-summary` step, run that command with `--file` pointing to your temp file
6. Verify the push succeeded by checking the JSON output for `"success": true`
7. Clean up the temporary summary file

**If summary generation fails:**
- Run `ox agent <id> doctor` — it can detect and help recover missing summaries
- The session data is safe regardless; only the rich summary is missing

$ox agent session stop
