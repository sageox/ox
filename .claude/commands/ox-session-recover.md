<!-- ox-hash: placeholder ver: 0.17.0 -->
<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.).
     Exception: Post-Command sections that require agent-side actions (e.g.,
     displaying a notice, generating a summary) are legitimate here. -->
Recover an ox session from a Claude Code transcript when session recording failed or captured no data.

Use when:
- `ox session stop` shows `entry_count: 0`
- Session viewer shows empty or "Session not found"
- You need to rebuild a session from a Claude Code transcript file

## Recovery Flow

### Step 1: Find the Claude Code transcript

Claude Code stores transcripts at:
```
~/.claude/projects/<PROJECT_HASH>/*.jsonl
```

Where `PROJECT_HASH` is the CWD with `/` replaced by `-` (e.g., `-Users-ryan-Code-my-project`).

Find the right transcript by checking modification times against the session's time window:
```bash
ls -lt ~/.claude/projects/<PROJECT_HASH>/*.jsonl | head -5
```

### Step 2: Try the built-in recover command first

```bash
ox agent <agent_id> session recover
```

If the adapter file still exists, this handles everything automatically. If it fails, continue to Step 3.

### Step 3: Convert transcript to web viewer format

The Claude Code transcript uses internal types (`queue-operation`, `progress`) that the web viewer cannot display. Convert to the expected format (`user`, `assistant`, `tool`, `system`).

**Claude Code transcript entry types and their mapping:**

| Transcript `type` | `message.role` | Web viewer `type` | Content location |
|---|---|---|---|
| `user` | `user` | `user` | `message.content` (string or content blocks) |
| `assistant` | `assistant` | `assistant` | `message.content` text blocks |
| `assistant` | (tool_use blocks) | `tool` | `message.content` tool_use blocks |
| `progress` | (tool_result) | `tool` | `data.content` tool_result blocks |
| `system` | | `system` | `message.content` |
| `queue-operation` | | skip | internal queue ops |

**Required output JSONL format (one JSON object per line):**
```json
{"type": "user", "content": "the user message", "ts": "ISO8601", "seq": 1}
{"type": "assistant", "content": "assistant text response", "ts": "ISO8601", "seq": 2}
{"type": "tool", "content": "", "tool_name": "Bash", "tool_input": "{...}", "ts": "ISO8601", "seq": 3}
{"type": "tool", "content": "", "tool_output": "command output", "ts": "ISO8601", "seq": 4}
```

**Validation rules (lint before upload):**
- Every entry MUST have `type` in: `user`, `assistant`, `system`, `tool`
- Every entry MUST have `ts` (ISO8601 timestamp) and `seq` (integer)
- `user`/`assistant`/`system` entries MUST have non-empty `content`
- `tool` entries MUST have `tool_name` + `tool_input`, OR `tool_output`
- Skip entries with `type: queue-operation` (internal Claude Code ops)
- Skip user entries starting with `<system_instruction>` (framework context)
- Extract text from assistant content blocks: use `text` field from `{"type": "text", "text": "..."}` blocks
- Skip `thinking` blocks from assistant content

### Step 4: Lint the converted JSONL

Before uploading, validate the output:
```bash
ox session lint --file /tmp/converted-raw.jsonl
```

This checks all entries have valid types, required fields, and web viewer compatibility. Fix any errors before uploading.

### Step 5: Upload to ledger

Write the converted JSONL to the session cache, then use `ox session upload`:

```bash
# Copy converted raw.jsonl to the session's ledger directory
LEDGER=$(ox status --json 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('ledger',{}).get('path',''))")
SESSION_DIR="$LEDGER/sessions/<session-name>"

cp /tmp/converted-raw.jsonl "$SESSION_DIR/raw.jsonl"

# Upload (handles LFS upload, meta.json, commit, push)
ox session upload <session-name>
```

### Step 6: Verify

Check the session is viewable at:
```
https://sageox.ai/repo/<repo_id>/sessions/<session-name>/viewer
```

The web viewer fetches `raw.jsonl` content via the LFS OID in `meta.json`. If the viewer shows empty, check that `meta.json` has the correct OID for `raw.jsonl`.

## Future

Once `ox session import-transcript` is implemented (see GitHub issue #192), this manual flow will be replaced by:
```bash
ox session import-transcript ~/.claude/projects/<hash>/<session-id>.jsonl
```
