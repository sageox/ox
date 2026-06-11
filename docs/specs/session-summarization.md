# Session Summarization Architecture

How session summaries are generated, including prompt composition and the three summarization paths.

## Output Schema

All paths produce the same `SummarizeResponse` JSON structure (defined in `internal/session/summarize.go:131-141`):

| Field | Type | Description |
|-------|------|-------------|
| `title` | string | Short descriptive title (5-10 words) |
| `summary` | string | One paragraph executive summary |
| `key_actions` | []string | Bullet points of actions taken |
| `outcome` | string | `success`, `partial`, or `failed` |
| `topics_found` | []string | Topics detected during session |
| `diagrams` | []string | Extracted mermaid diagram code |
| `aha_moments` | []AhaMoment | Pivotal moments of collaborative intelligence (3-5) |
| `sageox_insights` | []SageoxInsight | Moments where SageOx guidance provided value |

## Shared Prompt Guidelines

The `SummaryPromptGuidelines` constant (`internal/session/summarize.go:24-91`) is the single source of truth for the JSON output schema and scoring rubrics. It defines:

1. The exact JSON structure expected
2. Aha moment types: `question`, `insight`, `decision`, `breakthrough`, `synthesis`
3. Guidance to prioritize human contributions
4. SageOx insight attribution patterns (e.g., "Based on SageOx domain guidance...")

Both client-side paths embed this constant directly. The server-side path should match it.

## Three Summarization Paths

### 1. Server-Side API (`Summarize()`)

**Location:** `internal/session/summarize.go:145-189`
**Trigger:** `ox session stop` (default path when authenticated)

Flow:
- `buildSummarizeRequest()` converts `[]Entry` → `SummarizeRequest` (agent ID, type, model, simplified entries)
- POSTs to `POST /api/v1/session/summarize` with bearer token
- Server composes the prompt and calls the LLM
- Returns `SummarizeResponse` JSON

The prompt is composed **server-side** — the CLI sends raw entries, not a prompt.

### 2. Client-Side Agent Prompt (`BuildSummaryPrompt()`)

**Location:** `internal/session/summarize.go:218-254`
**Trigger:** `ox session stop` when the calling agent should generate the summary itself (avoids server round-trip)

Prompt structure:
1. Header: `"# Summarize Session"` + instruction
2. `SummaryPromptGuidelines` constant (output schema + rubrics)
3. Session content as fenced code block, formatted as `[seq] TYPE: content`
4. Step-by-step instructions ending with save path (`<raw-path>-summary.json`)

### 3. Resummary Prompt (`buildResummaryPrompt()`)

**Location:** `cmd/ox/session_resummary.go:93-143`
**Trigger:** `ox session resummary` — regenerates a summary from an existing JSONL file

Prompt structure:
1. Header: `"# Re-summarize Session"` + instruction
2. `SummaryPromptGuidelines` constant (same shared source)
3. Session content as fenced code block, formatted as `[seq] TYPE: content`
   - Skips `header`, `footer`, and `_meta` entries
   - In compact mode, truncates content >500 chars
4. Step-by-step instructions ending with save path (`summary.json` in same directory as input)

The resummary prompt is **printed to stdout** for the user to paste into an agent — it is not executed by the CLI.

## Key Design Decisions

- **Shared guidelines constant** ensures all paths produce structurally identical output
- **Server-side path** sends raw entries, not a pre-composed prompt — the server owns its own prompt engineering
- **Client-side paths** embed the guidelines directly so they work offline / without API calls
- **Resummary is copy-paste** rather than auto-executed, giving the user control over which agent/model processes it
