<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.).
     Exception: Post-Command sections that require agent-side actions (e.g.,
     displaying a notice, generating a summary) are legitimate here. -->
Stop recording and save this agent session to the project ledger.

## Post-Command

Follow the `guidance` field in the JSON output. If `summary_prompt` is present,
generate the summary as it instructs and pipe it back via stdin:

```bash
echo "$summary_json" | ox session push-summary --file - --session-dir <session-dir>
```

Pipe via stdin — never write the JSON to `/tmp/` or any shared path (concurrent
agents race on shared filenames and macOS tmpfs GC can reap them mid-run).

$ox agent session stop
