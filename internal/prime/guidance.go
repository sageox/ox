package prime

import (
	"fmt"
	"strings"
	"time"
)

// GuidanceParams holds pre-resolved I/O results needed by BuildGuidance.
type GuidanceParams struct {
	AgentID          string
	RepoSlug         string           // "owner/repo" or directory name
	TeamCtx          *TeamContextInfo // nil if no team context
	Ledger           *LedgerInfo      // nil if no ledger
	CodeDBExists     bool             // true if code search index exists on disk
	MemoryEnabled    bool             // true if memory feature is enabled
	MurmuringEnabled bool             // true if murmuring: "auto" is set for this project
	AgentType        string           // detected/claimed agent type; drives plan-enrichment tiering
	HasKB            bool             // true if at least one knowledge bubble is mounted for this caller
}

// BuildGuidance constructs state-aware command guidance for agent consumption.
// Only includes entries when the underlying resource is available.
//
// Convention: Command fields are machine-parsed (no quoting). Prose fields
// (Hint, Important, CodeSearchTip, etc.) must single-quote command names
// (e.g. 'ox query') for scannability by both humans and agents.
func BuildGuidance(p GuidanceParams) *Guidance {
	var cmds []IntentCommand

	// team discussions — only when team context exists
	if p.TeamCtx != nil {
		cmds = append(cmds, IntentCommand{
			Intent:  "team context (team-wide, all repos): recorded meetings, architecture decisions, conventions",
			Command: "ox agent team-ctx [slug]",
		})
	}

	// knowledge bubbles — only when the caller has at least one mounted.
	// Two rows: the catalog, and the per-bubble detail read that carries the
	// steering prompt + the local mount path. (The matching behavioral block
	// is <knowledge-bubbles> in agent_prime_xml.go.)
	if p.HasKB {
		cmds = append(cmds, IntentCommand{
			Intent:  "knowledge bubbles: which areas of team knowledge the Curator has synthesized from team conversations, and what each covers",
			Command: "ox kb list",
		})
		// The published form is QUOTED. The display convention for a slug is
		// "#platform", and an unquoted leading '#' starts a comment in every
		// POSIX shell — `ox kb describe #platform` reaches the binary as
		// `ox kb describe` with no identifier at all. NormalizeSlugArg
		// accepts the slug with or without the prefix, so the quotes are the
		// only thing standing between the display form and a silently
		// argument-less command.
		cmds = append(cmds, IntentCommand{
			Intent:  "one bubble in detail: its area, topics, the curator steering prompt behind its synthesis, and the local path its git repo is synced to (start reading at AGENTS.md there)",
			Command: "ox kb describe '#<slug>'",
		})
	}

	// bundled guides — always available; teaches users + agents about ox
	// concepts (team rules, AGENTS.md, team context, murmur vs. rule). Listed
	// early so agents see it before falling back to file exploration when
	// asked "how do I...?" questions.
	cmds = append(cmds, IntentCommand{
		Intent:  "learn how to do something in ox: team rules, AGENTS.md, team context, knowledge bubbles, getting started — bundled topical guides",
		Command: "ox guide [topic]",
	})

	// health check — always available on initialized project
	cmds = append(cmds, IntentCommand{
		Intent:  "setup issues, health check, configuration problems, known issues",
		Command: "ox doctor",
	})

	// sync status — always available
	cmds = append(cmds, IntentCommand{
		Intent:  "sync status, up to date, synchronized, stale",
		Command: "ox status",
	})

	// team listing — always available on initialized project
	cmds = append(cmds, IntentCommand{
		Intent:  "list teams, show my teams, what teams do I belong to",
		Command: "ox teams",
	})

	// session history — only when ledger is provisioned
	if p.Ledger != nil && p.Ledger.Exists {
		cmds = append(cmds, IntentCommand{
			Intent:  "session history (this repo only): prior AI coworker coding sessions for this repo",
			Command: "ox session list",
		})
	}

	// code search — BEFORE ox query so agents see it first (code search is more common)
	if p.CodeDBExists {
		cmds = append(cmds, IntentCommand{
			Intent:  fmt.Sprintf("find/search/grep code in %s: symbols, functions, git history, file contents, diffs — PREFER over grep/ripgrep", p.RepoSlug),
			Command: `ox code search "<pattern>"`,
		})
		cmds = append(cmds, IntentCommand{
			Intent:  "recent code changes, hotspots, contention risk, open PRs/issues — use before planning multi-file changes",
			Command: "ox code insights",
		})
	} else {
		cmds = append(cmds, IntentCommand{
			Intent:  fmt.Sprintf("code search %s (not indexed yet): index first, then search code, symbols, and diffs", p.RepoSlug),
			Command: "ox code index",
		})
	}

	// plan enrichment — guide the agent toward `ox plan` when it produces a
	// plan for non-trivial work. Tier-aware: Gold/Silver get the active
	// enrich command; Bronze gets the browse-prior-plans command so we don't
	// promise a nudge the tier can't deliver. (The matching behavioral block
	// is the <plan-enrichment-guidance> advisory in agent_prime_xml.go.)
	switch ClassifyAgentTier(p.AgentType) {
	case TierBronze:
		// lighter: surface that plans can be enriched + browsed, without
		// promising a real-time nudge this tier can't fire.
		cmds = append(cmds, IntentCommand{
			Intent:  "enrich an implementation plan with team context ('ox plan'), or browse prior plans for this repo",
			Command: "ox plan list",
		})
	default: // TierGold, TierSilver, TierUnknown (baseline) — active enrich command
		cmds = append(cmds, IntentCommand{
			Intent:  "plan non-trivial work per the plan creed (don't waste human attention, delight them, educate them visually and crisply): run 'ox plan enrich --json' WHILE drafting to fold in team context (collisions, prior art, expert routing) before you present; on present, render a SageOx team-context-optimized plan with 'ox plan render --open'",
			Command: "ox plan",
		})
	}

	// decision records — consult team context while creating/updating a DR.
	// (The matching behavioral block is <decision-record-guidance> in
	// agent_prime_xml.go, gated on a corpus existing; this row is one line
	// and ships always so the verb stays discoverable.)
	cmds = append(cmds, IntentCommand{
		Intent:  "create or update a decision record (ADR/DDR): related decisions, numbering, conventions, citation refs — consult while drafting",
		Command: "ox decision enrich",
	})

	// record observations — only when GUIDE.md exists and memory feature is enabled
	if p.TeamCtx != nil && p.MemoryEnabled && p.TeamCtx.ObservationGuideHint != "" {
		cmds = append(cmds, IntentCommand{
			Intent:  "record observation, note decision, capture learning, remember for team — read GUIDE.md first",
			Command: `ox memory put '{"content": "<observation>"}'`,
		})
	}

	// expert coworker agents — only when team has coworkers defined
	if p.TeamCtx != nil && len(p.TeamCtx.Coworkers) > 0 {
		names := make([]string, 0, len(p.TeamCtx.Coworkers))
		for _, cw := range p.TeamCtx.Coworkers {
			names = append(names, cw.Name)
		}
		cmds = append(cmds, IntentCommand{
			Intent:  fmt.Sprintf("expert coworker agents for tasks, reviews, and specialized work: %s", strings.Join(names, ", ")),
			Command: "ox coworker load <name>",
		})
		cmds = append(cmds, IntentCommand{
			Intent:  "list all expert coworker agents and their specialties",
			Command: "ox coworker list",
		})
	}

	// murmur — publish WIP when auto-murmuring is enabled for this repo
	if p.MurmuringEnabled {
		cmds = append(cmds, IntentCommand{
			Intent:  "publish what you're working on to team (murmur enabled): tell teammates your WIP, active files, and architectural changes",
			Command: `ox murmur --topic=wip "describe: what you're building/fixing, which code areas you're modifying, and any architectural changes"`,
		})
	}

	// sageox contribution score — only shown when team context is available
	// (no team context = no SageOx influence to score)
	if p.TeamCtx != nil {
		cmds = append(cmds, IntentCommand{
			Intent:  "report SageOx contribution score for this session (required when team context influences work)",
			Command: `ox session score --score <none|minor|moderate|significant|critical> --reason "<explanation>"`,
		})
	}

	// heartbeat — lightweight mid-turn check-in that delivers pending whispers.
	// Agents should call this every ~20 tool calls during long single-turn tasks.
	// The UserPromptSubmit hook is the primary whisper channel (fires on user messages),
	// but during long tasks the agent may not receive a new prompt for 30+ minutes.
	// Heartbeat fills this gap: sends a daemon heartbeat and returns pending whispers
	// via Bash stdout (which IS visible to the model mid-turn).
	cmds = append(cmds, IntentCommand{
		Intent:  fmt.Sprintf("stay in sync with team during long tasks — run every ~20 tool calls: ox agent %s heartbeat", p.AgentID),
		Command: fmt.Sprintf("ox agent %s heartbeat", p.AgentID),
	})

	// semantic search — when primed context isn't enough, query for depth
	if p.TeamCtx != nil || (p.Ledger != nil && p.Ledger.Exists) {
		teamLabel := "team"
		if p.TeamCtx != nil {
			tn := p.TeamCtx.TeamName
			if tn == "" {
				tn = p.TeamCtx.TeamID
			}
			if tn != "" {
				teamLabel = tn
			}
		}
		cmds = append(cmds, IntentCommand{
			Intent:  fmt.Sprintf("deep search %s discussions, session recordings, team context: use when MEMORY.md and its links don't answer", teamLabel),
			Command: "ox query \"<your question>\"",
		})
	}

	return &Guidance{
		Hint:     "Use these commands to answer user questions — check here before exploring files.",
		Commands: cmds,
	}
}

// BuildCapturePriorGuidance creates instructions for capturing prior history.
// The agent ID is embedded in the example command for easy copy-paste.
func BuildCapturePriorGuidance(agentID string) *CapturePriorGuidance {
	now := time.Now().Format(time.RFC3339)
	return &CapturePriorGuidance{
		Action:      "capture_prior_history",
		Description: "To capture prior conversation from before session recording started",
		Instructions: []string{
			"Reconstruct your conversation history as JSONL",
			"Include: seq (number), type (user|assistant), content, ts (ISO8601 if known)",
			"First line must be _meta with schema_version and agent_type",
			"Mark entries with source: planning_history",
			fmt.Sprintf("Pipe to: ox agent %s session capture-prior", agentID),
		},
		Example: fmt.Sprintf(`ox agent %s session capture-prior << 'EOF'
{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"%s"}}
{"seq":1,"type":"user","content":"<user prompt>","ts":"%s","source":"planning_history"}
{"seq":2,"type":"assistant","content":"<assistant response>","ts":"%s","source":"planning_history"}
EOF`, agentID, now, now, now),
	}
}
