package prime

import (
	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/teamdocs"
)

// SessionStatus represents the state of session recording
type SessionStatus struct {
	Recording        bool   `json:"recording"`
	File             string `json:"file,omitempty"`
	Mode             string `json:"mode,omitempty"`              // "infra" or "all"
	Source           string `json:"source,omitempty"`            // "repo", "team", "user", or "default"
	LedgerNeeded     bool   `json:"ledger_needed,omitempty"`     // true if ledger not yet provisioned by cloud
	AutoStarted      bool   `json:"auto_started,omitempty"`      // true if started by ox agent prime
	UserNotification string `json:"user_notification,omitempty"` // message for agent to relay to user
	SessionURL       string `json:"session_url,omitempty"`       // web URL to view this session recording
}

// LedgerInfo represents discovered ledger state for prime output
type LedgerInfo struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// CapturePriorGuidance provides instructions for capturing prior history
type CapturePriorGuidance struct {
	Action       string   `json:"action"`
	Description  string   `json:"description"`
	Instructions []string `json:"instructions"`
	Example      string   `json:"example"`
}

// TeamContextInfo represents discovered team context for prime output
type TeamContextInfo struct {
	TeamID     string   `json:"team_id"`
	TeamName   string   `json:"team_name,omitempty"`
	IsRepoTeam bool     `json:"is_repo_team"`
	Path       string   `json:"path"`
	Agents     []string `json:"agents,omitempty"`     // discovered agent names
	Escalation string   `json:"escalation,omitempty"` // path to human escalation roster if exists

	// Coworker customizations from coworkers/
	CoworkerInstructions  *TeamCoworkerInstructions `json:"coworker_instructions,omitempty"`
	Coworkers             []claude.Agent            `json:"coworkers,omitempty"`
	CoworkerCommands      []claude.Command          `json:"coworker_commands,omitempty"`
	AgentsIndexPath       string                    `json:"agents_index_path,omitempty"`        // path to agents/index.md if exists
	AgentsAgentsMDContent string                    `json:"agents_agents_md_content,omitempty"` // coworkers/agents/AGENTS.md content (first 200 lines)
	CoworkerHint          string                    `json:"coworker_hint,omitempty"`            // hint for agents about available coworkers

	// Agent context - distilled knowledge for AI agents
	HasAgentContext     bool   `json:"has_agent_context,omitempty"`      // true if agent-context/distilled-discussions.md exists
	AgentContextPath    string `json:"agent_context_path,omitempty"`     // full path to distilled-discussions.md
	AgentContextRelPath string `json:"agent_context_rel_path,omitempty"` // relative path within team context
	AgentContextHash    string `json:"agent_context_hash,omitempty"`     // content hash for deduplication
	ReadCommand         string `json:"read_command,omitempty"`           // command to read team discussions

	// Team docs catalog — progressive disclosure for docs/ files.
	// Listed in prime output so agents know what's available and when to read each doc.
	// Content is NOT inlined — agents read on demand via file path.
	TeamDocs []teamdocs.TeamDoc `json:"team_docs,omitempty"`

	// v4 Team Memory
	MemoryContent        string   `json:"memory_content,omitempty"`         // full MEMORY.md content (always inlined)
	SoulHint             string   `json:"soul_hint,omitempty"`              // path to SOUL.md (reference, not inlined)
	TeamHint             string   `json:"team_hint,omitempty"`              // path to TEAM.md (reference, not inlined)
	MemoryDaily          []string `json:"memory_daily,omitempty"`           // available daily summary files
	MemoryWeekly         []string `json:"memory_weekly,omitempty"`          // available weekly summary files
	MemoryMonthly        []string `json:"memory_monthly,omitempty"`         // available monthly summary files
	ObservationGuideHint string   `json:"observation_guide_hint,omitempty"` // path to memory/GUIDE.md (read when needed)

	// sync health
	Stale      bool   `json:"stale,omitempty"`       // true if last sync exceeds staleness threshold
	StaleSince string `json:"stale_since,omitempty"` // human-readable duration since last sync
}

// OtherTeams lists non-primary team contexts available to the agent.
// Included in prime output so agents know what other teams exist.
// Agents MUST NOT read these unless the user explicitly asks by team name.
type OtherTeams struct {
	Root  string           `json:"root"`  // base directory for all team contexts
	Hint  string           `json:"hint"`  // instruction for agent: only read when asked
	Teams []OtherTeamEntry `json:"teams"` // sorted by content freshness
}

// OtherTeamEntry is a compact reference to a non-primary team context.
type OtherTeamEntry struct {
	Slug string `json:"slug"`          // kebab-case identifier for CLI arg
	Name string `json:"name"`          // display name
	Dir  string `json:"dir"`           // subdirectory under root
	Age  string `json:"age,omitempty"` // content freshness from git log
}

// TeamCoworkerInstructions holds paths to team instruction files.
// These files should be read immediately for team-specific configuration.
type TeamCoworkerInstructions struct {
	ClaudeMDPath string `json:"claude_md_path,omitempty"` // coworkers/CLAUDE.md
	AgentsMDPath string `json:"agents_md_path,omitempty"` // coworkers/AGENTS.md
	HasClaudeMD  bool   `json:"has_claude_md"`
	HasAgentsMD  bool   `json:"has_agents_md"`
}

// ProjectGuidance represents parsed AGENTS.md content from the project
type ProjectGuidance struct {
	Source     string `json:"source"`                // path where AGENTS.md was found
	Content    string `json:"content"`               // raw content of AGENTS.md
	Size       int    `json:"size"`                  // byte size of content
	Tokens     int    `json:"tokens,omitempty"`      // estimated token count
	Skipped    bool   `json:"skipped,omitempty"`     // true if content was not injected
	SkipReason string `json:"skip_reason,omitempty"` // why content was skipped
}

// TeamInstructions represents team-level instruction files (AGENTS.md / CLAUDE.md)
// from the root of the team context repo, emitted directly into agent context.
type TeamInstructions struct {
	Source   string   `json:"source"`              // description of which files contributed
	Content  string   `json:"content"`             // concatenated content of all found files
	TeamName string   `json:"team_name,omitempty"` // team display name
	Size     int      `json:"size"`                // byte size of combined content
	Tokens   int      `json:"tokens,omitempty"`    // estimated token count
	Files    []string `json:"files"`               // which files contributed: ["AGENTS.md", "CLAUDE.md"]
}

// IntentCommand maps a user intent to the ox command that resolves it.
type IntentCommand struct {
	Intent  string `json:"intent"`  // natural language phrases the user might say
	Command string `json:"command"` // exact ox CLI command to run
}

// Guidance provides a top-level intent-to-command lookup for agents.
// Agents should consult this before exploring files or running ad-hoc discovery.
type Guidance struct {
	Hint     string          `json:"hint"`     // one-line instruction for the agent
	Commands []IntentCommand `json:"commands"` // ordered by query frequency
}

// UserNotice is a notice that agents must relay to the user (upgrade, restart, support).
type UserNotice struct {
	Type    string `json:"type"` // "upgrade", "restart", "support"
	Message string `json:"message"`
}

// Output is the structured response for agent bootstrap (prime)
type Output struct {
	Status            string                     `json:"status"` // fresh, degraded, unavailable
	AgentID           string                     `json:"agent_id"`
	Guidance          *Guidance                  `json:"guidance,omitempty"` // intent-to-command lookup (scan first)
	SessionID         string                     `json:"session_id,omitempty"`
	AgentType         string                     `json:"agent_type,omitempty"`     // detected or specified agent type
	AgentSupported    bool                       `json:"agent_supported"`          // true if agent is officially supported
	SupportNotice     string                     `json:"support_notice,omitempty"` // warning for unsupported agents
	Content           string                     `json:"content"`
	Attribution       config.ResolvedAttribution `json:"attribution"`                 // commit/PR attribution for ox-guided work
	PlanFooter        string                     `json:"plan_footer,omitempty"`       // exact text for plan footer ("Guided by SageOx")
	ProjectGuidance   *ProjectGuidance           `json:"project_guidance,omitempty"`  // AGENTS.md content if found
	TeamInstructions  *TeamInstructions          `json:"team_instructions,omitempty"` // team AGENTS.md/CLAUDE.md content if found
	CapturePrior      *CapturePriorGuidance      `json:"capture_prior,omitempty"`     // instructions for capturing prior history
	Message           string                     `json:"message,omitempty"`
	TokenEstimate     int                        `json:"token_estimate,omitempty"`      // estimated token count
	ContentLength     int                        `json:"content_length,omitempty"`      // raw byte length
	Session           *SessionStatus             `json:"session,omitempty"`             // session recording status
	Ledger            *LedgerInfo                `json:"ledger,omitempty"`              // repo-specific archive of coding sessions (NOT team context)
	Important         string                     `json:"important"`                     // always-present disambiguation of knowledge sources
	TeamContext       *TeamContextInfo           `json:"team_context,omitempty"`        // team context if configured
	TeamContextStatus string                     `json:"team_context_status,omitempty"` // "synced", "syncing", or empty; set when team_context is null but sync is expected
	OtherTeams        *OtherTeams                `json:"other_teams,omitempty"`         // non-primary teams (nil when only 1 team)
	UserNotification  string                     `json:"user_notification,omitempty"`   // pre-built status summary for agent to relay to user
	AgentTip          string                     `json:"agent_tip,omitempty"`           // contextual tip for the agent itself (not for the user)
	// Prime call tracking
	PrimeCallCount       int    `json:"prime_call_count,omitempty"`       // number of prime calls this session
	PrimeExcessiveNotice string `json:"prime_excessive_notice,omitempty"` // warning if prime called excessively
	// Cumulative context stats (from daemon, best-effort)
	CumulativeContextTokens int64 `json:"cumulative_context_tokens,omitempty"` // estimated total tokens produced by ox commands
	CommandCount            int   `json:"command_count,omitempty"`             // number of ox commands that produced context
	// Doctor agent marker
	NeedsDoctorAgent bool   `json:"needs_doctor_agent,omitempty"` // true if .needs-doctor-agent marker exists
	DoctorHint       string `json:"doctor_hint,omitempty"`        // hint for agent to run ox agent doctor
	// Observation recording directive (behavioral, not just a tool reference)
	ObservationDirective string `json:"observation_directive,omitempty"` // proactive instruction to record observations via ox memory put
	// Code search availability
	CodeSearchTip string `json:"code_search_tip,omitempty"` // guidance on code search availability for this repo
	// Hook auto-install
	HooksInstalled     bool   `json:"hooks_installed,omitempty"`      // true if hooks were newly installed this prime
	HooksRestartNotice string `json:"hooks_restart_notice,omitempty"` // message for agent to relay to user about restarting
	// Version update advisory
	UpdateAvailable bool   `json:"update_available,omitempty"` // true if newer ox version exists
	LatestVersion   string `json:"latest_version,omitempty"`   // latest available version (without v prefix)
	UpdateHint      string `json:"update_hint,omitempty"`      // human-readable update instruction
	// Structured user notices (XML <user-notices> block)
	UserNotices []UserNotice `json:"user_notices,omitempty"` // notices that agents must relay to the user
	// Per-step timing instrumentation
	ElapsedMs int64            `json:"elapsed_ms,omitempty"` // total prime execution time
	Timing    map[string]int64 `json:"timing,omitempty"`     // per-phase timing (ms)
}
