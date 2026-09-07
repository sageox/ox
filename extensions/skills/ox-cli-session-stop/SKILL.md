---
name: ox-cli-session-stop
description: "Stop recording and save this agent session to the project ledger."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

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
