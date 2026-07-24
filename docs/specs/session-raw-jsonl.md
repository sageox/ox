# Session raw.jsonl File Format Specification

The `raw.jsonl` file is the primary session recording format produced by `ox session start/stop`. Each line is a valid JSON object. The file captures an agent-to-human conversation session with full provenance for replay, summarization, and auditing.

## Machine-Readable Schema

A published JSON Schema for a single `raw.jsonl` line lives at
[`schema/v1/raw-jsonl.schema.json`](../../schema/v1/raw-jsonl.schema.json)
(`$id`: `https://sageox.ai/schemas/session/v1/raw-jsonl.schema.json`, MIT).
It is validated in CI against this repo's session fixtures **and** against live
`SessionWriter` output, so it describes what ox actually writes rather than what
this document wishes were true.

This document stays normative for everything a per-line schema cannot express:
line ordering, completeness, secret redaction, and the PII boundary on
`username`. Where the two disagree, that is a bug — please file it.

## Completeness Requirement

**`raw.jsonl` MUST contain the complete, unfiltered output from the coding agent.** Every conversation turn — user messages, assistant responses, tool calls, tool results, and system messages — must be included verbatim. Do not summarize, truncate, or selectively omit entries.

This file is the source of truth from which all derived artifacts (summaries, event logs, HTML views) are generated. Downstream consumers depend on having the full conversation to:

- Generate accurate AI summaries
- Extract structured events for the event log
- Replay sessions for debugging and auditing
- Detect tool usage patterns and coworker interactions
- Produce complete HTML/Markdown session views

The only transformation applied before writing is **secret redaction** — API keys, tokens, and credentials are replaced with `[REDACTED:pattern_name]` placeholders. The conversation structure and all non-secret content must remain intact.

## File Location

```
sessions/<session-name>/raw.jsonl
```

Session name format: `YYYY-MM-DDTHH-MM-<username>-<agentID>`
Example: `2026-01-06T14-32-ryan-Ox7f3a/raw.jsonl`

## Structure Overview

```
Line 1:     Header  (required, exactly one)
Lines 2-N:  Entries  (zero or more)
Last Line:  Footer   (required, exactly one)
```

## Line 1: Header

The header identifies the session and provides provenance metadata.

```json
{"type":"header","metadata":{...}}
```

### `metadata` Fields (StoreMeta)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | string | yes | Schema version. Currently `"1.0"` |
| `created_at` | ISO8601 | yes | When the session file was created |
| `agent_id` | string | no | Short agent ID (e.g., `"Ox7f3a"`). Carries `omitempty` — absent when unset |
| `agent_type` | string | no | Agent identifier (e.g., `"claude-code"`). Carries `omitempty` — absent when unset |
| `agent_version` | string | no | Version of the coding agent (e.g., `"1.0.3"`) |
| `model` | string | no | LLM model used (e.g., `"claude-sonnet-4-20250514"`) |
| `username` | string | no | Privacy-safe display name, via `identity.AttributionDisplayName()`. **NOT an email** — `raw.jsonl` is committed to a ledger repo, so writers must never put a contactable address here. Also accepted on read as `ox_username`. |
| `repo_id` | string | no | SageOx repo ID for provenance |
| `session_id` | string | no | The `ses_` recording identity minted at session start. The header is its **crash-safe carrier**: `.recording.json` is deleted at stop/abort, so an orphan-finalize can only recover this ID from here. Only `ses_`-prefixed values are accepted into this field — see the caution below. |
| `ox_version` | string | no | Version of ox that created the session |

Example:
```json
{"type":"header","metadata":{"version":"1.0","created_at":"2026-01-06T14:32:00Z","agent_id":"Ox7f3a","agent_type":"claude-code","agent_version":"1.0.3","model":"claude-sonnet-4-20250514","username":"testdev","repo_id":"repo_01JEYQ9Z8X"}}
```

### Alternative Header Format

The reader also accepts the `_meta` wrapper format (used by capture-prior/import):
```json
{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"Ox7f3a","started_at":"2026-01-06T14:32:00Z",...}}
```

> **Caution — `session_id` means two different things.** In this dialect it is an
> agent-supplied identifier (`"Ox7f3a"` above). In the native header's `metadata`
> it is the `ses_` recording identity. `ParseStoreMeta` keeps them apart by
> accepting only `ses_`-prefixed values into the recording-identity field; a
> consumer that conflates them attributes a session to the wrong recording.
>
> The reader also accepts `started_at` as an alias for `created_at`, and
> `ox_username` as an alias for `username`.

See [Capture-Prior Format](#capture-prior-format) for details.

## Lines 2-N: Entries

Each entry represents a conversation turn or tool invocation. Entries are written using `WriteRaw()` which adds `timestamp` and `seq` if not present.

```json
{"type":"<entry_type>","content":"...","timestamp":"2026-01-06T14:32:01Z","seq":0}
```

### Entry Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Entry type. **Open vocabulary** — see below |
| `content` | string | no | Message text or tool output. Absent on some tool entries, which carry everything in `tool_*` |
| `timestamp` | ISO8601 | no | When the entry was recorded. Auto-added by `WriteRaw()`/`WriteEntry()` when absent; the import dialect uses **`ts`** instead |
| `seq` | integer | no | Sequence number. Auto-added when absent. ox writes **zero-based**; the import dialect is **one-based**. Ordering only, never an identity |
| `eid` | string | no | Unique 5-char alphanumeric entry identifier. Auto-generated when absent; missing entirely on pre-`eid` sessions and on the import dialect |
| `tool_name` | string | no | Tool name (only for `tool` entries). Import dialect uses **`tool`** |
| `tool_input` | string **or object** | no | Tool input. ox writes a string (often itself JSON); the import dialect writes a decoded object. A reader that checks only for a string silently drops every imported tool call's arguments |
| `tool_output` | string **or object** | no | Tool output. Same string-or-object split as `tool_input` |
| `coworker_name` | string | no | Coworker/subagent name if applicable |
| `coworker_model` | string | no | Coworker model tier (sonnet, opus, haiku) |
| `data` | object | no | Nested payload written by `WriteEntry()` (as opposed to the flat `WriteRaw()` shape). Typically carries `role` and `content` |

**`type` is the only field an entry is guaranteed to have.** Everything else is
optional, aliased, or auto-injected — so a reader must not assume presence. Only
`version` and `created_at` are guaranteed in a native header's `metadata`; they
are the two fields on `StoreMeta` without `omitempty`.

**Entry ID (`eid`):** Each entry gets a unique 5-character identifier from `[0-9A-Za-z]` (62^5 ≈ 916M possibilities). Generated automatically by `WriteRaw()` if not provided by the caller. Provides stable references for deduplication, cross-referencing from derived artifacts, and audit trails. Sessions recorded before this field was added will have entries without `eid` — readers should tolerate missing values.

### Entry Types

| Type | Description |
|------|-------------|
| `user` | Human user message or prompt |
| `assistant` | AI agent response |
| `system` | System message (context injection, framework content, coworker load) |
| `tool` | Tool call or result (bash, read, write, edit, grep, glob) |

**The vocabulary is open, and wider than those four.** `WriteRaw()` takes a
`map[string]any` and writes whatever `type` the caller supplies, so values
including `message`, `tool_call`, and `tool_result` also occur in real sessions.
`header` and `footer` are the only reserved values. **A reader MUST preserve an
entry whose `type` it does not recognize rather than dropping or rejecting it** —
that is why the published schema does not enumerate this field.

### Dialects

`raw.jsonl` is not one shape. Three occur in practice, and a reader has to
handle all three:

| Dialect | Written by | Header | Entries |
|---|---|---|---|
| **Flat** (this document's default) | `SessionWriter.WriteRaw()` | `type:"header"` + `metadata` | flat keys, `timestamp`, zero-based `seq`, `eid` |
| **Nested** | `SessionWriter.WriteEntry()` | same | `{type, timestamp, seq, eid, data:{role, content}}` |
| **Import** | `ox agent <id> session capture-prior`, importers | `_meta` wrapper | `ts`, one-based `seq`, `tool`, object-valued `tool_input`, no `eid` |

Convergence on one shape is desirable but not scheduled; until then, treat the
aliases in the field table above as required reading, and validate against the
[published schema](../../schema/v1/raw-jsonl.schema.json) rather than assuming
one dialect.

### Adapter-Layer Entry Classification

Coding agents (e.g., Claude Code) mix human and system content in `type: "user"` protocol entries. The adapter layer (`ClaudeCodeAdapter`) classifies these before writing to `raw.jsonl`, so downstream consumers see correct types without needing their own heuristics.

**Classification rules for source `type: "user"` entries:**

| Content Pattern | Classified As | Rationale |
|----------------|---------------|-----------|
| Plain human text | `user` | Genuine human message |
| `<system-reminder>...</system-reminder>` tags only | `system` | Framework-injected context |
| `<system_instruction>...</system_instruction>` tags only | `system` | System directives |
| `<system-instruction>...</system-instruction>` tags only | `system` | System directives (hyphen variant) |
| `<!-- ox-hash: ... -->` marker | `system` | Skill expansion content |
| `isMeta: true` flag | `system` | Agent framework metadata |
| Plan mode boilerplate (`Entered plan mode.`, `Plan mode is active.`, `Plan mode still active`) | `system` | Framework UI injection |
| Context compaction continuation (`This session is being continued from a previous conversation`) | `system` | Injected when session resumes after hitting context window limit |
| `<local-command-stdout>...</local-command-stdout>` tags only | `system` | Conductor local command output |
| `<local-command-caveat>...</local-command-caveat>` tags only | `system` | Conductor framework caveat injection |
| Unicode tool prefixes (`⏺` U+23FA, `⎿` U+23BF) | `system` | Agent tool call/result display markers |
| Pure `tool_result` content blocks (no text) | *(skipped)* | Deduplicated — tool calls already captured from assistant `tool_use` blocks |
| Mixed human text + system tags | `user` | Tags stripped, human text preserved |

**Deduplication:** `tool_result` content blocks in user-role entries are protocol plumbing — the agent framework feeds tool output back as user messages. Since tool calls and their results are already captured from the assistant's `tool_use` blocks (as `tool` entries), including `tool_result` blocks would duplicate content. They are omitted from `raw.jsonl`.

**Backward compatibility:** The HTML generator (`cleanMessageContent`) and view layer (`reclassifyByContent`) retain tag-stripping logic as safety nets for sessions recorded before adapter-layer classification was added.

### Entry Examples

User message:
```json
{"type":"user","content":"Fix the login bug","timestamp":"2026-01-06T14:32:01Z","seq":0,"eid":"aB3xZ"}
```

Assistant response:
```json
{"type":"assistant","content":"I'll investigate the login flow...","timestamp":"2026-01-06T14:32:05Z","seq":1,"eid":"k9Qm2"}
```

Tool call:
```json
{"type":"tool","content":"","tool_name":"bash","tool_input":"go test ./...","tool_output":"ok  github.com/user/repo 1.234s","timestamp":"2026-01-06T14:32:10Z","seq":2,"eid":"Tn4pL"}
```

System message (coworker load):
```json
{"type":"system","content":"Loaded coworker: code-reviewer (model: sonnet)","coworker_name":"code-reviewer","coworker_model":"sonnet","timestamp":"2026-01-06T14:33:00Z","seq":5,"eid":"Wv7jR"}
```

## Last Line: Footer

The footer provides session summary statistics.

```json
{"type":"footer","closed_at":"2026-01-06T15:00:00Z","entry_count":42}
```

### Footer Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Always `"footer"` |
| `closed_at` | ISO8601 | yes | When the session was closed |
| `entry_count` | integer | yes | Total entries written (excluding header/footer) |

## Complete Example

```jsonl
{"type":"header","metadata":{"version":"1.0","created_at":"2026-01-06T14:32:00Z","agent_id":"Ox7f3a","agent_type":"claude-code","model":"claude-sonnet-4-20250514","username":"testdev"}}
{"type":"user","content":"Fix the failing test in auth_test.go","timestamp":"2026-01-06T14:32:01Z","seq":0,"eid":"aB3xZ"}
{"type":"assistant","content":"I'll look at the test file to understand the failure.","timestamp":"2026-01-06T14:32:03Z","seq":1,"eid":"k9Qm2"}
{"type":"tool","content":"","tool_name":"read","tool_input":"/path/to/auth_test.go","timestamp":"2026-01-06T14:32:04Z","seq":2,"eid":"Tn4pL"}
{"type":"assistant","content":"The test expects a different error message. Let me fix it.","timestamp":"2026-01-06T14:32:08Z","seq":3,"eid":"Wv7jR"}
{"type":"tool","content":"","tool_name":"edit","tool_input":"{\"file_path\":\"/path/to/auth_test.go\",\"old_string\":\"...\",\"new_string\":\"...\"}","timestamp":"2026-01-06T14:32:10Z","seq":4,"eid":"m5PqX"}
{"type":"tool","content":"","tool_name":"bash","tool_input":"go test ./internal/auth/...","tool_output":"ok  github.com/user/repo/internal/auth 0.5s","timestamp":"2026-01-06T14:32:15Z","seq":5,"eid":"nR8cY"}
{"type":"assistant","content":"Test is passing now.","timestamp":"2026-01-06T14:32:17Z","seq":6,"eid":"J2fKd"}
{"type":"footer","closed_at":"2026-01-06T14:32:20Z","entry_count":7}
```

## Companion Files

A session folder may contain additional derived files alongside `raw.jsonl`:

| File | Description | Generated By |
|------|-------------|--------------|
| `summary.json` | AI-generated session summary | `ox session stop` (via API or agent prompt) |
| `summary.md` | Markdown session summary | `ox session stop` |
| `session.html` | HTML session viewer | `ox session view --html` |
| `session.md` | Markdown session transcript | `ox session view --md` |
| `meta.json` | LFS metadata (file hashes, sizes) | LFS upload pipeline |
| `.recording.json` | Active recording state (deleted on stop) | `ox session start` |

## Capture-Prior Format

The `ox agent <id> session capture-prior` command accepts a slightly different JSONL format for importing externally-generated session history. This format uses `_meta` wrapper and includes `seq` in entries.

### Header

```json
{"_meta":{"schema_version":"1","source":"planning_history","agent_id":"Ox7f3a","agent_type":"claude-code","session_title":"Architecture planning","captured_at":"2026-01-06T14:00:00Z"}}
```

### HistoryMeta Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `schema_version` | string | yes | Currently `"1"` |
| `source` | string | yes | How history was generated (e.g., `"planning_history"`, `"agent_reconstruction"`) |
| `agent_id` | string | yes | Short agent ID |
| `agent_type` | string | no | Agent identifier |
| `session_title` | string | no | Human-readable session title |
| `captured_at` | ISO8601 | no | When the history was captured |
| `started_at` | ISO8601 | no | When the original conversation began |
| `message_count` | integer | no | Total entry count |
| `time_range` | object | no | `{"earliest":"...","latest":"..."}` |

### Entries

```json
{"seq":1,"type":"user","content":"Let's plan the architecture","ts":"2026-01-06T14:00:01Z","source":"planning_history"}
{"seq":2,"type":"assistant","content":"Here's my proposed architecture...","ts":"2026-01-06T14:00:05Z","source":"planning_history","is_plan":true}
```

### Capture-Prior Entry Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `seq` | integer | yes | Monotonically increasing sequence number (starting from 1) |
| `type` | string | yes | One of: `user`, `assistant`, `system`, `tool` |
| `content` | string | yes | Message content |
| `ts` | ISO8601 | no | Entry timestamp |
| `source` | string | no | Origin marker (e.g., `"planning_history"`) |
| `is_plan` | boolean | no | Marks entry as containing a plan/decision |
| `tool_name` | string | no | Tool name (for `tool` entries) |
| `tool_input` | string | no | Tool input (for `tool` entries) |
| `tool_output` | string | no | Tool output (for `tool` entries) |
| `summary` | string | no | Brief summary of the entry |

### Validation Rules (Capture-Prior)

1. First line MUST be a valid `_meta` object with `schema_version`, `source`, and `agent_id`
2. `seq` numbers MUST be monotonically increasing (no duplicates, no gaps allowed but not required to be contiguous)
3. `type` MUST be one of: `user`, `assistant`, `system`, `tool`
4. `content` MUST be non-empty for all entries
5. `tool` entries SHOULD include `tool_name`
6. At least one entry is required for capture-prior

## Generic Agent Input Format

For coding agents without a deep adapter (Codex, Amp, Cursor, etc.), ox provides a generic session capture mechanism. The agent writes JSONL to a "drop file" that ox creates at session start.

### How It Works

1. `ox agent <id> session start` returns JSON with `session_file` (path to write to), `format_hint` (example entries), and `next_actions` (machine-parseable steps)
2. During the session, the agent writes entries using `ox agent <id> session log` or direct JSONL appends
3. `ox agent <id> session stop` reads the file via the generic adapter, redacts secrets, and saves to the ledger
4. The drop file (`input.jsonl`) is deleted after successful processing (it contains pre-redaction content)

### Writing Entries

**Recommended: Use the `session log` command** (handles formatting, timestamps, seq numbers, file locking):

```bash
ox agent <id> session log --role user --content "Fix the login bug"
ox agent <id> session log --role assistant --content "I'll investigate the auth flow..."
ox agent <id> session log --role tool --tool-name bash --tool-input "go test ./..." --content "PASS"
echo "Long content" | ox agent <id> session log --role assistant --stdin
```

**Alternative: Write JSONL directly** to `session_file`:

```jsonl
{"type":"user","content":"Fix the login bug","timestamp":"2026-03-05T19:32:01Z"}
{"type":"assistant","content":"I'll investigate the auth flow...","timestamp":"2026-03-05T19:32:05Z"}
{"type":"tool","content":"PASS","tool_name":"bash","tool_input":"go test ./...","timestamp":"2026-03-05T19:32:10Z"}
```

### Entry Fields (Generic Input)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Entry type: `user`, `assistant`, `system`, `tool` |
| `content` | string | yes | Message text or tool output |
| `timestamp` | ISO8601 | no | When the entry occurred (auto-added by `session log`) |
| `seq` | integer | no | Sequence number (auto-added by `session log`) |
| `tool_name` | string | no | Tool name (for `type:"tool"` entries) |
| `tool_input` | string | no | Tool input (for `type:"tool"` entries) |

### Optional Header

An optional header line provides metadata. If present, it must be the first line:

```json
{"type":"header","metadata":{"agent_type":"codex","model":"gpt-4.1","agent_version":"0.1.2"}}
```

### Error Tolerance

- Malformed JSON lines are skipped (logged as warnings), not fatal
- Duplicate headers (from retry concatenation) are skipped
- Empty files return zero entries (not an error)
- Missing timestamps default to current time during processing

## Security

- Secret redaction is applied before writing: API keys, tokens, passwords, and other sensitive patterns are replaced with `[REDACTED:pattern_name]`
- File permissions: `0644` for session files, `0755` for session directories
- Auth tokens and credentials detected in tool output are automatically redacted

## Implementation References

| Component | File | Description |
|-----------|------|-------------|
| `SessionWriter` | `internal/session/store.go` | Writes raw.jsonl (header, entries, footer) |
| `StoreMeta` | `internal/session/store.go:253` | Header metadata struct |
| `SessionEntry` | `internal/session/session.go:23` | Entry struct definition |
| `SessionMeta` | `internal/session/metadata.go:16` | Session lifecycle metadata |
| `HistoryMeta` | `internal/session/history_schema.go:46` | Capture-prior metadata |
| `HistoryEntry` | `internal/session/history_schema.go:85` | Capture-prior entry |
| `ClaudeCodeAdapter` | `internal/session/adapters/claude_code.go` | Reads Claude Code JSONL source |
| `Redactor` | `internal/session/secrets.go` | Secret redaction pipeline |
| `ValidateHistoryJSONLReader` | `internal/session/history_schema.go:140` | Validates capture-prior input |
