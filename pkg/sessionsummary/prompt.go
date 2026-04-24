package sessionsummary

import (
	"fmt"
	"strings"
)

// SummaryPromptGuidelines contains the shared guidelines for session summarization.
// Used by both the CLI resummary command and the server-side summarization endpoint.
const SummaryPromptGuidelines = `## Output Format

Create a JSON object with this structure:

{
  "title": "Short descriptive title (5-10 words)",
  "summary": "One paragraph executive summary describing what was accomplished",
  "key_actions": [
    "Action 1 that was taken",
    "Action 2 that was taken"
  ],
  "outcome": "success|partial|failed",
  "topics_found": ["topic1", "topic2"],
  "diagrams": ["mermaid diagram code if any were created"],
  "chapter_titles": ["Problem Discussion", "Root Cause Analysis", "Implementation", "Testing & Verification"],
  "aha_moments": [
    {
      "seq": 7,
      "role": "user|assistant",
      "type": "question|insight|decision|breakthrough|synthesis",
      "highlight": "The exact quote or key text from this moment",
      "why": "Brief explanation of why this was a pivotal moment"
    }
  ],
  "sageox_insights": [
    {
      "seq": 12,
      "topic": "react-patterns",
      "insight": "What SageOx guidance was applied",
      "impact": "The outcome or value it provided"
    }
  ],
  "agent_summary": {
    "decisions": [
      {
        "what": "What was decided",
        "why": "Rationale for the decision",
        "owner": "Who owns this decision"
      }
    ],
    "action_items": [
      {
        "task": "What needs to be done",
        "assignee": "Who should do it",
        "priority": "high|medium|low"
      }
    ],
    "open_questions": [
      {
        "question": "Unresolved question from the session",
        "context": "Why this matters or what it blocks"
      }
    ],
    "technical_context": {
      "technologies": ["languages, frameworks, tools used or discussed"],
      "architecture": ["architectural patterns, components, or systems involved"],
      "integrations": ["external services, APIs, or systems integrated with"]
    },
    "constraints": ["Technical or business constraints identified"],
    "non_goals": ["Things explicitly decided NOT to do"]
  },
  "quality_score": 0.75,
  "score_reason": "New feature with architectural decision and test coverage"
}

## Chapter Titles Guidelines

Generate 3-8 short chapter titles (2-4 words each) that narrate the session's progression.
Each title corresponds to a conversation phase (roughly one per user turn or topic shift).
Good titles read like a story outline: "Problem Discovery", "Root Cause Analysis", "Design Decision", "Implementation", "Testing".
Keep titles concise and action-oriented. Omit if the session is too short for meaningful chapters.

## Aha Moments Guidelines

Identify **3-5 pivotal moments** where collaborative intelligence emerged.
Less is better - only capture truly impactful moments.

**IMPORTANT**: Human questions and insights are often MORE valuable than AI insights.
When a human asks a thoughtful question that redirects the conversation toward a better outcome,
that's a key moment. Prioritize capturing human contributions when they're insightful.

Types of moments:
- **question**: A question (often from human) that unlocked a better direction
- **insight**: A realization that changed the approach
- **decision**: A key architectural or design decision
- **breakthrough**: Solving a blocking problem
- **synthesis**: Combining ideas into something better

The seq field should match the message sequence number. The role is "user" or "assistant".

## SageOx Insights Guidelines

Identify moments where **SageOx guidance** provided unique value. Look for explicit attributions:
- "Based on SageOx domain guidance..."
- "SageOx's team pattern suggests..."
- "Following SageOx best practices for..."
- "SageOx guidance on [topic] indicates..."

For each insight, capture:
- **seq**: Message number where the insight was applied
- **topic**: Domain area (e.g., "react-patterns", "api-design", "testing")
- **insight**: What guidance was applied
- **impact**: The value it provided (avoided mistakes, saved time, better architecture)

Only include moments where SageOx guidance demonstrably improved the outcome.
If no SageOx attributions are present in the session, leave sageox_insights empty.

## Agent Summary Guidelines

Extract structured data for AI agents to consume. This enables downstream automation
and cross-session knowledge aggregation.

- **decisions**: Architectural or design decisions made during the session. Include rationale.
- **action_items**: Tasks identified but not completed. Include priority when apparent.
- **open_questions**: Unresolved questions that need follow-up. Include why they matter.
- **technical_context**: Technologies, architecture patterns, and integrations discussed.
- **constraints**: Technical or business constraints that shaped decisions.
- **non_goals**: Things explicitly decided NOT to do or out of scope.

Only include fields with actual content. Omit empty arrays.

## Quality Score Guidelines

Rate the session's value to the team on a 0.0-1.0 scale. This determines whether the session
is shared with the team (uploaded to ledger) or kept locally/discarded.

**Score high (0.7-1.0):**
- Architectural decisions or design rationale documented
- Bugs found with root cause analysis
- Reusable patterns or approaches discovered
- Knowledge that would save a future coworker time

**Score medium (0.3-0.7):**
- Routine feature implementation with some decisions
- Bug fixes without broader insights
- Configuration or setup with team-relevant details

**Score low (0.0-0.3):**
- Routine maintenance (version bumps, formatting, rebasing)
- Abandoned sessions (started, backed out, no real work)
- Boilerplate-only (just ran prime, asked one question, left)
- Repetitive work already captured in a prior session

The score_reason should be a single sentence explaining the rating.
`

// BuildSummaryPrompt builds a prompt for the calling agent to generate a session summary.
// The agent receives this prompt in the JSON output and produces the summary itself,
// avoiding a server-side API call.
// If ledgerSessionDir is non-empty, a step is added instructing the agent to push the
// summary to the ledger via `ox session push-summary`.
//
// The prompt describes both possible on-disk shapes the agent may encounter:
//   - raw.jsonl: every entry captured during the session (user, assistant, tool,
//     system, header).
//   - summary-input jsonl (produced by pkg/tokenopt in ConversationOnly mode):
//     user + assistant + header kept verbatim, tool entries collapsed to
//     `{type:"tool_mark", tool_name, brief}`, system entries dropped. May have
//     fewer lines than len(entries).
//
// Callers pick the path; the prompt stays agnostic.
//
// # Contract mismatch with ValidateSummaryContent (historical note)
//
// The prompt below REQUESTS a rich schema: title, summary, key_actions,
// aha_moments, sageox_insights, diagrams, chapter_titles, agent_summary,
// quality_score. Originally the validator in validate.go only REQUIRED the
// first three plus outcome, so agents could ship minimal summaries that
// passed validation but lacked the fields that make session recordings
// useful to coworkers. ValidateSummaryRichness now enforces key_actions
// and aha_moments on non-trivial sessions (entry_count > 20) — output-
// token cost is negligible compared to input-token cost already paid to
// ingest the session.
func BuildSummaryPrompt(entries []Entry, rawPath, ledgerSessionDir string) string {
	var sb strings.Builder

	sb.WriteString("# Summarize Session\n\n")
	sb.WriteString("Analyze the following session and generate a summary JSON object.\n\n")

	// shared guidelines
	sb.WriteString(SummaryPromptGuidelines)
	sb.WriteString("\n")

	// reference the session file on disk instead of embedding all entries
	sb.WriteString("## Session to Analyze\n\n")
	fmt.Fprintf(&sb, "Read the session recording at: `%s`\n\n", rawPath)
	sb.WriteString("The file is JSONL format — one JSON object per line. Possible entry shapes:\n\n")
	sb.WriteString("- `{type:\"header\", metadata:{...}}` — session metadata, one per file\n")
	sb.WriteString("- `{type:\"user\", content:\"...\", timestamp:\"...\"}` — human input\n")
	sb.WriteString("- `{type:\"assistant\", content:\"...\", timestamp:\"...\"}` — AI response prose\n")
	sb.WriteString("- `{type:\"tool\", tool_name:\"...\", tool_input:\"...\", tool_output:\"...\"}` — full tool invocation (only in unoptimized files)\n")
	sb.WriteString("- `{type:\"tool_mark\", tool_name:\"...\", brief:\"...\"}` — compact tool marker (in summary-input files: carries the tool name + a short gist of what was invoked; full tool I/O is not preserved)\n")
	sb.WriteString("- `{type:\"system\", content:\"...\"}` — system reminder (may be absent in summary-input files; they are dropped by the pre-summarize pass)\n\n")
	sb.WriteString("Focus on user/assistant dialog to recover the session narrative; use tool and tool_mark entries as scaffolding for what the agent did between messages. Line count may be smaller than the original entry_count if the file has been pre-processed.\n\n")

	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Read the session recording file at the path above\n")
	sb.WriteString("2. Identify the main goal and what was accomplished\n")
	sb.WriteString("3. Find the pivotal aha moments (questions, insights, decisions)\n")
	sb.WriteString("4. Generate the JSON with all required fields from the Output Format above\n")
	sb.WriteString("5. Save the summary JSON to a temporary file (e.g., `/tmp/ox-summary.json` or `.ox-summary.json` in the workspace root)\n")

	// if ledger session dir is available, add push instruction
	if ledgerSessionDir != "" {
		fmt.Fprintf(&sb, "6. Push summary to ledger by running: `ox session push-summary --file <path-to-summary-file> --session-dir %s`\n", ledgerSessionDir)
		sb.WriteString("   Replace `<path-to-summary-file>` with the actual path where you saved the summary in step 5\n")
	}

	// Suppress unused-param warning for `entries` without changing the API;
	// counting is no longer injected into the prompt because summary-input
	// files have fewer entries than len(entries) and stating a wrong number
	// misleads the summarizer.
	_ = entries

	return sb.String()
}
