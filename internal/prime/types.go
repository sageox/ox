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
	PRDirective      string `json:"pr_directive,omitempty"`      // exact-literal instruction: end PR bodies with the SageOx-Session trailer
}

// LedgerInfo represents discovered ledger state for prime output.
//
// MIGRATION NOTE: New callers should consume Output.KB (typed entries with
// type=repo). LedgerInfo is the legacy mirror retained for one release while
// downstream consumers migrate; it will be removed once the kb envelope has
// shipped for one full release cycle.
type LedgerInfo struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// KBInfo is one row in Output.KB — the unified per-knowledge-bubble envelope
// emitted by `ox agent prime`. Built from the F3 three-source merger
// (internal/kb.Merger) so kb-API rows, legacy team contexts, and legacy
// ledger rows all flow through the same shape.
//
// Per the kb plan: per-kind rendering in prime stays differentiated (the
// rich `team_context` mirror still carries the full team payload for one
// release). KBInfo itself is intentionally minimal — it identifies the
// bubble, where it lives on disk, the caller's role, and a short hint;
// payloads are emitted in their type-specific siblings until the legacy
// mirrors are removed.
type KBInfo struct {
	KBID       string `json:"kb_id,omitempty"`
	Type       string `json:"type"` // kb_type slug ("personal", "team", "repo", ...)
	Slug       string `json:"slug,omitempty"`
	Name       string `json:"name,omitempty"`
	Path       string `json:"path,omitempty"`        // canonical XDG path (paths.KBDir) or legacy local dir
	ViewerRole string `json:"viewer_role,omitempty"` // "owner", "member", "viewer"
	Tokens     int    `json:"tokens,omitempty"`      // estimated/observed tokens contributed by this bubble
	Legacy     bool   `json:"legacy,omitempty"`      // true for legacy team-context / ledger synthesized rows
	Hint       string `json:"hint,omitempty"`        // short, type-specific guidance for the agent
}

// CapturePriorGuidance provides instructions for capturing prior history
type CapturePriorGuidance struct {
	Action       string   `json:"action"`
	Description  string   `json:"description"`
	Instructions []string `json:"instructions"`
	Example      string   `json:"example"`
}

// TeamContextInfo represents discovered team context for prime output.
//
// MIGRATION NOTE: New callers should consume Output.KB (typed entries with
// type=team). TeamContextInfo is the legacy mirror retained for one release
// while downstream consumers migrate; the full payload (AGENTS.md, team_docs,
// team_rules, soul/team hints, memory) continues to flow here for now because
// KBInfo is the minimal-envelope contract — per-type rich content moves into
// KB entries in a follow-up bead.
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

	// Team rules — modular AI-coworker rules under agents/rules/**/*.md
	// (with coworkers/rules/ as legacy fallback). The team-scope cousin of
	// Claude's .claude/rules/<topic>.md. Filtered by repos:, audience:, and
	// status: at discovery time. visibility: always rules carry their body
	// inlined for prime emission; visibility: indexed rules carry only
	// metadata and are read on demand by the agent.
	TeamRules []teamdocs.TeamRule `json:"team_rules,omitempty"`

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

// Knowledge-bubble source identifiers for ContextBudget.
//
// Add a new constant here when introducing a new knowledge bubble (e.g., a
// per-user knowledge bubble, an organization-level bubble, a role-scoped
// bubble). Tag every emit site that injects content from that bubble with
// the new constant; the rest of the pipeline (heartbeat, daemon, status)
// flows the source through automatically — no schema migration required.
//
// Source names are stable string keys serialized into JSON and surfaced to
// agents in the prime XML <context-budget> block. Use lowercase, no spaces.
const (
	// BudgetSourceSageox: tool overhead. Every word is a SageOx product
	// decision: instructions, commands table, attribution, code-search hint,
	// rule-promotion-guidance, ledger descriptor, session-context framing,
	// immediate-actions, user-notices, catalog headers/footers. SageOx is
	// judged on this bucket and only this bucket.
	BudgetSourceSageox = "sageox"

	// BudgetSourceTeam: team-authored or team-imported content delivered
	// from the team-context repo. AGENTS.md, CLAUDE.md, MEMORY.md, team
	// rules (always-tier bodies and indexed rows), coworker profiles,
	// team commands catalog rows, team docs catalog rows. SageOx delivers
	// it; the team controls its size.
	BudgetSourceTeam = "team"

	// BudgetSourceProject: content the repo (project) authored — the
	// project's own AGENTS.md surfaced via <project-guidance>. Distinct
	// from team content because it lives in the repo, not in team context.
	BudgetSourceProject = "project"

	// BudgetSourceUser: reserved for the upcoming per-user knowledge
	// bubble (a SageOx user's personal context that travels across teams
	// and repos). When the feature lands, tag those emit sites with this
	// constant; no other plumbing changes required.
	BudgetSourceUser = "user"
)

// ContextBudget reports the estimated token cost of prime output split by
// who controls the content. SageOx is judged on what it directly emits
// (BudgetSourceSageox) separately from content authored by teams, projects,
// users, or any future knowledge bubble. A rolled-up single number
// conflates these and unfairly penalizes teams whose context is rich.
//
// Estimates use the same len/4 heuristic as tokens.EstimateTokens — no
// tokenizer dependency at the prime emit site.
//
// The map shape (rather than fixed fields) is intentional: future
// knowledge bubbles add entries by tagging emit sites with a new source
// constant. The IPC, daemon aggregation, and display layers preserve the
// map verbatim, so adding a bubble is a one-line code change.
type ContextBudget struct {
	// BySource maps a source identifier (BudgetSource* constants) to the
	// estimated token count for that source. Sum across all entries
	// equals Total().
	BySource map[string]int `json:"by_source,omitempty"`
}

// Add increments the token count for a given source. Zero or negative
// tokens are ignored. Lazily initializes the map.
func (b *ContextBudget) Add(source string, tokens int) {
	if tokens <= 0 || source == "" {
		return
	}
	if b.BySource == nil {
		b.BySource = make(map[string]int)
	}
	b.BySource[source] += tokens
}

// Get returns the token count for a given source, or 0 if absent.
func (b ContextBudget) Get(source string) int {
	return b.BySource[source]
}

// Total returns the sum of every source's tokens.
func (b ContextBudget) Total() int {
	sum := 0
	for _, v := range b.BySource {
		sum += v
	}
	return sum
}

// OrderedSources returns source names in display order: well-known sources
// first (sageox, team, project, user — the order users intuit most), then
// any other sources alphabetically. Use for consistent output across
// runs as new bubbles are added.
func (b ContextBudget) OrderedSources() []string {
	wellKnown := []string{BudgetSourceSageox, BudgetSourceTeam, BudgetSourceProject, BudgetSourceUser}
	seen := make(map[string]bool, len(b.BySource))
	out := make([]string, 0, len(b.BySource))
	for _, k := range wellKnown {
		if _, ok := b.BySource[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range b.BySource {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	// stable alphabetical order for unknown sources
	for i := 1; i < len(rest); i++ {
		for j := i; j > 0 && rest[j-1] > rest[j]; j-- {
			rest[j-1], rest[j] = rest[j], rest[j-1]
		}
	}
	return append(out, rest...)
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

// CloudMCPSuggestedTools is the canonical list of cloud MCP tool names
// agents should consider when local context surfaces are sparse or
// unavailable. Sourced here so additions don't require touching the
// ephemeral-hint builder.
//
// Order matters: list the highest-leverage tools first — agents that
// trim the list (token budget pressure) keep the most useful entries.
var CloudMCPSuggestedTools = []string{
	"sageox.search_team_context",
	"sageox.search_codebase",
	"sageox.kb_query",
	"sageox.session_history",
}

// EphemeralHint surfaces cloud MCP routing guidance in the prime payload.
// The CloudMCPEndpoint and SuggestedTools fields are emitted
// unconditionally — the cloud MCP server is a valid context source on a
// dev laptop too, it's just not the preferred one. The Recommendation
// and LocalDataSparse fields are emitted only when the runtime can't
// reliably satisfy context queries locally (no persistent disk OR no
// long-lived helper), at which point the calling agent should prefer
// MCP over local caches.
type EphemeralHint struct {
	Active           bool     `json:"active"`
	Reason           string   `json:"reason,omitempty"`             // env var or venue marker that triggered ephemeral mode
	LocalDataSparse  bool     `json:"local_data_sparse,omitempty"`  // true when local caches are missing/partial
	CloudMCPEndpoint string   `json:"cloud_mcp_endpoint,omitempty"` // e.g. https://api.sageox.ai/mcp
	Recommendation   string   `json:"recommendation,omitempty"`     // single-line guidance for the agent
	SuggestedTools   []string `json:"suggested_tools,omitempty"`    // cloud MCP tool names to prefer
}

// Output is the structured response for agent bootstrap (prime)
type Output struct {
	Status           string                     `json:"status"` // fresh, degraded, unavailable
	AgentID          string                     `json:"agent_id"`
	Guidance         *Guidance                  `json:"guidance,omitempty"` // intent-to-command lookup (scan first)
	SessionID        string                     `json:"session_id,omitempty"`
	AgentType        string                     `json:"agent_type,omitempty"`     // detected or specified agent type
	AgentSupported   bool                       `json:"agent_supported"`          // true if agent is officially supported
	SupportNotice    string                     `json:"support_notice,omitempty"` // warning for unsupported agents
	Content          string                     `json:"content"`
	Attribution      config.ResolvedAttribution `json:"attribution"`                 // commit/PR attribution for ox-guided work
	PlanFooter       string                     `json:"plan_footer,omitempty"`       // exact text for plan footer ("Guided by SageOx")
	ProjectGuidance  *ProjectGuidance           `json:"project_guidance,omitempty"`  // AGENTS.md content if found
	TeamInstructions *TeamInstructions          `json:"team_instructions,omitempty"` // team AGENTS.md/CLAUDE.md content if found
	CapturePrior     *CapturePriorGuidance      `json:"capture_prior,omitempty"`     // instructions for capturing prior history
	Message          string                     `json:"message,omitempty"`
	TokenEstimate    int                        `json:"token_estimate,omitempty"` // estimated token count
	ContentLength    int                        `json:"content_length,omitempty"` // raw byte length
	Session          *SessionStatus             `json:"session,omitempty"`        // session recording status
	KB               []KBInfo                   `json:"kb,omitempty"`             // unified knowledge-bubble envelope (kb-API + legacy team-contexts + legacy ledgers, deduped)
	// CurrentKB is the KB the agent is currently operating inside, resolved
	// from the cwd via kb.ResolveCurrentKB. Nil if the cwd is not inside a
	// .sageox/-mapped tree.
	CurrentKB         *KBInfo          `json:"current_kb,omitempty"`
	Ledger            *LedgerInfo      `json:"ledger,omitempty"`              // legacy mirror; new callers should use KB (see LedgerInfo godoc)
	Important         string           `json:"important"`                     // always-present disambiguation of knowledge sources
	TeamContext       *TeamContextInfo `json:"team_context,omitempty"`        // legacy mirror; new callers should use KB (see TeamContextInfo godoc)
	TeamContextStatus string           `json:"team_context_status,omitempty"` // "synced", "syncing", or empty; set when team_context is null but sync is expected
	OtherTeams        *OtherTeams      `json:"other_teams,omitempty"`         // non-primary teams (nil when only 1 team)
	UserNotification  string           `json:"user_notification,omitempty"`   // pre-built status summary for agent to relay to user
	AgentTip          string           `json:"agent_tip,omitempty"`           // contextual tip for the agent itself (not for the user)
	// Prime call tracking
	PrimeCallCount       int    `json:"prime_call_count,omitempty"`       // number of prime calls this session
	PrimeExcessiveNotice string `json:"prime_excessive_notice,omitempty"` // warning if prime called excessively
	// CompactReprime is true when this call qualifies for the compact
	// re-prime delta (see ShouldCompactReprime): a routine re-invocation of
	// `ox agent prime` within the SAME context window the agent already
	// primed in this session. The XML renderer skips the static and
	// slow-changing tiers (instructions, command reference, team knowledge)
	// when true — content the agent already holds from its earlier prime
	// call — and emits only the volatile per-session delta. Always false
	// for the agent's first prime this window or a clear/compact-triggered
	// re-prime (the context window was actually wiped). See bd ox-32f6.
	CompactReprime bool `json:"compact_reprime,omitempty"`
	// Cumulative context stats (from daemon, best-effort).
	// CumulativeContextTokens is the rolled-up total; the per-source
	// split lives in CumulativeContextTokensBySource (keyed by
	// BudgetSource* constants — extensible for future knowledge bubbles).
	CumulativeContextTokens         int64            `json:"cumulative_context_tokens,omitempty"`
	CumulativeContextTokensBySource map[string]int64 `json:"cumulative_context_tokens_by_source,omitempty"`
	CommandCount                    int              `json:"command_count,omitempty"` // number of ox commands that produced context

	// Doctor agent marker
	NeedsDoctorAgent bool   `json:"needs_doctor_agent,omitempty"` // true if .needs-doctor-agent marker exists
	DoctorHint       string `json:"doctor_hint,omitempty"`        // hint for agent to run ox agent doctor
	// Scheduled agent tasks ready for THIS agent to claim. Surfacing them at
	// prime time is the universal delivery channel: every adapter runs `ox agent
	// prime` at session start, so even agents without a per-prompt push hook
	// (the mid-session channel, claude-code/codex) still learn about queued work.
	AgentTasksReady int `json:"agent_tasks_ready,omitempty"`
	// Saved plans with open human review feedback (the PULL discovery path): the
	// count ox agent prime surfaces so ANY new session — not just the authoring
	// coworker the push task targets — learns feedback is waiting to be addressed.
	OpenPlanFeedback int `json:"open_plan_feedback,omitempty"`
	// Observation recording directive (behavioral, not just a tool reference)
	ObservationDirective string `json:"observation_directive,omitempty"` // proactive instruction to record observations via ox memory put
	// Murmur directive (behavioral — set when murmuring: "auto")
	MurmurDirective string `json:"murmur_directive,omitempty"` // proactive instruction to publish WIP status via ox murmur
	// Orchestrator directive (behavioral — set when the session runs under an
	// orchestrator whose runtime changes how ox coordination reaches the agent,
	// e.g. Buzz, where lifecycle hooks don't fire so push-whispers must be pulled)
	OrchestratorDirective string `json:"orchestrator_directive,omitempty"`
	// Current user identity (so agents can distinguish self vs teammate)
	// Multiple aliases because sessions, murmurs, and discussions each use different name forms.
	// This is local-only context (not persisted to ledger), so full name is safe here.
	CurrentUserName    string   `json:"current_user_name,omitempty"`    // privacy-safe display name (e.g., "Ryan S.")
	CurrentUserAliases []string `json:"current_user_aliases,omitempty"` // all name forms the agent might encounter

	// Code search availability
	CodeDBAvailable bool   `json:"code_db_available,omitempty"` // true if code search index exists on disk
	CodeSearchTip   string `json:"code_search_tip,omitempty"`   // guidance on code search availability for this repo
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
	// Ephemeral-mode hint: nil when not in ephemeral mode. When set, agents
	// should prefer the cloud MCP server for context ops since local caches
	// are sparse. See docs/adr/adr-ephemeral-mode.md.
	EphemeralHint *EphemeralHint `json:"ephemeral_hint,omitempty"`
}
