<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.).
     Exception: Post-Command sections that require agent-side actions (e.g.,
     displaying a notice, generating a summary) are legitimate here. -->
Stop recording and save this agent session to the project ledger.

## Post-Command

After the command completes, check the JSON output for `guidance` and `summary_prompt` fields.
Follow the `guidance` field for next steps.

**If `summary_prompt` is present (sync mode):**
1. Read the prompt carefully — it references the raw session file on disk
2. Read the raw session file at the path specified in the prompt
3. Generate the summary JSON following the Output Format in the prompt
4. Pipe the JSON to `ox session push-summary --file - --session-dir <session-dir>` via stdin. Do NOT write the JSON to `/tmp/` or any shared path — multiple agents may run concurrently on the same machine and race on shared filenames, and macOS tmpfs GC can reap files between attempts.
   ```bash
   echo "$summary_json" | ox session push-summary --file - --session-dir <session-dir>
   ```
5. Verify the push succeeded by checking the JSON output for `"success": true`

**If `summary_prompt` is absent (async mode):**
No agent action required. Upload and summary generation happen automatically in the background.

**If summary generation fails:**
- Run `ox agent <id> doctor` — it can detect and help recover missing summaries
- The session data is safe regardless; only the rich summary is missing

$ox agent session stop
