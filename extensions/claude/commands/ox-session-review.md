<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
Audit all sessions in the project ledger for quality, then offer cleanup and regeneration actions.

## Phase 1 — Scan & Score (read-only)

1. Run `ox session list --all` to enumerate all sessions
2. Resolve the ledger sessions directory:
   - Read `.sageox/config.json` to get `repo_id` and endpoint
   - The ledger lives at `~/.local/share/sageox/<endpoint-slug>/ledgers/<repo_id>/sessions/`
   - List subdirectories — each is a session folder
3. For each session directory:
   a. Read `meta.json` — get `entry_count`, `title`, `summary`, `files`, `created_at`
   b. Read `summary.json` if it exists — get `title`, `summary`, `key_actions`, `outcome`
   c. Check hydration status: if `raw.jsonl` is missing or is an LFS pointer (starts with `version https://git-lfs.github.com`), run `ox session download <session_name>` to hydrate it
   d. Read `raw.jsonl` — count substantive messages, tool calls (Write/Edit), total entries
   e. Score quality using the signals below

## Quality Signals

Categorize each session into one of these buckets (first match wins):

### Removal Candidates
- `entry_count` is 0 OR meta has no `files` manifest
- Very few substantive messages (< 3 user+assistant turns in `raw.jsonl`)
- Only skill invocations, no real work (e.g., just `/ox-session-start` + `/ox-session-stop` with nothing between)
- Session errored/failed immediately (check `outcome` = "failed" in summary, or raw.jsonl shows early error)
- No files modified (zero Write/Edit tool calls) AND < 5 total entries in `raw.jsonl`

### Missing/Poor Summary
- No `summary.json` exists at all
- `summary.json` has empty or missing `title`, `summary`, or `key_actions`
- Summary is fallback stats-only text (matches pattern like "N user messages, N assistant responses")

### Poor Title
- Title is empty, or matches generic patterns: "Session recording", date-only, fewer than 4 words
- Title doesn't reflect actual session content (compare title to raw.jsonl topics)

### OK
- Session passes all checks above

## Phase 2 — Report

Present a grouped report to the user. Use tables for each category:

**Removal Candidates** (N sessions)
| Session | Date | Entries | Reason |
|---------|------|---------|--------|

**Missing/Poor Summaries** (N sessions)
| Session | Date | Issue |
|---------|------|-------|

**Poor Titles** (N sessions)
| Session | Date | Current Title | Suggested Title |
|---------|------|---------------|-----------------|

**Summary**: X total sessions, Y removal candidates, Z need summary work, W have poor titles, V are OK.

## Phase 3 — User Confirms Actions

ASK the user which sessions to act on. Present options:
- Remove specific sessions (list them)
- Regenerate summaries for specific sessions (list them)
- Skip / do nothing

**NEVER auto-delete or auto-regenerate.** Wait for explicit user approval on each category.

## Phase 4 — Execute Approved Actions

### For approved removals:
```
ox session remove <session_name> --force
```

### For approved summary regenerations:
1. Read the full `raw.jsonl` for the session
2. Generate a summary JSON object matching this exact schema:

```json
{
  "title": "Short descriptive title (5-10 words)",
  "summary": "One paragraph executive summary describing what was accomplished",
  "key_actions": ["Action 1", "Action 2"],
  "outcome": "success|partial|failed",
  "topics_found": ["topic1", "topic2"],
  "diagrams": [],
  "chapter_titles": ["Phase 1", "Phase 2"],
  "aha_moments": [
    {
      "seq": 7,
      "role": "user|assistant",
      "type": "question|insight|decision|breakthrough|synthesis",
      "highlight": "Key text from this moment",
      "why": "Why this was important"
    }
  ],
  "sageox_insights": []
}
```

3. Save the JSON to a temp file (e.g., `/tmp/ox-summary-<session_name>.json`)
4. Push via: `ox session push-summary --file /tmp/ox-summary-<session_name>.json --session-dir <full_path_to_session_dir>`
5. Verify the push succeeded (check JSON output for `"success": true`)
6. Clean up the temp file

## Edge Cases

- **Empty ledger**: Report "No sessions found" and exit
- **All sessions OK**: Report "All N sessions are healthy" and exit
- **Hydration failure**: Log warning, skip that session, continue with others
- **Large sessions**: For raw.jsonl > 5000 lines, read first 200 and last 200 lines for quality assessment
