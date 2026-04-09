---
paths:
  - "cmd/ox/session*.go"
  - "internal/session/**"
---

# Planning Session Capture

Import prior planning discussions as sessions.

```bash
cat planning.jsonl | ox agent <id> session import
ox agent <id> session import --file planning.jsonl --title "Plan"
```

**JSONL format:**
```jsonl
{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"<ISO8601>"}}
{"ts":"<ISO8601>","type":"user","content":"<prompt>","seq":1,"source":"planning_history"}
{"ts":"<ISO8601>","type":"assistant","content":"<response>","seq":2,"source":"planning_history"}
{"ts":"<ISO8601>","type":"assistant","content":"<final plan>","seq":3,"source":"planning_history","is_plan":true}
```

**Rules:** Sequential `seq` numbers. Types: `user`, `assistant`, `system`, `tool`. Mark final plan with `"is_plan":true`. Capture key points, not tool spam.

**When to offer:** After finalizing plans, ask: "Want me to capture this planning session?"

## Capturing Prior Planning (Before `ox session start`)

Reconstruct conversation as JSONL with `seq`, `type`, `content`, `ts` (ISO8601), `source: "planning_history"`. First line must be `_meta` header. Pipe to `ox agent <id> session capture-prior`.
