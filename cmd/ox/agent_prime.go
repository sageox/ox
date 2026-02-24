package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/notification"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/internal/tokens"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/pkg/agentx"
	"github.com/spf13/cobra"
)

// withAttributionGuidance appends config-driven SageOx attribution guidance to content.
// Always-on blocks (not config-gated): real-time insight attribution, plan footer.
// Config-gated blocks (omitted when field is empty): commit attribution, code comments.
// If not logged in, includes a warning about potentially stale team context.
func withAttributionGuidance(content string, loggedIn bool, attr config.ResolvedAttribution) string {
	var sb strings.Builder
	sb.WriteString(content)
	sb.WriteString("\n\n## SageOx Attribution\n")

	// not-logged-in warning (conditional)
	if !loggedIn {
		sb.WriteString("\n**\u26a0\ufe0f Not Logged In:**\n")
		sb.WriteString("Currently not logged in to SageOx, so may not be using your latest team context in making decisions.\n")
		sb.WriteString("When presenting plans or forming commit messages, remind the user of this limitation.\n")
	}

	// real-time insight attribution (always-on)
	sb.WriteString("\n**Real-Time Insight Attribution (Important for session value):**\n")
	sb.WriteString("When SageOx guidance influences your approach, explicitly attribute it in your responses:\n")
	sb.WriteString("- \"Based on SageOx domain guidance, I'll...\"\n")
	sb.WriteString("- \"Team patterns shared through SageOx suggests...\"\n")
	sb.WriteString("- \"Following SageOx best practices for [topic]...\"\n")
	sb.WriteString("- \"SageOx guidance on [domain] indicates...\"\n")
	sb.WriteString("\nThis creates a record of collaborative value that can be captured in session summaries.\n")
	sb.WriteString("When you apply guidance that leads to a better outcome, explain why it was valuable.\n")

	// plan footer (always-on — transparency requirement)
	sb.WriteString("\n**Plan Footer (Required for team-guided/security/architecture plans):**\n")
	sb.WriteString("When presenting a plan for user approval that was informed by this guidance, add the following as the final line:\n\n")
	sb.WriteString("> Guided by SageOx\n>\n")
	sb.WriteString("> \u26a0\ufe0f SageOx is currently for expert skiers only who are comfortable running large swarms of agentic AI agents updating their code.\n")

	// code comments (config-gated: only when commit attribution is enabled)
	if attr.Commit != "" {
		sb.WriteString("\n**Code Comments (Optional):**\n")
		sb.WriteString("For significant team-guided changes, consider adding:\n")
		sb.WriteString("```\n// SageOx-guided: use 'ox' CLI when planning changes\n```\n")
	}

	// commit attribution (config-gated)
	if attr.Commit != "" {
		sb.WriteString("\n**Commit Attribution:**\n")
		sb.WriteString("For commits implementing SageOx-guided plans:\n")
		sb.WriteString("```\n")
		sb.WriteString(attr.Commit)
		sb.WriteString("\n```\n")
	}

	// PR attribution for squash merges (always-on when any attribution is configured)
	if attr.Commit != "" {
		sb.WriteString("\n**PR Attribution (Critical for Squash Merges):**\n")
		sb.WriteString("GitHub squash merges use the PR body as the commit message. To ensure attribution survives:\n")
		sb.WriteString("- Always include the following as the **last line** of every PR body:\n")
		sb.WriteString("```\n")
		sb.WriteString(attr.Commit) // git trailer format, not markdown
		sb.WriteString("\n```\n")
	}

	return sb.String()
}

// buildAttributionTextSection renders a human-readable attribution block for text output,
// only including lines for non-empty config fields.
func buildAttributionTextSection(attr config.ResolvedAttribution) string {
	var sb strings.Builder
	sb.WriteString("## Attribution\n")
	sb.WriteString("When this guidance influences your work:\n")
	if attr.Plan != "" {
		sb.WriteString("- **Plans**: Add footer noting SageOx guidance informed the approach\n")
	}
	if attr.Commit != "" {
		sb.WriteString(fmt.Sprintf("- **Commits**: Add trailer \"%s\"\n", attr.Commit))
	}
	if attr.PR != "" {
		sb.WriteString(fmt.Sprintf("- **PRs**: End body with \"%s\" (survives squash merge)\n", attr.Commit))
	}
	return sb.String()
}

// sessionStatus represents the state of session recording
type sessionStatus struct {
	Recording        bool   `json:"recording"`
	File             string `json:"file,omitempty"`
	Mode             string `json:"mode,omitempty"`              // "infra" or "all"
	Source           string `json:"source,omitempty"`            // "repo", "team", "user", or "default"
	LedgerNeeded     bool   `json:"ledger_needed,omitempty"`     // true if ledger not yet provisioned by cloud
	AutoStarted      bool   `json:"auto_started,omitempty"`      // true if started by ox agent prime
	UserNotification string `json:"user_notification,omitempty"` // message for agent to relay to user
}

// ledgerInfo represents discovered ledger state for prime output
type ledgerInfo struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// capturePriorGuidance provides instructions for capturing prior history
type capturePriorGuidance struct {
	Action       string   `json:"action"`
	Description  string   `json:"description"`
	Instructions []string `json:"instructions"`
	Example      string   `json:"example"`
}

// teamContextInfo represents discovered team context for prime output
type teamContextInfo struct {
	TeamID     string   `json:"team_id"`
	TeamName   string   `json:"team_name,omitempty"`
	IsRepoTeam bool     `json:"is_repo_team"`
	Path       string   `json:"path"`
	Agents     []string `json:"agents,omitempty"`     // discovered agent names
	Escalation string   `json:"escalation,omitempty"` // path to human escalation roster if exists

	// Claude customizations from coworkers/ai/claude/
	ClaudeInstructions *teamClaudeInstructions `json:"claude_instructions,omitempty"`
	ClaudeAgents       []claude.Agent          `json:"claude_agents,omitempty"`
	ClaudeCommands     []claude.Command        `json:"claude_commands,omitempty"`
	AgentsIndexPath    string                  `json:"agents_index_path,omitempty"` // path to agents/index.md if exists

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
}

// teamClaudeInstructions holds paths to team instruction files.
// These files should be read immediately by Claude for team-specific configuration.
type teamClaudeInstructions struct {
	ClaudeMDPath string `json:"claude_md_path,omitempty"` // coworkers/ai/claude/CLAUDE.md
	AgentsMDPath string `json:"agents_md_path,omitempty"` // coworkers/ai/claude/AGENTS.md
	HasClaudeMD  bool   `json:"has_claude_md"`
	HasAgentsMD  bool   `json:"has_agents_md"`
}

// ProjectGuidance represents parsed AGENTS.md content from the project
type ProjectGuidance struct {
	Source  string `json:"source"`           // path where AGENTS.md was found
	Content string `json:"content"`          // raw content of AGENTS.md
	Size    int    `json:"size"`             // byte size of content
	Tokens  int    `json:"tokens,omitempty"` // estimated token count
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

// intentCommand maps a user intent to the ox command that resolves it.
type intentCommand struct {
	Intent  string `json:"intent"`  // natural language phrases the user might say
	Command string `json:"command"` // exact ox CLI command to run
}

// agentGuidance provides a top-level intent-to-command lookup for agents.
// Agents should consult this before exploring files or running ad-hoc discovery.
type agentGuidance struct {
	Hint     string          `json:"hint"`     // one-line instruction for the agent
	Commands []intentCommand `json:"commands"` // ordered by query frequency
}

// agentPrimeOutput is the structured response for agent bootstrap (prime)
type agentPrimeOutput struct {
	Status          string                     `json:"status"` // fresh, unavailable
	AgentID         string                     `json:"agent_id"`
	Guidance        *agentGuidance             `json:"guidance,omitempty"` // intent-to-command lookup (scan first)
	SessionID       string                     `json:"session_id,omitempty"`
	AgentType       string                     `json:"agent_type,omitempty"`     // detected or specified agent type
	AgentSupported  bool                       `json:"agent_supported"`          // true if agent is officially supported
	SupportNotice   string                     `json:"support_notice,omitempty"` // warning for unsupported agents
	Content         string                     `json:"content"`
	Attribution     config.ResolvedAttribution `json:"attribution"`                // commit/PR attribution for ox-guided work
	PlanFooter      string                     `json:"plan_footer,omitempty"`      // exact text for plan footer ("Guided by SageOx")
	ProjectGuidance  *ProjectGuidance           `json:"project_guidance,omitempty"`  // AGENTS.md content if found
	TeamInstructions *TeamInstructions           `json:"team_instructions,omitempty"` // team AGENTS.md/CLAUDE.md content if found
	CapturePrior    *capturePriorGuidance      `json:"capture_prior,omitempty"`    // instructions for capturing prior history
	Message         string                     `json:"message,omitempty"`
	TokenEstimate   int                        `json:"token_estimate,omitempty"` // estimated token count
	ContentLength   int                        `json:"content_length,omitempty"` // raw byte length
	Session         *sessionStatus             `json:"session,omitempty"`        // session recording status
	Ledger          *ledgerInfo                `json:"ledger,omitempty"`         // ledger discovery for team sessions
	TeamContext       *teamContextInfo `json:"team_context,omitempty"`        // team context if configured
	TeamContextStatus string           `json:"team_context_status,omitempty"` // "synced", "syncing", or empty; set when team_context is null but sync is expected
	UserNotification  string           `json:"user_notification,omitempty"`   // pre-built status summary for agent to relay to user
	// Prime call tracking
	PrimeCallCount       int    `json:"prime_call_count,omitempty"`       // number of prime calls this session
	PrimeExcessiveNotice string `json:"prime_excessive_notice,omitempty"` // warning if prime called excessively
	// Doctor agent marker
	NeedsDoctorAgent bool   `json:"needs_doctor_agent,omitempty"` // true if .needs-doctor-agent marker exists
	DoctorHint       string `json:"doctor_hint,omitempty"`        // hint for agent to run ox agent doctor
	// Hook auto-install
	HooksInstalled     bool   `json:"hooks_installed,omitempty"`      // true if hooks were newly installed this prime
	HooksRestartNotice string `json:"hooks_restart_notice,omitempty"` // message for agent to relay to user about restarting
	// Version update advisory
	UpdateAvailable bool   `json:"update_available,omitempty"` // true if newer ox version exists
	LatestVersion   string `json:"latest_version,omitempty"`   // latest available version (without v prefix)
	UpdateHint      string `json:"update_hint,omitempty"`      // human-readable update instruction
}

// agentPrimeCmd registers a new agent instance and starts a session
var agentPrimeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Bootstrap agent session with team context",
	Long: `Bootstrap an AI coding agent session with team context and project configuration.

Returns an agent_id and team context information for the current project.
Agents use this to load team norms, conventions, and architectural decisions.

This command is designed for fail-fast operation - it will never block
coding agents for extended periods.`,
	RunE: runAgentPrime,
}

func initAgentPrimeCmd() {
	// Agent UX Decision: JSON is the default output format.
	//
	// Why: Agents are the primary consumers of ox commands. Text output wastes
	// tokens and requires parsing. JSON is machine-readable by default.
	//
	// --text: For humans who want readable output
	// --review: For security engineers to audit what agents receive (shows both)
	agentPrimeCmd.Flags().Bool("text", false, "Output human-readable text instead of JSON")
	agentPrimeCmd.Flags().Bool("review", false, "Security audit mode: show English summary + JSON")

	// Future optimization: agent/model can be used to tune output for specific agent/model combinations.
	agentPrimeCmd.Flags().String("agent", "", "Agent identifier (claude-code, cursor, droid, windsurf) (default: none)")
	agentPrimeCmd.Flags().String("model", "", "Model identifier (claude-opus-4-5, gpt-4o) (default: none)")
	agentPrimeCmd.Flags().String("agent-ver", "", "Agent version (e.g., 1.0.42) (default: none)")

	// Idempotent mode: skip priming if session already primed (token optimization).
	// Uses marker files in /tmp/<user>/sageox/sessions/{claude_session_id}.json to track primed sessions.
	// When true and marker exists: outputs nothing, exits 0 (saves ~1k tokens).
	// When false (default): always outputs context (safe, may waste tokens on duplicate calls).
	agentPrimeCmd.Flags().Bool("idempotent", false, "Skip priming if session already primed (token optimization)")
}

// runAgentPrime bootstraps a new agent instance with team context.
//
// IMPORTANT: `ox agent prime` is special - it CREATES the agent ID.
// Unlike other agent commands (`ox agent <id> review`),
// prime cannot have an agent ID parameter because the ID doesn't exist yet.
// Do NOT refactor this to require an agent ID - that would break the bootstrap flow.
//
// Idempotent behavior:
// - Reads Claude session_id from stdin (hook JSON context)
// - Uses session markers at /tmp/<user>/sageox/sessions/{session_id}.json
// - With --idempotent: skips priming if marker exists (saves ~1k tokens)
// - Without --idempotent: always outputs but reuses agent_id from marker if exists
//
// LIMITATION: Running `claude "prompt"` executes the prompt BEFORE any hooks fire,
// so ox cannot intercept this invocation. Users must run `claude` without a prompt
// argument to allow the session-start hook to run `ox agent prime` first.
func runAgentPrime(cmd *cobra.Command, args []string) error {
	// gate: require agent context
	if errMsg := agentx.RequireAgent("ox agent prime"); errMsg != "" {
		return fmt.Errorf("%s", errMsg)
	}

	// quick health check - non-blocking if daemon unavailable
	// Note: called here because prime doesn't go through runWithAgentID
	emitDaemonIssueWarnings()

	textMode, _ := cmd.Flags().GetBool("text")
	reviewMode, _ := cmd.Flags().GetBool("review")
	agentType, _ := cmd.Flags().GetString("agent")
	model, _ := cmd.Flags().GetString("model")
	agentVer, _ := cmd.Flags().GetString("agent-ver")
	idempotent, _ := cmd.Flags().GetBool("idempotent")

	// read Claude hook input from stdin (session_id for marker keying)
	// this is non-blocking and returns nil if not in hook context
	hookInput := ReadClaudeHookInput()
	var claudeSessionID string
	if hookInput != nil {
		claudeSessionID = hookInput.SessionID
	}

	// check session marker for idempotent behavior
	var existingMarker *SessionMarker
	if claudeSessionID != "" {
		existingMarker, _ = ReadSessionMarker(claudeSessionID)
		if existingMarker != nil && idempotent {
			// idempotent mode: session already primed, output nothing
			// this saves ~1k tokens on redundant prime calls
			return nil
		}
	}

	// use detected agent as fallback when --agent not provided
	if agentType == "" {
		if agent := agentx.CurrentAgent(); agent != nil {
			agentType = agent.Name()
		}
	}

	// enrich User-Agent for all subsequent API calls in this process
	if agentType != "" {
		useragent.SetAgentType(agentType)
	}
	if agentVer != "" {
		useragent.SetAgentVersion(agentVer)
	} else if agentType != "" {
		// auto-detect agent version as fallback when --agent-ver not provided
		if agent := agentx.CurrentAgent(); agent != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if ver := agent.DetectVersion(ctx, agentx.NewSystemEnvironment()); ver != "" {
				agentVer = ver
				useragent.SetAgentVersion(ver)
			}
		}
	}

	// load attribution from user and project configs
	attribution := loadResolvedAttribution()

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// check if project is initialized (.sageox/ exists)
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		// project not initialized - tell the agent to ask user to run ox init
		output := agentPrimeOutput{
			Status:  "unavailable",
			Message: "Project not initialized. The user needs to run 'ox init' to initialize SageOx in this repository before using agent commands.",
		}
		return outputAgentPrime(cmd, textMode, reviewMode, output)
	}

	// anti-entropy: ensure ox:prime marker exists in AGENTS.md/CLAUDE.md
	// only run on properly initialized projects (config.json exists, not just .sageox/ dir)
	if config.IsInitialized(projectRoot) {
		_, _ = EnsureOxPrimeMarker(projectRoot)
	}

	// anti-entropy: ensure Claude Code hooks are installed
	hooksInstalled := ensureClaudeHooks(projectRoot)

	// get project-specific endpoint (single source of truth)
	projectEndpoint := endpoint.GetForProject(projectRoot)

	// check if user is authenticated
	if auth.IsAuthRequired() {
		authenticated, _ := auth.IsAuthenticatedForEndpoint(projectEndpoint)
		if !authenticated {
			endpointSlug := endpoint.NormalizeSlug(projectEndpoint)
			output := agentPrimeOutput{
				Status:  "unavailable",
				Message: fmt.Sprintf("Authentication required. The user needs to run 'ox login' to authenticate with %s before using agent commands.", endpointSlug),
			}
			return outputAgentPrime(cmd, textMode, reviewMode, output)
		}
	}

	store, err := getInstanceStore(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to initialize instance store: %w", err)
	}

	// determine agent_id: reuse from marker if exists, otherwise generate new
	var agentID string
	if existingMarker != nil && existingMarker.AgentID != "" {
		// reuse agent_id from marker (preserves identity across re-primes)
		agentID = existingMarker.AgentID
	} else {
		// collect existing IDs to avoid collision during generation
		existingInstances, err := store.List()
		if err != nil {
			existingInstances = []*agentinstance.Instance{}
		}
		existingIDs := make([]string, len(existingInstances))
		for i, inst := range existingInstances {
			existingIDs[i] = inst.AgentID
		}

		agentID, err = agentinstance.GenerateAgentID(existingIDs)
		if err != nil {
			return fmt.Errorf("failed to generate agent ID: %w", err)
		}
	}

	// attempt to start session recording if enabled
	sessionStat := startSessionRecording(projectRoot, agentID, agentType)

	// discover team context if configured
	teamCtx := discoverTeamContext(projectRoot)

	// load team instruction files (AGENTS.md / CLAUDE.md from team context root)
	var teamInstructions *TeamInstructions
	if teamCtx != nil {
		teamInstructions = loadTeamInstructions(teamCtx.Path, teamCtx.TeamName)
	}

	// discover ledger for team session guidance (after team context so hint can include discussions path)
	ledgerStatus := discoverLedger(teamCtx)

	// load project guidance from AGENTS.md
	projectGuidance := loadProjectGuidance(projectRoot)

	// build capture-prior guidance with current agent ID
	capturePrior := buildCapturePriorGuidance(agentID)

	// check auth status for attribution warning
	isLoggedIn, _ := auth.IsAuthenticated()

	// check for .needs-doctor-agent marker
	needsDoctorAgent := doctor.NeedsDoctorAgent(projectRoot)
	var doctorHint string
	if needsDoctorAgent {
		doctorHint = "Run 'ox agent doctor' to finalize incomplete sessions"
	}

	// build intent-to-command guidance for agent consumption
	guidance := buildGuidance(teamCtx, ledgerStatus)

	// check for team context notifications using mtime-based approach
	var lastNotified time.Time
	if existingMarker != nil {
		lastNotified = existingMarker.LastNotified
	}
	var teamCtxPath string
	if teamCtx != nil {
		teamCtxPath = teamCtx.Path
	}
	latestMtime, updatedFiles := notification.CheckForUpdates(teamCtxPath, lastNotified)

	// emit notification if there are updates (before main output)
	if len(updatedFiles) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "## SageOx Team Context Updated")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Your SageOx team knowledge has been updated. Please inform the user and re-read these files:")
		fmt.Fprintln(cmd.OutOrStdout())
		for _, f := range updatedFiles {
			relPath := f
			if teamCtxPath != "" {
				if rel, err := filepath.Rel(teamCtxPath, f); err == nil {
					relPath = rel
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", relPath)
		}
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "---")
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// register agent instance locally (bootstrap completes without cloud API)
	serverSessionID := auth.NewServerSessionID()

	inst := &agentinstance.Instance{
		AgentID:         agentID,
		ServerSessionID: serverSessionID,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		AgentType:       agentType,
		AgentVer:        agentVer,
		Model:           model,
		PrimeCallCount:  1,
	}

	if err := store.Add(inst); err != nil {
		return fmt.Errorf("failed to store instance: %w", err)
	}
	trackInstanceStart(inst)

	contentWithAttribution := withAttributionGuidance("", isLoggedIn, attribution)

	output := agentPrimeOutput{
		Status:           "fresh",
		AgentID:          agentID,
		Guidance:         guidance,
		SessionID:        serverSessionID,
		AgentType:        agentType,
		AgentSupported:   isAgentSupported(agentType),
		SupportNotice:    getAgentSupportNotice(agentType),
		Content:          contentWithAttribution,
		TokenEstimate:    tokens.EstimateTokens(contentWithAttribution),
		ContentLength:    len(contentWithAttribution),
		Attribution:      attribution,
		PlanFooter:       config.DefaultPlanFooterAttribution(),
		ProjectGuidance:  projectGuidance,
		TeamInstructions: teamInstructions,
		CapturePrior:     capturePrior,
		Session:          sessionStat,
		Ledger:           ledgerStatus,
		TeamContext:      teamCtx,
		PrimeCallCount:   1,
		NeedsDoctorAgent: needsDoctorAgent,
		DoctorHint:       doctorHint,
		HooksInstalled:   hooksInstalled,
	}

	// set team context status hint for agents when team context hasn't synced yet
	if output.TeamContext == nil {
		// check if we have a team ID configured (team context expected but not yet synced)
		projCfg, _ := config.LoadProjectConfig(projectRoot)
		if projCfg != nil && projCfg.TeamID != "" {
			output.TeamContextStatus = "syncing"
		}
	}

	// build pre-assembled notification for JSON-consuming agents.
	// this duplicates the logic in outputAgentPrimeText so JSON consumers
	// don't have to assemble the notification from individual fields.
	var notifParts []string
	if output.TeamContext != nil {
		teamName := output.TeamContext.TeamName
		if teamName == "" {
			teamName = output.TeamContext.TeamID
		}
		if output.TeamContext.HasAgentContext {
			notifParts = append(notifParts, fmt.Sprintf("Team context: %s (synced, discussions available)", teamName))
		} else {
			notifParts = append(notifParts, fmt.Sprintf("Team context: %s (synced)", teamName))
		}
	} else if output.TeamContextStatus != "" {
		notifParts = append(notifParts, "Team context: "+output.TeamContextStatus)
	}
	if output.Session != nil && output.Session.Recording {
		notifParts = append(notifParts, "Session recording: active")
	} else {
		notifParts = append(notifParts, "Session recording: available (/ox-session-start)")
	}
	if len(notifParts) > 0 {
		output.UserNotification = "SageOx is active on this repo. " + strings.Join(notifParts, ". ") + "."
	}

	if hooksInstalled {
		output.HooksRestartNotice = "SageOx hooks were just installed. Tell the user to exit this session and start a new one so the hooks take effect."
	}

	// check for version updates from daemon cache (pure file read, ~0ms)
	if vResult := checkVersionFromCache(); vResult != nil {
		output.UpdateAvailable = true
		output.LatestVersion = vResult.LatestVersion
		output.UpdateHint = fmt.Sprintf(
			"v%s -> v%s available. Run 'brew upgrade sageox' or visit https://github.com/sageox/ox/releases",
			vResult.CurrentVersion, vResult.LatestVersion,
		)
		// append to user notification
		if output.UserNotification != "" {
			output.UserNotification += " " + output.UpdateHint + "."
		}
	}

	// write session marker for idempotent behavior (graceful failure)
	if claudeSessionID != "" {
		// determine LastNotified: use latestMtime if we have files, else preserve existing
		newLastNotified := latestMtime
		if newLastNotified.IsZero() && existingMarker != nil {
			newLastNotified = existingMarker.LastNotified
		}

		marker := &SessionMarker{
			AgentID:         agentID,
			SessionID:       serverSessionID,
			ClaudeSessionID: claudeSessionID,
			PrimedAt:        time.Now(),
			LastNotified:    newLastNotified,
		}
		if err := WriteSessionMarker(marker); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write session marker: %v\n", err)
		}

		envVars := map[string]string{
			"SAGEOX_AGENT_ID":   agentID,
			"SAGEOX_SESSION_ID": serverSessionID,
		}
		if agentType != "" {
			envVars["AGENT_ENV"] = agentType
		}
		if agentVer != "" {
			envVars["AGENT_VERSION"] = agentVer
		}
		_ = WriteToClaudeEnvFile(envVars)
	}

	err = outputAgentPrime(cmd, textMode, reviewMode, output)

	// Start daemon if not already running.
	// Daemon self-exits via inactivity timeout when heartbeats stop.
	// Runs after output so agent gets its bootstrap response immediately.
	if config.IsInitialized(projectRoot) {
		_ = daemon.EnsureDaemonAttached()
	}

	return err
}

// loadResolvedAttribution loads and merges attribution from user and project configs.
// Project config takes precedence over user config, which takes precedence over defaults.
func loadResolvedAttribution() config.ResolvedAttribution {
	// load user config (ignore errors, use defaults)
	userCfg, _ := config.LoadUserConfig("")
	var userAttr *config.Attribution
	if userCfg != nil {
		userAttr = userCfg.Attribution
	}

	// load project config (ignore errors, use defaults)
	projectCfg, _, _ := config.GetProjectContext()
	var projectAttr *config.Attribution
	if projectCfg != nil {
		projectAttr = projectCfg.Attribution
	}

	return config.MergeAttribution(projectAttr, userAttr)
}

// loadProjectGuidance loads AGENTS.md from the project root or .sageox/ directory.
// Returns nil if no AGENTS.md is found (not an error - it's optional).
func loadProjectGuidance(projectRoot string) *ProjectGuidance {
	if projectRoot == "" {
		return nil
	}

	// search paths in priority order
	paths := []string{
		filepath.Join(projectRoot, "AGENTS.md"),
		filepath.Join(projectRoot, ".sageox", "AGENTS.md"),
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue // file not found or not readable
		}

		content := string(data)
		return &ProjectGuidance{
			Source:  p,
			Content: content,
			Size:    len(data),
			Tokens:  tokens.EstimateTokens(content),
		}
	}

	return nil // no AGENTS.md found (not an error)
}

// isAutoGeneratedAgentsMD returns true if the content is the auto-generated
// boilerplate from CreateAgentsMD(), not team-authored instructions.
func isAutoGeneratedAgentsMD(content string) bool {
	return strings.Contains(content, "*Generated by SageOx CLI")
}

// loadTeamInstructions loads AGENTS.md and/or CLAUDE.md from the team context root.
// Returns nil if no instruction files exist (or AGENTS.md is only auto-generated boilerplate).
func loadTeamInstructions(teamCtxPath, teamName string) *TeamInstructions {
	if teamCtxPath == "" {
		return nil
	}

	var parts []string
	var files []string

	// check AGENTS.md (skip auto-generated boilerplate)
	agentsPath := filepath.Join(teamCtxPath, "AGENTS.md")
	if data, err := os.ReadFile(agentsPath); err == nil {
		content := string(data)
		if !isAutoGeneratedAgentsMD(content) {
			parts = append(parts, content)
			files = append(files, "AGENTS.md")
		}
	}

	// check CLAUDE.md (no auto-generated version exists)
	claudePath := filepath.Join(teamCtxPath, "CLAUDE.md")
	if data, err := os.ReadFile(claudePath); err == nil {
		parts = append(parts, string(data))
		files = append(files, "CLAUDE.md")
	}

	if len(parts) == 0 {
		return nil
	}

	combined := strings.Join(parts, "\n\n---\n\n")
	source := strings.Join(files, " + ")

	return &TeamInstructions{
		Source:   source,
		Content:  combined,
		TeamName: teamName,
		Size:     len(combined),
		Tokens:   tokens.EstimateTokens(combined),
		Files:    files,
	}
}

// buildCapturePriorGuidance creates instructions for capturing prior history.
// The agent ID is embedded in the example command for easy copy-paste.
func buildCapturePriorGuidance(agentID string) *capturePriorGuidance {
	return &capturePriorGuidance{
		Action:      "capture_prior_history",
		Description: "To capture planning discussion from before session start",
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
EOF`, agentID, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339)),
	}
}

// buildGuidance constructs state-aware command guidance for agent consumption.
// Only includes entries when the underlying resource is available.
func buildGuidance(teamCtx *teamContextInfo, ledger *ledgerInfo) *agentGuidance {
	var cmds []intentCommand

	// team discussions — only when team context exists
	if teamCtx != nil {
		cmds = append(cmds, intentCommand{
			Intent:  "team discussions, architecture decisions, conventions, what to implement next",
			Command: "ox agent team-ctx",
		})
	}

	// health check — always available on initialized project
	cmds = append(cmds, intentCommand{
		Intent:  "setup issues, health check, configuration problems, known issues",
		Command: "ox doctor",
	})

	// sync status — always available
	cmds = append(cmds, intentCommand{
		Intent:  "sync status, up to date, synchronized, stale",
		Command: "ox status",
	})

	// session history — only when ledger is provisioned
	if ledger != nil && ledger.Exists {
		cmds = append(cmds, intentCommand{
			Intent:  "session history, prior sessions, what was worked on before",
			Command: "ox session list",
		})
	}

	return &agentGuidance{
		Hint:     "Use these commands to answer user questions — check here before exploring files.",
		Commands: cmds,
	}
}

// startSessionRecording attempts to start session recording if enabled.
// Returns the session status for inclusion in prime output.
// Errors are logged but not fatal - session recording is optional.
func startSessionRecording(projectRoot, agentID, agentType string) *sessionStatus {
	// resolve session mode from config hierarchy
	resolved := config.ResolveSessionRecording(projectRoot)

	// only auto-start recording when config is explicitly set to "auto"
	// "manual" mode requires the user to run `ox session start` themselves
	// "disabled" mode means no recording at all
	if !resolved.IsAuto() {
		return nil
	}

	// check if ledger is provisioned and cloned (required for session storage)
	if !ledger.Exists("") {
		// return status indicating ledger needed
		return &sessionStatus{
			Recording:    false,
			Mode:         resolved.Mode,
			Source:       string(resolved.Source),
			LedgerNeeded: true,
		}
	}

	// check if already recording
	if session.IsRecording(projectRoot) {
		state, err := session.LoadRecordingState(projectRoot)
		if err == nil && state != nil {
			return &sessionStatus{
				Recording: true,
				File:      state.SessionFile,
				Mode:      state.FilterMode,
				Source:    string(resolved.Source),
			}
		}
		return &sessionStatus{
			Recording: true,
			Mode:      resolved.Mode,
			Source:    string(resolved.Source),
		}
	}

	// detect agent adapter for session file location
	var sessionFile string
	if agent := agentx.CurrentAgent(); agent != nil {
		// try to find agent session file based on agent type
		sessionFile = detectAgentSessionFile(agent)
	}

	// generate session file path
	timestamp := time.Now().Format("2006-01-02-150405")
	outputFile := filepath.Join(projectRoot, ".sageox", "sessions", fmt.Sprintf("%s-%s.md", timestamp, agentID))

	// start recording with filter mode
	opts := session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: agentType,
		SessionFile: sessionFile,
		OutputFile:  outputFile,
		FilterMode:  resolved.Mode,
	}

	state, err := session.StartRecording(projectRoot, opts)
	if err != nil {
		// already recording is not an error
		if errors.Is(err, session.ErrAlreadyRecording) {
			existingState, _ := session.LoadRecordingState(projectRoot)
			if existingState != nil {
				return &sessionStatus{
					Recording: true,
					File:      existingState.SessionFile,
					Mode:      existingState.FilterMode,
					Source:    string(resolved.Source),
				}
			}
			return &sessionStatus{
				Recording: true,
				Mode:      resolved.Mode,
				Source:    string(resolved.Source),
			}
		}
		// non-fatal but visible — agent sees stderr and can surface it
		fmt.Fprintf(os.Stderr, "warning: session recording failed to start: %v\n", err)
		return nil
	}

	// build user notification message
	notificationMsg := "Recording session. Discussions may be shared with your team. Run /ox-session-stop to end recording."
	if resolved.IsAuto() {
		notificationMsg += " (Tip: Disable auto-start with 'ox config set session_recording manual')"
	}

	return &sessionStatus{
		Recording:        true,
		File:             state.SessionFile,
		Mode:             resolved.Mode,
		Source:           string(resolved.Source),
		AutoStarted:      true,
		UserNotification: notificationMsg,
	}
}

// detectAgentSessionFile attempts to find the session file for the current agent.
// Returns empty string if not found or agent doesn't support session files.
func detectAgentSessionFile(agent agentx.Agent) string {
	// claude-code stores sessions in ~/.claude/projects/<hash>/session.jsonl
	if agent.Type() == agentx.AgentTypeClaudeCode {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		// the exact project hash is not easily determinable here
		// return the base path for now
		return filepath.Join(home, ".claude", "projects")
	}
	return ""
}

// outputAgentPrime emits bootstrap output based on the selected output mode.
//
// OUTPUT MODE DECISION:
// JSON is the default because agents are the primary consumers of ox commands.
// Text output wastes tokens and requires parsing. JSON is machine-readable by default.
//
// Flag precedence: --review > --text > JSON (default)
//
// --review: Security audit mode for humans to inspect what agents receive.
//
//	Shows both human-readable summary AND the full JSON payload.
//	Useful for security engineers auditing agent context.
//
// --text: Human-readable output for debugging or manual inspection.
//
//	Retains the original hybrid format with markdown.
//
// Default (no flags): Pure JSON output optimized for agent consumption.
func outputAgentPrime(cmd *cobra.Command, textMode, reviewMode bool, output agentPrimeOutput) error {
	// --review takes precedence: show both human summary and JSON
	if reviewMode {
		humanSummary := buildHumanSummary(output)
		prettyJSON, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}

		// build markdown with JSON code block for colorized output
		md := fmt.Sprintf("## Human Summary\n\n%s\n\n## Machine Output\n\n```json\n%s\n```\n",
			humanSummary,
			string(prettyJSON),
		)
		fmt.Fprint(cmd.OutOrStdout(), ui.RenderMarkdown(md))
		return nil
	}

	// --text: human-readable output only
	if textMode {
		return outputAgentPrimeText(cmd, output)
	}

	// default: JSON output for agent consumption
	// This is the primary use case - agents consume JSON directly without parsing overhead
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// buildHumanSummary creates a human-readable summary for --review mode
func buildHumanSummary(output agentPrimeOutput) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "- **Agent ID:** %s\n", output.AgentID)
	if output.SessionID != "" {
		fmt.Fprintf(&sb, "- **Session ID:** %s\n", output.SessionID)
	}
	if output.AgentType != "" {
		supportStatus := "supported"
		if !output.AgentSupported {
			supportStatus = "not officially supported"
		}
		fmt.Fprintf(&sb, "- **Agent Type:** %s (%s)\n", output.AgentType, supportStatus)
	}
	fmt.Fprintf(&sb, "- **Status:** %s\n", output.Status)

	if output.SupportNotice != "" {
		fmt.Fprintf(&sb, "- **Support Notice:** %s\n", output.SupportNotice)
	}

	if output.Message != "" {
		fmt.Fprintf(&sb, "- **Message:** %s\n", output.Message)
	}

	if output.TokenEstimate > 0 {
		fmt.Fprintf(&sb, "- **Token Estimate:** %d\n", output.TokenEstimate)
	}

	if output.Guidance != nil {
		fmt.Fprintf(&sb, "- **Guidance:** %d intent-to-command mappings\n", len(output.Guidance.Commands))
	}

	if output.ProjectGuidance != nil {
		fmt.Fprintf(&sb, "- **Project Guidance:** AGENTS.md found (%d bytes, ~%d tokens)\n",
			output.ProjectGuidance.Size, output.ProjectGuidance.Tokens)
	}

	if output.CapturePrior != nil {
		sb.WriteString("- **Capture Prior:** Instructions available for retroactive history capture\n")
	}

	if output.Session != nil {
		if output.Session.Recording {
			fmt.Fprintf(&sb, "- **Session:** Recording (mode: %s)\n", output.Session.Mode)
		} else if output.Session.LedgerNeeded {
			sb.WriteString("- **Session:** Not recording (ledger not provisioned)\n")
		}
	}

	if output.TeamContext != nil {
		fmt.Fprintf(&sb, "- **Team Context:** %s\n", output.TeamContext.TeamID)
		if ci := output.TeamContext.ClaudeInstructions; ci != nil && ci.HasAgentsMD {
			fmt.Fprintf(&sb, "- **Team AGENTS.md:** %s\n", shortenPath(ci.AgentsMDPath))
		}
	}

	if output.PrimeCallCount > 0 {
		fmt.Fprintf(&sb, "- **Prime Call Count:** %d\n", output.PrimeCallCount)
	}

	if output.PrimeExcessiveNotice != "" {
		fmt.Fprintf(&sb, "- **Warning:** %s\n", output.PrimeExcessiveNotice)
	}

	if output.Ledger != nil {
		if output.Ledger.Exists {
			fmt.Fprintf(&sb, "- **Ledger:** %s\n", shortenPath(output.Ledger.Path))
		} else {
			sb.WriteString("- **Ledger:** Not provisioned\n")
		}
	}

	if output.NeedsDoctorAgent {
		fmt.Fprintf(&sb, "- **Doctor Attention Needed:** %s\n", output.DoctorHint)
	}

	if output.HooksInstalled {
		fmt.Fprintf(&sb, "- **Hooks Installed:** %s\n", output.HooksRestartNotice)
	}

	if output.UpdateAvailable {
		fmt.Fprintf(&sb, "- **Update Available:** %s\n", output.UpdateHint)
	}

	return sb.String()
}

// outputAgentPrimeText emits bootstrap output in human-readable text format.
func outputAgentPrimeText(cmd *cobra.Command, output agentPrimeOutput) error {
	// human-readable summary
	fmt.Fprintf(cmd.OutOrStdout(), "Agent: %s\n", output.AgentID)
	if output.SessionID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\n", output.SessionID)
	}

	// instruct agent to notify user about SageOx.
	// NOTE: all output from this function is consumed by the AI agent, not shown
	// directly to the human. the agent decides how to relay this to the user.
	// "NOTIFY USER NOW" is a behavioral directive — it tells the agent to surface
	// this information immediately rather than silently absorbing it.
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "**NOTIFY USER NOW:** Tell the user that SageOx is active on this repo. Include these details:")
	fmt.Fprintln(cmd.OutOrStdout())
	// team context status — give agent concrete lines to relay
	if output.TeamContext != nil {
		teamName := output.TeamContext.TeamName
		if teamName == "" {
			teamName = output.TeamContext.TeamID
		}
		if output.TeamContext.HasAgentContext {
			fmt.Fprintf(cmd.OutOrStdout(), "- Team context: %s (synced, discussions available via `ox agent team-ctx`)\n", teamName)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "- Team context: %s (synced)\n", teamName)
		}
	} else if output.TeamContextStatus != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "- Team context: %s\n", output.TeamContextStatus)
	}
	// session recording status
	if output.Session != nil && output.Session.Recording {
		fmt.Fprintf(cmd.OutOrStdout(), "- Session recording: active\n")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "- Session recording: available (`/ox-session-start`)")
	}

	// quick reference: intent-to-command lookup
	if output.Guidance != nil && len(output.Guidance.Commands) > 0 {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "## Quick Reference")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), output.Guidance.Hint)
		fmt.Fprintln(cmd.OutOrStdout())
		for _, ic := range output.Guidance.Commands {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-60s  %s\n", ic.Intent, ic.Command)
		}
	}

	// show version update notice
	if output.UpdateAvailable {
		fmt.Fprintln(cmd.OutOrStdout())
		cli.PrintSuggestionBox(
			"Update Available",
			output.UpdateHint,
			"brew upgrade sageox",
		)
	}

	// show hooks restart notice prominently
	if output.HooksInstalled {
		fmt.Fprintln(cmd.OutOrStdout())
		cli.PrintSuggestionBox(
			"Coding Agent Restart Required",
			output.HooksRestartNotice,
			"",
		)
	}

	// show doctor attention needed warning prominently
	if output.NeedsDoctorAgent {
		fmt.Fprintln(cmd.OutOrStdout())
		cli.PrintSuggestionBox(
			"Doctor Attention Needed",
			output.DoctorHint,
			"ox agent doctor",
		)
	}

	// show agent support notice for unsupported agents
	if output.SupportNotice != "" {
		fmt.Fprintln(cmd.OutOrStdout())
		cli.PrintSuggestionBox(
			"Agent Support Notice",
			output.SupportNotice,
			"",
		)
	}

	// show excessive prime warning
	if output.PrimeExcessiveNotice != "" {
		fmt.Fprintln(cmd.OutOrStdout())
		cli.PrintSuggestionBox(
			"Excessive Prime Calls",
			output.PrimeExcessiveNotice,
			"",
		)
	}

	// session status section
	if output.Session != nil {
		if output.Session.LedgerNeeded {
			// show suggestion box when ledger not provisioned
			cli.PrintSuggestionBox(
				"Ledger Required for Sessions",
				fmt.Sprintf("Session recording is set to %q (from %s) but the ledger has not been provisioned by cloud.\nRun 'ox doctor --fix' to clone repos from cloud.",
					output.Session.Mode, output.Session.Source),
				"ox ledger sync",
			)
		} else if output.Session.Recording {
			fmt.Fprintln(cmd.OutOrStdout())
			modeInfo := ""
			if output.Session.Mode != "" {
				modeInfo = fmt.Sprintf(" (mode: %s", output.Session.Mode)
				if output.Session.Source != "" && output.Session.Source != "default" {
					modeInfo += fmt.Sprintf(", from %s", output.Session.Source)
				}
				modeInfo += ")"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Session: Recording%s\n", modeInfo)
			fmt.Fprintf(cmd.OutOrStdout(), "   Stop: ox agent %s session stop\n", output.AgentID)
			fmt.Fprintln(cmd.OutOrStdout(), "   Change mode: ox config set session_recording <none|infra|all>")
		}
	}

	// ledger / repo session history section
	if output.Ledger != nil {
		fmt.Fprintln(cmd.OutOrStdout())
		if output.Ledger.Exists {
			fmt.Fprintln(cmd.OutOrStdout(), "## Ledger (Repo Session History)")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "The ledger is a repo-specific archive of prior AI coworker coding sessions.")
			fmt.Fprintln(cmd.OutOrStdout(), "It is NOT team context. Do NOT consult the ledger unless explicitly asked")
			fmt.Fprintln(cmd.OutOrStdout(), "to review prior sessions or coding history for this repo.")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "  List sessions:  ox session list")
			fmt.Fprintln(cmd.OutOrStdout(), "  View a session: ox session view <name> --text")
			fmt.Fprintln(cmd.OutOrStdout(), "  (without --text, opens in browser — not suitable for agents)")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Do NOT read ledger files directly (LFS stubs). Always use ox session commands.")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Ledger: not provisioned (sessions unavailable until 'ox doctor --fix' or daemon sync)")
		}
	}

	if output.Status != "fresh" && output.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "\nStatus: %s\n", output.Message)
	}

	if output.Content != "" {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), output.Content)
	}

	// output project guidance if found
	if output.ProjectGuidance != nil {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "---PROJECT_GUIDANCE---")
		fmt.Fprintln(cmd.OutOrStdout(), output.ProjectGuidance.Content)
		fmt.Fprintln(cmd.OutOrStdout(), "---END_PROJECT_GUIDANCE---")
	}

	// output team context if configured
	if output.TeamContext != nil {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "---TEAM_CONTEXT---")
		teamJSON, _ := json.Marshal(output.TeamContext)
		fmt.Fprintln(cmd.OutOrStdout(), string(teamJSON))
		fmt.Fprintln(cmd.OutOrStdout(), "---END_TEAM_CONTEXT---")

		// emit team instructions directly (AGENTS.md / CLAUDE.md from team context root)
		if output.TeamInstructions != nil {
			fmt.Fprintln(cmd.OutOrStdout())
			header := "## Team Instructions"
			if output.TeamInstructions.TeamName != "" {
				header += fmt.Sprintf(" (%s)", output.TeamInstructions.TeamName)
			}
			fmt.Fprintln(cmd.OutOrStdout(), header)
			if len(output.TeamInstructions.Files) > 1 {
				fmt.Fprintf(cmd.OutOrStdout(), "From %s\n", output.TeamInstructions.Source)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), output.TeamInstructions.Content)
		}

		// emit Claude subagents section if any exist
		if len(output.TeamContext.ClaudeAgents) > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "## Claude Subagents")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Your team has specialized Claude subagents with domain expertise.")
			fmt.Fprintln(cmd.OutOrStdout(), "**When the user's task matches a subagent's description, load it first:**")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "  ox coworker agent <name>")
			fmt.Fprintln(cmd.OutOrStdout())

			// reference the index.md catalog if it exists
			if output.TeamContext.AgentsIndexPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Full catalog at `%s`.\n", output.TeamContext.AgentsIndexPath)
				fmt.Fprintln(cmd.OutOrStdout())
			}

			fmt.Fprintln(cmd.OutOrStdout(), "| Subagent | When to Use |")
			fmt.Fprintln(cmd.OutOrStdout(), "|----------|-------------|")
			for _, agent := range output.TeamContext.ClaudeAgents {
				desc := agent.Description
				if desc == "" {
					desc = "(no description)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "| %s | %s |\n", agent.Name, desc)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Loading a subagent outputs its full expertise into your context for the task.")
		}

		// emit team commands table if any exist
		if len(output.TeamContext.ClaudeCommands) > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "## Team Commands")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "| Command | Trigger | Description |")
			fmt.Fprintln(cmd.OutOrStdout(), "|---------|---------|-------------|")
			for _, tcmd := range output.TeamContext.ClaudeCommands {
				desc := tcmd.Description
				if desc == "" {
					desc = "(no description)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "| %s | %s | %s |\n", tcmd.Name, tcmd.Trigger, desc)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Invoke commands via slash prefix (e.g., /deploy).")
		}

		// emit team docs catalog if any indexed docs exist
		if len(output.TeamContext.TeamDocs) > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "## Team Docs (read on demand — not preloaded)")
			fmt.Fprintln(cmd.OutOrStdout())
			for _, doc := range output.TeamContext.TeamDocs {
				title := doc.Title
				if title == "" {
					title = doc.Name
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s — \"%s\"\n", doc.Name, title)
				if doc.When != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    When: %s\n", doc.When)
				}
			}
		}

		// always emit team context guidance — discussions may sync after prime runs
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "**Team context available** — recorded team meetings and discussions")
		fmt.Fprintln(cmd.OutOrStdout(), "(architecture, conventions, product direction).")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "  Read SageOx team discussions:  ox agent team-ctx")
		fmt.Fprintln(cmd.OutOrStdout())
		if !output.TeamContext.HasAgentContext {
			fmt.Fprintln(cmd.OutOrStdout(), "Not yet synced — may appear shortly as the daemon syncs in the background.")
		}
	}

	// output capture-prior instructions
	if output.CapturePrior != nil {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "## Session Capture")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "To capture prior discussion, generate JSONL history and pipe to:")
		fmt.Fprintf(cmd.OutOrStdout(), "  ox agent %s session capture-prior\n", output.AgentID)
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Format: {\"seq\":N,\"type\":\"user|assistant\",\"content\":\"...\",\"ts\":\"ISO8601\",\"source\":\"planning_history\"}")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "---CAPTURE_PRIOR---")
		capturePriorJSON, _ := json.Marshal(output.CapturePrior)
		fmt.Fprintln(cmd.OutOrStdout(), string(capturePriorJSON))
		fmt.Fprintln(cmd.OutOrStdout(), "---END_CAPTURE_PRIOR---")
	}

	// output attribution settings for ox-guided commits/PRs (config-driven)
	if output.Attribution.Commit != "" || output.Attribution.Plan != "" || output.Attribution.PR != "" {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), buildAttributionTextSection(output.Attribution))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "---ATTRIBUTION---")
	attrJSON, _ := json.Marshal(output.Attribution)
	fmt.Fprintln(cmd.OutOrStdout(), string(attrJSON))
	fmt.Fprintln(cmd.OutOrStdout(), "---END_ATTRIBUTION---")

	return nil
}

// supportedAgents lists officially supported coding agents for MVP
// Other agents may work but quality of guidance is not guaranteed
var supportedAgents = map[string]bool{
	"claude-code": true,
}

// isAgentSupported returns true if the agent is officially supported
func isAgentSupported(agentType string) bool {
	if agentType == "" {
		return false // unknown agent is not supported
	}
	return supportedAgents[agentType]
}

// getAgentSupportNotice returns a notice for unsupported agents, or empty string for supported ones
func getAgentSupportNotice(agentType string) string {
	if isAgentSupported(agentType) {
		return ""
	}

	if agentType == "" {
		return "SageOx is explicitly designed for use with Claude Code. It is unknown if this agent will appropriately interpret and effectively apply team context. You should review plans deeply to ensure this agent has produced an insightful plan."
	}

	// get display name from registry (e.g., "cursor" -> "Cursor")
	displayName := agentType
	if agent, ok := agentx.DefaultRegistry.Get(agentx.AgentType(agentType)); ok {
		displayName = agent.Name()
	}

	return fmt.Sprintf("SageOx is explicitly designed for use with Claude Code. It is unknown if %s will appropriately interpret and effectively apply team context. You should review plans deeply to ensure %s has produced an insightful plan.", displayName, displayName)
}

// trackInstanceStart tracks an agent instance start event
func trackInstanceStart(inst *agentinstance.Instance) {
	// track telemetry
	if cliCtx != nil && cliCtx.TelemetryClient != nil {
		cliCtx.TelemetryClient.Track(telemetry.Event{
			Type:           telemetry.EventSessionStart,
			AgentID:        inst.AgentID,
			SessionID:      inst.ServerSessionID,
			AgentType:      inst.AgentType,
			Model:          inst.Model,
			PrimeCallCount: inst.PrimeCallCount,
			Success:        true,
		})
	}

}

// trackPrimeExcessive tracks when prime is called excessively
func trackPrimeExcessive(inst *agentinstance.Instance) {
	if cliCtx != nil && cliCtx.TelemetryClient != nil {
		cliCtx.TelemetryClient.Track(telemetry.Event{
			Type:           telemetry.EventPrimeExcessive,
			AgentID:        inst.AgentID,
			SessionID:      inst.ServerSessionID,
			AgentType:      inst.AgentType,
			Model:          inst.Model,
			PrimeCallCount: inst.PrimeCallCount,
			Success:        true,
		})
	}
}

// discoverTeamContext discovers team context from local config and scans for skills/agents.
// Returns nil if no team context is configured.
func discoverTeamContext(projectRoot string) *teamContextInfo {
	if projectRoot == "" {
		return nil
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return nil
	}

	isRepoTeam := config.IsRepoTeamContext(projectRoot, tc.TeamID)

	info := &teamContextInfo{
		TeamID:      tc.TeamID,
		TeamName:    tc.TeamName,
		IsRepoTeam:  isRepoTeam,
		Path:        tc.Path,
		ReadCommand: "ox agent team-ctx",
	}

	// if team context directory hasn't synced yet, return partial info
	// so agents still see the "team context available" section
	if _, err := os.Stat(tc.Path); os.IsNotExist(err) {
		return info
	}

	// discover agents: capabilities/ai/claude/agents/*.md
	agentsDir := filepath.Join(tc.Path, "capabilities", "ai", "claude", "agents")
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				// strip .md extension for agent name
				name := strings.TrimSuffix(entry.Name(), ".md")
				info.Agents = append(info.Agents, name)
			}
		}
	}

	// check for human escalation roster
	escalationPath := filepath.Join(tc.Path, "capabilities", "team", "index.md")
	if _, err := os.Stat(escalationPath); err == nil {
		info.Escalation = "capabilities/team/index.md"
	}

	// discover Claude customizations from coworkers/ai/claude/
	// this is the new standard path for team Claude configurations
	customizations, err := claude.DiscoverAll(tc.Path)
	if err == nil && customizations != nil && customizations.HasAnyCustomizations() {
		// populate instruction file paths
		if customizations.HasInstructionFiles() {
			info.ClaudeInstructions = &teamClaudeInstructions{
				ClaudeMDPath: customizations.ClaudeMDPath,
				AgentsMDPath: customizations.AgentsMDPath,
				HasClaudeMD:  customizations.HasClaudeMD,
				HasAgentsMD:  customizations.HasAgentsMD,
			}
		}

		// populate discovered agents/commands
		info.ClaudeAgents = customizations.Agents
		info.ClaudeCommands = customizations.Commands

		// populate agents index path if exists
		if customizations.HasAgentsIndex {
			info.AgentsIndexPath = customizations.AgentsIndexPath
		}
	}

	// discover team docs from docs/ directory.
	// Only markdown files are indexed — agents read markdown natively,
	// frontmatter is a markdown convention, and token estimation is
	// trivial for text. Non-markdown assets need entirely different
	// disclosure mechanisms and are out of scope for this catalog.
	if docs, _ := teamdocs.DiscoverDocs(tc.Path); len(docs) > 0 {
		info.TeamDocs = docs
	}

	// check for agent-context/distilled-discussions.md
	agentContextRelPath := filepath.Join("agent-context", "distilled-discussions.md")
	agentContextPath := filepath.Join(tc.Path, agentContextRelPath)
	if content, err := os.ReadFile(agentContextPath); err == nil {
		info.HasAgentContext = true
		info.AgentContextPath = agentContextPath
		info.AgentContextRelPath = agentContextRelPath
		// compute hash for context deduplication
		hash := sha256.Sum256(content)
		info.AgentContextHash = fmt.Sprintf("%x", hash[:4])
	}

	return info
}

// discoverLedger checks whether the ledger exists and returns actionable guidance
// for agents to help users discover prior coding sessions for this repo.
// Reuses getLedgerPath() from doctor_ledger_git.go (same resolution used by session commands).
func discoverLedger(teamCtx *teamContextInfo) *ledgerInfo {
	path := getLedgerPath()
	if path == "" {
		return &ledgerInfo{Exists: false}
	}

	hint := "The ledger is a repo-specific archive of prior AI coworker coding sessions. It is NOT team context. Only consult when explicitly asked to review prior sessions. Use 'ox session list' to browse and 'ox session view <name> --text' to view one. Do not read ledger files directly (LFS stubs)."

	return &ledgerInfo{
		Exists: true,
		Path:   path,
		Hint:   hint,
	}
}

// ensureClaudeHooks auto-installs Claude Code hooks if Claude Code is detected
// and hooks are missing. Returns true if hooks were newly installed.
// Idempotent: merges with existing hooks, preserves non-ox hooks.
// Non-fatal: logs warning to stderr on failure.
func ensureClaudeHooks(projectRoot string) bool {
	if !detectClaudeCode() {
		return false
	}
	if HasProjectClaudeHooks(projectRoot) {
		return false // already installed
	}
	if err := InstallProjectClaudeHooks(projectRoot); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to auto-install Claude Code hooks: %v\n", err)
		return false
	}
	return true
}
