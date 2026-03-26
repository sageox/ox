package prime

import (
	"fmt"
	"strings"
	"time"
)

// GuidanceParams holds pre-resolved I/O results needed by BuildGuidance.
type GuidanceParams struct {
	AgentID        string
	RepoSlug       string           // "owner/repo" or directory name
	TeamCtx        *TeamContextInfo // nil if no team context
	Ledger         *LedgerInfo      // nil if no ledger
	CodeDBExists   bool             // true if code search index exists on disk
	MemoryEnabled  bool             // true if memory feature is enabled
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
