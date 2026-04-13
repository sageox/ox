package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/internal/tips"
	"github.com/sageox/ox/internal/tokens"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/useragent"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
	"github.com/spf13/cobra"
)

// type aliases for backward compatibility within this package
type agentPrimeOutput = prime.Output
type sessionStatus = prime.SessionStatus
type ledgerInfo = prime.LedgerInfo
type capturePriorGuidance = prime.CapturePriorGuidance
type teamContextInfo = prime.TeamContextInfo
type otherTeams = prime.OtherTeams
type otherTeamEntry = prime.OtherTeamEntry
type teamCoworkerInstructions = prime.TeamCoworkerInstructions
type ProjectGuidance = prime.ProjectGuidance
type TeamInstructions = prime.TeamInstructions
type intentCommand = prime.IntentCommand
type agentGuidance = prime.Guidance
type UserNotice = prime.UserNotice

// withAttributionGuidance delegates to prime.WithAttributionGuidance.
func withAttributionGuidance(content string, loggedIn bool, attr config.ResolvedAttribution) string {
	return prime.WithAttributionGuidance(content, loggedIn, attr)
}

// buildAttributionTextSection delegates to prime.BuildAttributionTextSection.
func buildAttributionTextSection(attr config.ResolvedAttribution) string {
	return prime.BuildAttributionTextSection(attr)
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
	agentPrimeCmd.Flags().String("format", "", "Output format: xml (default) or json")

	// Future optimization: agent/model can be used to tune output for specific agent/model combinations.
	agentPrimeCmd.Flags().String("agent", "", "Agent identifier (claude-code, cursor, droid, windsurf) (default: none)")
	agentPrimeCmd.Flags().String("model", "", "Model identifier (claude-opus-4-5, gpt-4o) (default: none)")
	agentPrimeCmd.Flags().String("agent-ver", "", "Agent version (e.g., 1.0.42) (default: none)")

	// Idempotent mode: skip priming if session already primed (token optimization).
	// Uses marker files in /tmp/<user>/sageox/sessions/{agent_session_id}.json to track primed sessions.
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
// - Reads agent session_id from stdin (hook JSON context)
// - Uses session markers at /tmp/<user>/sageox/sessions/{agent_session_id}.json
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

	primeStart := time.Now()
	timing := make(map[string]int64)

	// quick health check - non-blocking if daemon unavailable
	// Note: called here because prime doesn't go through runWithAgentID
	phaseStart := time.Now()
	emitDaemonIssueWarnings()
	timing["daemon_health"] = time.Since(phaseStart).Milliseconds()

	textMode, _ := cmd.Flags().GetBool("text")
	reviewMode, _ := cmd.Flags().GetBool("review")
	agentType, _ := cmd.Flags().GetString("agent")
	model, _ := cmd.Flags().GetString("model")
	agentVer, _ := cmd.Flags().GetString("agent-ver")
	idempotent, _ := cmd.Flags().GetBool("idempotent")

	// read agent hook input from stdin (session_id for marker keying)
	// this is non-blocking and returns nil if not in hook context
	hookInput := ReadAgentHookInput()
	var agentSessionID string
	if hookInput != nil {
		agentSessionID = hookInput.SessionID
	}

	// fallback: if no session ID from hook stdin, try agent's native env var
	// (e.g., CODEX_THREAD_ID, AMP_THREAD_URL, CLAUDE_CODE_SESSION_ID)
	if agentSessionID == "" {
		if agent := agentx.CurrentAgent(); agent != nil && agent.SupportsSession() {
			agentSessionID = agent.SessionID(agentx.NewSystemEnvironment())
		}
	}

	// check session marker for idempotent behavior
	var existingMarker *SessionMarker
	if agentSessionID != "" {
		existingMarker, _ = ReadSessionMarker(agentSessionID)
		if existingMarker != nil && idempotent {
			// idempotent mode: session already primed, output nothing
			// this saves ~1k tokens on redundant prime calls
			return nil
		}
	}

	// use detected agent as fallback when --agent not provided
	if agentType == "" {
		if agent := agentx.CurrentAgent(); agent != nil {
			agentType = string(agent.Type())
		}
	}
	agentType = canonicalAgentType(agentType)

	// enrich User-Agent for all subsequent API calls in this process
	if agentType != "" {
		useragent.SetAgentType(agentType)
	}
	if agentVer != "" {
		useragent.SetAgentVersion(agentVer)
	} else if agentType != "" {
		// auto-detect agent version: try agentx first, then flexible fallback
		if agent := agentx.CurrentAgent(); agent != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if ver := agent.DetectVersion(ctx, agentx.NewSystemEnvironment()); ver != "" {
				agentVer = ver
				useragent.SetAgentVersion(ver)
			}
		}
		// fallback: flexible regex for CLIs with non-standard version output
		if agentVer == "" {
			if ver := detectAgentVersionFallback(agentType); ver != "" {
				agentVer = ver
				useragent.SetAgentVersion(ver)
			}
		}
	}
	if orchType := agentx.OrchestratorType(); orchType != "" {
		useragent.SetOrchestratorType(orchType)
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

	// generate agentID and start recording BEFORE auth check — recording is local,
	// auth is only needed for upload and cloud features
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
		// fallback: if a hook already started recording for the same agent process,
		// reuse that recording's agent ID so prime stays idempotent.
		// This handles the case where CLAUDE.md's BLOCKING instruction triggers
		// a second prime call after the SessionStart hook already created a session.
		if currentPID := proc.FindAgentAncestorPID(); currentPID > 0 {
			if states, loadErr := session.LoadAllRecordingStates(projectRoot); loadErr == nil {
				for _, s := range states {
					if s.IsAgentAlive() && s.ParentPID > 0 && s.ParentPID == currentPID {
						agentID = s.AgentID
						slog.Debug("prime: reusing agent ID from active recording", "agent_id", agentID, "parent_pid", currentPID)
						break
					}
				}
			}
		}
	}

	// fallback: check SAGEOX_AGENT_ID env and sole-active-recording.
	// Covers: (a) prime called from CLAUDE.md BLOCKING instruction after /clear
	// (CLAUDE_ENV_FILE persists the var), (b) prime subprocess called from hook
	// with the env var passed explicitly by runPrimeForHook.
	if agentID == "" {
		if states, loadErr := session.LoadAllRecordingStates(projectRoot); loadErr == nil {
			envID := os.Getenv("SAGEOX_AGENT_ID")
			agentID = resolveAgentIDFromStates(states, envID)
		}
	}

	if agentID == "" {
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

	// detect parent agent early: if SAGEOX_AGENT_ID is already set, this is a subagent
	// and the existing value identifies the parent (orchestrator inherits env vars).
	// Must happen before startSessionRecording so parent info is recorded in session state.
	parentAgentID := ""
	if existing := os.Getenv("SAGEOX_AGENT_ID"); existing != "" && existing != agentID {
		parentAgentID = existing
	}

	// attempt to start session recording if enabled (local, no auth needed)
	phaseStart = time.Now()
	sessionStat := startSessionRecording(projectRoot, agentID, agentType, parentAgentID)
	timing["session_start"] = time.Since(phaseStart).Milliseconds()

	// check if user is authenticated — degraded mode if not (recording continues locally)
	if auth.IsAuthRequired() {
		authenticated, authErr := auth.IsAuthCredentialValidForEndpoint(projectEndpoint)
		if !authenticated {
			endpointSlug := endpoint.NormalizeSlug(projectEndpoint)

			// write session marker so hooks can discover session file and continue recording
			if agentSessionID != "" {
				marker := &SessionMarker{
					AgentID:        agentID,
					AgentSessionID: agentSessionID,
					PrimedAt:       time.Now(),
					ParentPID:      proc.FindAgentAncestorPID(),
				}
				if writeErr := WriteSessionMarker(marker); writeErr != nil {
					slog.Warn("failed to write session marker in degraded mode", "error", writeErr)
				}
			}

			// distinguish "never logged in" from "token expired/refresh failed"
			msg := "Not logged in."
			if authErr != nil {
				msg = "Authentication expired."
			}
			output := agentPrimeOutput{
				Status:  "degraded",
				AgentID: agentID,
				Session: sessionStat,
				Message: fmt.Sprintf("%s Run 'ox login' to authenticate with %s. Session recording is active locally — data will be uploaded after authentication.", msg, endpointSlug),
			}
			if sessionStat != nil && sessionStat.UserNotification != "" {
				output.UserNotification = sessionStat.UserNotification
			}
			return outputAgentPrime(cmd, textMode, reviewMode, output)
		}
	}

	// discover team context if configured
	phaseStart = time.Now()
	teamCtx := discoverTeamContext(projectRoot)

	// check team context staleness
	checkTeamContextStaleness(teamCtx, projectRoot)

	// load team instruction files (AGENTS.md / CLAUDE.md from team context root)
	var teamInstructions *TeamInstructions
	if teamCtx != nil {
		teamInstructions = loadTeamInstructions(teamCtx.Path, teamCtx.TeamName)
	}
	timing["team_context"] = time.Since(phaseStart).Milliseconds()

	// discover ledger for team session guidance (after team context so hint can include discussions path)
	phaseStart = time.Now()
	ledgerStatus := discoverLedger(teamCtx)

	// load project guidance from AGENTS.md
	projectGuidance := loadProjectGuidance(projectRoot, agentType)

	// build capture-prior guidance with current agent ID
	capturePrior := buildCapturePriorGuidance(agentID)

	// check auth status for attribution warning
	isLoggedIn, _ := auth.IsAuthCredentialValidForEndpoint(projectEndpoint)

	// check for .needs-doctor-agent marker
	needsDoctorAgent := doctor.NeedsDoctorAgent(projectRoot)
	var doctorHint string
	if needsDoctorAgent {
		doctorHint = "Run 'ox agent doctor' to finalize incomplete sessions" // quote command names in prose for scannability
	}

	// build intent-to-command guidance for agent consumption
	guidance := buildGuidance(agentID, projectRoot, teamCtx, ledgerStatus)
	timing["guidance_build"] = time.Since(phaseStart).Milliseconds()

	// register or update agent instance locally (bootstrap completes without cloud API)
	var inst *agentinstance.Instance
	var primeCallCount int

	if existingMarker != nil && existingMarker.AgentID != "" {
		// re-prime: increment existing instance's prime call count.
		// Only fall through to fresh-prime on "not found"; real errors (lock, I/O) are returned.
		updated, isExcessive, err := store.IncrementPrimeCallCount(agentID)
		if err == nil {
			inst = updated
			primeCallCount = updated.PrimeCallCount
			if isExcessive {
				trackPrimeExcessive(updated)
			}
		} else if !errors.Is(err, agentinstance.ErrInstanceNotFound) {
			return fmt.Errorf("failed to update instance: %w", err)
		}
	}

	if inst == nil {
		// fresh prime (or re-prime where instance wasn't found): create new
		serverSessionID := auth.NewServerSessionID()
		inst = &agentinstance.Instance{
			AgentID:         agentID,
			ServerSessionID: serverSessionID,
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(24 * time.Hour),
			AgentType:       agentType,
			AgentVer:        agentVer,
			Model:           model,
			ParentPID:       proc.FindAgentAncestorPID(),
			ParentAgentID:   parentAgentID,
			PrimeCallCount:  1,
		}
		if err := store.Add(inst); err != nil {
			return fmt.Errorf("failed to store instance: %w", err)
		}
		primeCallCount = 1
	}
	trackInstanceStart(inst)

	contentWithAttribution := withAttributionGuidance("", isLoggedIn, attribution)

	output := agentPrimeOutput{
		Status:           "fresh",
		AgentID:          agentID,
		Guidance:         guidance,
		SessionID:        inst.ServerSessionID,
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
		PrimeCallCount:   primeCallCount,
		NeedsDoctorAgent: needsDoctorAgent,
		DoctorHint:       doctorHint,
		HooksInstalled:   hooksInstalled,
	}

	// populate cumulative context stats from daemon (best-effort).
	// read BEFORE sending this command's heartbeat — intentional: these report
	// "consumed so far", not including the prime output about to be emitted.
	// heartbeats are async fire-and-forget, so ordering wouldn't be guaranteed anyway.
	if daemonClient := daemon.TryConnect(); daemonClient != nil {
		if instances, err := daemonClient.Instances(); err == nil {
			for _, di := range instances {
				if di.AgentID == agentID {
					output.CumulativeContextTokens = di.CumulativeContextTokens
					output.CommandCount = di.CommandCount
					break
				}
			}
		}
	}

	// populate excessive prime notice if applicable
	if inst.IsPrimeExcessive() {
		output.PrimeExcessiveNotice = fmt.Sprintf(
			"Prime called %d times (threshold: %d). This may indicate context compaction issues or agent misconfiguration. Each prime injects ~%d bytes into your context window.",
			primeCallCount, agentinstance.ExcessivePrimeThreshold, output.ContentLength)
	}

	// populate session URL if recording
	// for subagents: use parent session URL so PRs/commits link to the main session
	if output.Session != nil && output.Session.Recording {
		if projCfg, cfgErr := config.LoadProjectConfig(projectRoot); cfgErr == nil {
			lookupAgentID := agentID
			if parentAgentID != "" {
				lookupAgentID = parentAgentID
			}
			state, _ := session.LoadRecordingStateForAgent(projectRoot, lookupAgentID)
			if state == nil && parentAgentID != "" {
				// parent session not found; fall back to own session
				state, _ = session.LoadRecordingStateForAgent(projectRoot, agentID)
			}
			if state != nil {
				sessionName := session.GetSessionName(state.SessionPath)
				output.Session.SessionURL = buildSessionURL(projCfg, sessionName)
			}
		}
	}

	// emit provided context-trace events (best-effort, never blocks prime)
	if teamCtx != nil || teamInstructions != nil {
		var traceDir string
		if sessionStat != nil && sessionStat.Recording {
			if recordingState, stateErr := session.LoadRecordingStateForAgent(projectRoot, agentID); stateErr == nil && recordingState != nil {
				traceDir = recordingState.SessionPath
			}
		}
		// fallback: write to .sageox/cache/context-trace/ when not recording
		if traceDir == "" {
			traceDir = filepath.Join(projectRoot, ".sageox", "cache", "context-trace")
			_ = os.MkdirAll(traceDir, 0o755)
		}
		emitProvidedContextTrace(traceDir, teamCtx, teamInstructions)
	}

	// always-present disambiguation of knowledge sources
	// quote command names in prose for scannability
	output.Important = "SageOx has two SEPARATE knowledge sources. " +
		"(1) TEAM CONTEXT: team-wide meetings, architecture decisions, and conventions shared across ALL repos. Read with: 'ox agent team-ctx'. " +
		"(2) SESSIONS/LEDGER: repo-specific archive of prior AI coworker coding sessions for THIS repo only. Browse with: 'ox session list'. " +
		"These are unrelated — sessions are NOT discussions, and the ledger is NOT team context."

	// discover other team contexts (non-primary)
	primaryTeamID := ""
	if teamCtx != nil {
		primaryTeamID = teamCtx.TeamID
	} else if projCfg, _ := config.LoadProjectConfig(projectRoot); projCfg != nil {
		primaryTeamID = projCfg.TeamID
	}
	output.OtherTeams = discoverOtherTeamContexts(projectRoot, primaryTeamID)

	// update Important text when multiple teams are available
	totalTeams := 1 // primary
	if output.OtherTeams != nil {
		totalTeams += len(output.OtherTeams.Teams)
	}
	if totalTeams > 1 {
		output.Important = fmt.Sprintf("SageOx has two SEPARATE knowledge sources. "+
			"(1) TEAM CONTEXT: team-wide meetings, architecture decisions, and conventions shared across ALL repos. "+
			"You have access to %d team contexts. Read with: 'ox agent team-ctx [slug]'. "+
			"(2) SESSIONS/LEDGER: repo-specific archive of prior AI coworker coding sessions for THIS repo only. "+
			"Browse with: 'ox session list'. "+
			"These are unrelated — sessions are NOT discussions, and the ledger is NOT team context.", totalTeams)
	}

	// set team context status hint for agents when team context hasn't synced yet
	if output.TeamContext == nil {
		// check if we have a team ID configured (team context expected but not yet synced)
		projCfg, _ := config.LoadProjectConfig(projectRoot)
		if projCfg != nil && projCfg.TeamID != "" {
			output.TeamContextStatus = "syncing"
		}
	}

	// observation directive — behavioral instruction for agents to proactively record
	if teamCtx != nil && auth.IsMemoryEnabled() && teamCtx.ObservationGuideHint != "" {
		output.ObservationDirective = fmt.Sprintf(
			"Proactively record observations throughout this session using `ox memory put`. "+
				"Record decisions, discoveries, questions, and notable events as they happen — don't wait to be asked. "+
				"Read GUIDE.md first for what to capture: %s", teamCtx.ObservationGuideHint)
	}

	// murmur directive — behavioral instruction when auto-murmuring is configured
	if config.MurmuringEnabled(projectRoot) {
		output.MurmurDirective = "Murmuring is ENABLED. Proactively publish WIP to teammates:\n" +
			"  • At START of significant work — say what you're about to do\n" +
			"  • After architectural decisions — what you decided and why\n" +
			"  • Command: ox murmur --topic=wip \"concise description (≤500 bytes)\"\n" +
			fmt.Sprintf("  • Stay in sync: run `ox agent %s heartbeat` every ~20 tool calls during long tasks\n", agentID) +
			"Run your first murmur NOW: describe what the user asked and which code areas you expect to touch."
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
			notifParts = append(notifParts, fmt.Sprintf("Team context: %s (synced — team-wide meetings/decisions)", teamName))
		} else {
			notifParts = append(notifParts, fmt.Sprintf("Team context: %s (synced — team-wide)", teamName))
		}
	} else if output.TeamContextStatus != "" {
		notifParts = append(notifParts, "Team context: "+output.TeamContextStatus)
	}
	if output.Session != nil && output.Session.Recording {
		notifParts = append(notifParts, "Session recording: active — discussions may be shared with teammates")
	} else {
		notifParts = append(notifParts, "Session recording: available (/ox-session-start)")
	}
	if len(notifParts) > 0 {
		output.UserNotification = "This session is enhanced by team context via SageOx. " + strings.Join(notifParts, ". ") + "."
	}
	if codexNote := codexLifecycleNotification(agentType); codexNote != "" {
		if output.UserNotification != "" {
			output.UserNotification += " " + codexNote
		} else {
			output.UserNotification = codexNote
		}
	}

	if hooksInstalled {
		output.HooksRestartNotice = "SageOx hooks were just installed. Tell the user to exit this session and start a new one so the hooks take effect."
		output.UserNotices = append(output.UserNotices, UserNotice{
			Type:    "restart",
			Message: "SageOx hooks were just installed. Exit this session and start a new one so the hooks take effect.",
		})
	}

	// check for version updates from daemon cache (pure file read, ~0ms)
	if vResult := checkVersionFromCache(); vResult != nil {
		output.UpdateAvailable = true
		output.LatestVersion = vResult.LatestVersion
		output.UpdateHint = fmt.Sprintf(
			"v%s -> v%s available. Run 'ox upgrade' to update.",
			vResult.CurrentVersion, vResult.LatestVersion,
		)
		output.UserNotices = append(output.UserNotices, UserNotice{
			Type:    "upgrade",
			Message: output.UpdateHint,
		})
	}

	// add support notice to user notices if present
	if output.SupportNotice != "" {
		output.UserNotices = append(output.UserNotices, UserNotice{
			Type:    "support",
			Message: output.SupportNotice,
		})
	}

	// write session marker for idempotent behavior (graceful failure)
	if agentSessionID != "" {
		marker := &SessionMarker{
			AgentID:        agentID,
			SessionID:      inst.ServerSessionID,
			AgentSessionID: agentSessionID,
			PrimedAt:       time.Now(),
			ParentPID:      proc.FindAgentAncestorPID(),
		}
		if err := WriteSessionMarker(marker); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write session marker: %v\n", err)
		}
	}

	// always write SAGEOX_AGENT_ID to the env file so /clear in Claude Code
	// picks up the new agent ID rather than inheriting a stale one from the
	// previous session (CLAUDE_ENV_FILE persists across /clear)
	{
		envVars := map[string]string{
			"SAGEOX_AGENT_ID":   agentID,
			"SAGEOX_SESSION_ID": inst.ServerSessionID,
		}
		if agentType != "" {
			envVars["AGENT_ENV"] = agentType
		}
		if agentVer != "" {
			envVars["AGENT_VERSION"] = agentVer
		}
		_ = WriteToAgentEnvFile(envVars)
	}

	// append code search tip to AgentTip based on index availability
	repoSlug := repoSlugFromRemoteOrDir(projectRoot)
	codeDBDir := resolveCodeDBDir(projectRoot)
	if _, statErr := os.Stat(codeDBDir); statErr == nil {
		output.CodeDBAvailable = true
		teamLabel := ""
		if teamCtx != nil {
			tn := teamCtx.TeamName
			if tn == "" {
				tn = teamCtx.TeamID
			}
			teamLabel = tn + " "
		}
		output.CodeSearchTip = fmt.Sprintf("'ox code search' is available for %s, PREFER over grep for code search (it indexes symbols, git history, diffs). Use 'ox query' only for %steam context (discussions) and ledger session recordings.", repoSlug, teamLabel)
	} else {
		output.CodeSearchTip = fmt.Sprintf("Run 'ox code index' to enable local code search (symbols, diffs, git history) for %s.", repoSlug)
	}

	output.ElapsedMs = time.Since(primeStart).Milliseconds()
	output.Timing = timing

	err = outputAgentPrime(cmd, textMode, reviewMode, output)

	// eagerly create whisper.db if not yet present (daemon may take time to start)
	if projCfg, cfgErr := config.LoadProjectConfig(projectRoot); cfgErr == nil && projCfg != nil {
		repoID := projCfg.RepoID
		ep := endpoint.GetForProject(projectRoot)
		if repoID != "" && ep != "" {
			whisperDir := paths.WhisperDBDir(repoID, ep)
			if whisperDir != "" {
				whisperDBPath := filepath.Join(whisperDir, "whisper.db")
				if _, statErr := os.Stat(whisperDBPath); os.IsNotExist(statErr) {
					_ = os.MkdirAll(whisperDir, 0o755)
					if ws, wsErr := whisperstore.Open(whisperDBPath); wsErr == nil {
						ws.Close()
					}
				}
			}
		}
	}

	// Start daemon if not already running.
	// Daemon self-exits via inactivity timeout when heartbeats stop.
	// Runs after output so agent gets its bootstrap response immediately.
	if config.IsInitialized(projectRoot) {
		_ = daemon.EnsureDaemonAttached()
	}

	return err
}

// resolveAgentIDFromStates attempts to find a reusable agent ID from active recordings.
// Checks envID first (from SAGEOX_AGENT_ID), then falls back to sole-active-recording.
// Returns empty string if no match found.
func resolveAgentIDFromStates(states []*session.RecordingState, envID string) string {
	// try env-provided ID first
	if envID != "" {
		for _, s := range states {
			if s.AgentID == envID && s.IsAgentAlive() {
				slog.Debug("prime: reusing agent ID from SAGEOX_AGENT_ID env", "agent_id", envID)
				return envID
			}
		}
	}
	// last resort: if exactly one active recording exists, reuse it
	var alive []*session.RecordingState
	for _, s := range states {
		if s.IsAgentAlive() {
			alive = append(alive, s)
		}
	}
	if len(alive) == 1 {
		slog.Debug("prime: reusing sole active recording agent ID", "agent_id", alive[0].AgentID)
		return alive[0].AgentID
	}
	return ""
}

// loadResolvedAttribution loads and merges attribution from user and project configs.
// Project config takes precedence over user config, which takes precedence over defaults.
func loadResolvedAttribution() config.ResolvedAttribution {
	// load user config (ignore errors, use defaults)
	userCfg, _ := config.LoadUserConfig()
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

// loadProjectGuidance is intentionally a no-op.
// The project's AGENTS.md / CLAUDE.md are already loaded natively by AI coding tools
// (Claude Code reads CLAUDE.md, Codex reads AGENTS.md, etc.), so ox agent prime
// must NOT re-inject them — that would waste tokens on duplicate content.
// Only team-context-level instructions (loaded via loadTeamInstructions) are emitted.
func loadProjectGuidance(projectRoot string, agentType string) *ProjectGuidance {
	return nil // project guidance loaded natively by the AI tool, not by prime
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

// buildCapturePriorGuidance delegates to prime.BuildCapturePriorGuidance.
func buildCapturePriorGuidance(agentID string) *capturePriorGuidance {
	return prime.BuildCapturePriorGuidance(agentID)
}

// buildGuidance constructs state-aware command guidance for agent consumption.
// Performs I/O (os.Stat, exec.Command) to resolve repo slug and code DB availability,
// then delegates to pure prime.BuildGuidance.
func buildGuidance(agentID, projectRoot string, teamCtx *teamContextInfo, ledgerStatus *ledgerInfo) *agentGuidance {
	repoSlug := repoSlugFromRemoteOrDir(projectRoot)
	codeDBDir := resolveCodeDBDir(projectRoot)
	_, statErr := os.Stat(codeDBDir)

	return prime.BuildGuidance(prime.GuidanceParams{
		AgentID:          agentID,
		RepoSlug:         repoSlug,
		TeamCtx:          teamCtx,
		Ledger:           ledgerStatus,
		CodeDBExists:     statErr == nil,
		MemoryEnabled:    auth.IsMemoryEnabled(),
		MurmuringEnabled: config.MurmuringEnabled(projectRoot),
	})
}

// repoSlugFromRemoteOrDir extracts "owner/repo" from git remote origin URL,
// falling back to the directory name if the remote is unavailable.
// Examples: "sageox/ox", "my-project"
// offline-safe: falls back to directory name for local-only repos
func repoSlugFromRemoteOrDir(projectRoot string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err == nil {
		url := strings.TrimSpace(string(out))
		// handle SSH: git@github.com:owner/repo.git
		if idx := strings.Index(url, ":"); idx != -1 && !strings.Contains(url[:idx], "/") {
			url = url[idx+1:]
		}
		// handle HTTPS: https://github.com/owner/repo.git
		url = strings.TrimSuffix(url, ".git")
		parts := strings.Split(url, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	return filepath.Base(projectRoot)
}

// startSessionRecording attempts to start session recording if enabled.
// Returns the session status for inclusion in prime output.
// Errors are logged but not fatal - session recording is optional.
func startSessionRecording(projectRoot, agentID, agentType, parentAgentID string) *sessionStatus {
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

	// respect explicit session stop — user ran /ox-session-stop, don't auto-restart
	if session.ConsumeExplicitStop(projectRoot, agentID) {
		return nil
	}

	// check if already recording
	if existing, err := session.LoadRecordingStateForAgent(projectRoot, agentID); err == nil && existing != nil {
		return &sessionStatus{
			Recording: true,
			File:      existing.SessionFile,
			Mode:      existing.FilterMode,
			Source:    string(resolved.Source),
		}
	}

	// generate session file path
	timestamp := time.Now().Format("2006-01-02-150405")
	outputFile := filepath.Join(projectRoot, ".sageox", "sessions", fmt.Sprintf("%s-%s.md", timestamp, agentID))

	// Determine the long-lived agent PID for liveness detection.
	// Hooks run inside a transient bash shell, so os.Getppid() returns the shell
	// PID which dies immediately after the hook. OX_PARENT_PID is set by the hook
	// to proc.FindAgentAncestorPID(), which walks the tree to find the agent process.
	// If OX_PARENT_PID is missing (direct invocation), walk the tree ourselves.
	parentPID := proc.FindAgentAncestorPID()
	if envPID := os.Getenv("OX_PARENT_PID"); envPID != "" {
		if parsed, parseErr := strconv.Atoi(envPID); parseErr == nil && parsed > 0 {
			parentPID = parsed
		}
	}

	// determine watch mode: hook-driven agents use hooks, hookless agents use daemon tail
	watchMode := "hook"
	if agent := GetAgent(agentType); agent != nil {
		if !agent.SupportsHooks() {
			watchMode = "tail"
		} else if !agent.HasHooks(false) && !agent.HasHooks(true) {
			// agent supports hooks but none are installed — fall back to tail mode
			watchMode = "tail"
		}
	}

	opts := session.StartRecordingOptions{
		AgentID:       agentID,
		AdapterName:   agentType,
		OutputFile:    outputFile,
		FilterMode:    resolved.Mode,
		ParentPID:     parentPID,
		Username:      identity.AttributionUsername(endpoint.GetForProject(projectRoot), config.GetDisplayName()),
		WorkspacePath: projectRoot,
		Branch:        repotools.GetCurrentBranch(projectRoot),
		WatchMode:     watchMode,
	}

	// propagate parent agent info so the recording state knows this is a subagent
	if parentAgentID != "" {
		opts.ParentAgentID = parentAgentID
		if parentState, _ := session.LoadRecordingStateForAgent(projectRoot, parentAgentID); parentState != nil {
			opts.ParentSessionPath = parentState.SessionPath
		}
	}

	state, err := session.StartRecording(projectRoot, opts)
	if err != nil {
		// already recording is not an error
		if errors.Is(err, session.ErrAlreadyRecording) {
			existingState, _ := session.LoadRecordingStateForAgent(projectRoot, agentID)
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

	// write raw.jsonl header immediately so incremental hooks can append entries
	if writeErr := writeRawHeader(projectRoot, state); writeErr != nil {
		slog.Warn("failed to write raw.jsonl header at auto-start", "error", writeErr)
	}

	// for tail-mode sessions: try to find the agent's session file and tell
	// the daemon to start tailing it. If TryConnect returns nil (daemon not
	// yet running), no data is lost: DetectAndRestart reads from
	// SourceOffset=0, catching up from the beginning of the file once the
	// daemon starts and discovers the active recording.
	if state.WatchMode == "tail" {
		sendSessionWatchStart(state, projectRoot)
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

// sendSessionWatchStart attempts to find the agent's session file and tell
// the daemon to start tailing it. Best-effort: if the file doesn't exist yet
// or daemon isn't running, the doctor interval picks it up later.
func sendSessionWatchStart(state *session.RecordingState, projectRoot string) {
	// try to discover the agent's native session file
	sessionFile := state.SessionFile
	if sessionFile == "" {
		adapter, err := adapters.GetAdapter(state.AdapterName)
		if err != nil {
			return
		}
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = projectRoot
		}
		sf, err := adapter.FindSessionFile(adapters.SessionLookup{
			RepoRoot: repoRoot,
			AgentID:  state.AgentID,
			Since:    state.StartedAt,
		})
		if err != nil {
			slog.Debug("tail-mode: session file not found yet, daemon will discover later",
				"agent_id", state.AgentID, "adapter", state.AdapterName)
			return
		}
		sessionFile = sf
		// persist the discovered session file so daemon can find it via DetectAndRestart
		state.SessionFile = sf
		if saveErr := session.SaveRecordingState(projectRoot, state); saveErr != nil {
			slog.Warn("failed to save session file to recording state", "error", saveErr)
		}
	}

	client := daemon.TryConnect()
	if client == nil {
		return
	}

	_ = client.SessionWatchStart(daemon.SessionWatchStartPayload{
		SessionName: filepath.Base(state.SessionPath),
		SessionFile: sessionFile,
		AdapterName: state.AdapterName,
	})
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
// Output format selection:
// XML is the preferred format for LLM agent consumption (structured, semantic, token-efficient).
// JSON is retained for backward compatibility, debugging, and programmatic consumers.
// XML output is ordered for prompt caching: static content first, per-session content last.
// See: https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices#structure-prompts-with-xml-tags
func outputAgentPrime(cmd *cobra.Command, textMode, reviewMode bool, output agentPrimeOutput) error {
	// always include tips: one for the agent, one for the agent to relay to the user
	output.AgentTip = tips.GetTip("prime")
	if output.CodeSearchTip != "" {
		output.AgentTip += " " + output.CodeSearchTip
	}
	if userTip := tips.GetPrimeUserTip(output.AgentType); userTip != "" {
		if output.UserNotification != "" {
			output.UserNotification += "\n\nTip: " + userTip
		} else {
			output.UserNotification = "Tip: " + userTip
		}
	}

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

	// determine output format: xml (default) or json
	formatFlag, _ := cmd.Flags().GetString("format")
	if formatFlag == "" {
		formatFlag = "xml" // default for all callers — all consumers are LLMs
	}

	switch formatFlag {
	case "json":
		// legacy JSON output for debugging and programmatic consumers
		cw := agentinstance.NewCountingWriter(cmd.OutOrStdout())
		encoder := json.NewEncoder(cw)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return err
		}
		// prime is not dispatched via runWithAgentID, send heartbeat directly
		if bytes := cw.BytesWritten(); bytes > 0 && output.AgentID != "" {
			sendContextHeartbeat(output.AgentID, bytes, "prime")
		}
		return nil
	default:
		// XML: structured tags optimized for LLM consumption and prompt caching
		return outputAgentPrimeXML(cmd, output)
	}
}

// buildHumanSummary delegates to prime.BuildHumanSummary.
func buildHumanSummary(output agentPrimeOutput) string {
	return prime.BuildHumanSummary(output)
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

	// knowledge sources disambiguation — always shown
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "## Knowledge Sources")
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "SageOx has two SEPARATE knowledge sources:")
	fmt.Fprintln(cmd.OutOrStdout(), "  1. TEAM CONTEXT — team-wide meetings, decisions, conventions (all repos). Command: ox agent team-ctx [slug]")
	fmt.Fprintln(cmd.OutOrStdout(), "  2. SESSIONS/LEDGER — repo-specific coding session archive (this repo only). Command: ox session list")
	fmt.Fprintln(cmd.OutOrStdout(), "These are unrelated. Sessions are NOT discussions. The ledger is NOT team context.")

	// other team contexts section
	if output.OtherTeams != nil && len(output.OtherTeams.Teams) > 0 {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "## Other Team Contexts (%d)\n", len(output.OtherTeams.Teams))
		fmt.Fprintln(cmd.OutOrStdout())
		// column-aligned table
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %-30s %s\n", "slug", "name", "age")
		for _, t := range output.OtherTeams.Teams {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %-30s %s\n", t.Slug, t.Name, t.Age)
		}
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Read with: ox agent team-ctx <slug>")
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
			fmt.Fprintln(cmd.OutOrStdout(), "Use 'ox session list' and 'ox session view' for sessions.")
			fmt.Fprintln(cmd.OutOrStdout(), "If you encounter a stub file (3-line pointer starting with \"version https://git-lfs\"),")
			fmt.Fprintln(cmd.OutOrStdout(), "fetch real content: ox fetch <path>  (prints local cache path you can Read)")
			fmt.Fprintln(cmd.OutOrStdout(), "  ox fetch <path> -o output.jpg   # explicit output path")
			fmt.Fprintln(cmd.OutOrStdout(), "  ox fetch <path> --stdout | jq . # stream to stdout")
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
		if output.ProjectGuidance.Skipped {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintf(cmd.OutOrStdout(), "\n## Project Guidance\n\n_Skipped: %s_\n", output.ProjectGuidance.SkipReason)
		} else {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "---PROJECT_GUIDANCE---")
			fmt.Fprintln(cmd.OutOrStdout(), output.ProjectGuidance.Content)
			fmt.Fprintln(cmd.OutOrStdout(), "---END_PROJECT_GUIDANCE---")
		}
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

		// emit v4 team memory section
		emitTeamMemorySection(cmd, output.TeamContext)

		// emit agents-level AGENTS.md if present (team-authored agent instructions)
		if output.TeamContext.AgentsAgentsMDContent != "" {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), output.TeamContext.AgentsAgentsMDContent)
		}

		// emit coworkers section if any exist
		if len(output.TeamContext.Coworkers) > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "## Expert Coworkers")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Your team has expert coworkers (specialized subagents) with domain expertise.")
			fmt.Fprintln(cmd.OutOrStdout(), "**When the user's task matches a coworker's description, load it first:**")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "  ox coworker load <name>")
			fmt.Fprintln(cmd.OutOrStdout())

			// reference the index.md catalog if it exists
			if output.TeamContext.AgentsIndexPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Full catalog at `%s`.\n", output.TeamContext.AgentsIndexPath)
				fmt.Fprintln(cmd.OutOrStdout())
			}

			fmt.Fprintln(cmd.OutOrStdout(), "| Coworker | When to Use |")
			fmt.Fprintln(cmd.OutOrStdout(), "|----------|-------------|")
			for _, agent := range output.TeamContext.Coworkers {
				desc := agent.Description
				if desc == "" {
					desc = "(no description)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "| %s | %s |\n", agent.Name, desc)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Loading a coworker outputs its full expertise into your context for the task.")
		}

		// emit team commands table if any exist
		if len(output.TeamContext.CoworkerCommands) > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "## Team Commands")
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "| Command | Trigger | Description |")
			fmt.Fprintln(cmd.OutOrStdout(), "|---------|---------|-------------|")
			for _, tcmd := range output.TeamContext.CoworkerCommands {
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

		// always emit team context guidance — may sync after prime runs
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "**Team context available** — team-wide recorded meetings and decisions")
		fmt.Fprintln(cmd.OutOrStdout(), "(architecture, conventions, product direction — shared across all repos, NOT repo session history).")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "  Read team context:  ox agent team-ctx")
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
		fmt.Fprintln(cmd.OutOrStdout(), "To capture prior conversation from before recording started, generate JSONL and pipe to:")
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

// canonicalAgentType delegates to prime.CanonicalAgentType.
func canonicalAgentType(agentType string) string {
	return prime.CanonicalAgentType(agentType)
}

// isAgentSupported delegates to prime.IsAgentSupported.
func isAgentSupported(agentType string) bool {
	return prime.IsAgentSupported(agentType)
}

// getAgentSupportNotice delegates to prime.GetAgentSupportNotice.
func getAgentSupportNotice(agentType string) string {
	return prime.GetAgentSupportNotice(agentType)
}

// codexLifecycleNotification delegates to prime.CodexLifecycleNotification.
func codexLifecycleNotification(agentType string) string {
	return prime.CodexLifecycleNotification(agentType)
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
		ReadCommand: "ox agent team-ctx", // not quoted: this is a machine-parsed command field
	}

	// if team context directory hasn't synced yet, return partial info
	// so agents still see the "team context available" section
	if _, err := os.Stat(tc.Path); os.IsNotExist(err) {
		return info
	}

	// check for human escalation roster
	escalationPath := filepath.Join(tc.Path, "capabilities", "team", "index.md")
	if _, err := os.Stat(escalationPath); err == nil {
		info.Escalation = "capabilities/team/index.md"
	}

	// discover coworker customizations from coworkers/
	// agents in coworkers/agents/, commands in coworkers/commands/
	customizations, err := claude.DiscoverAll(tc.Path)
	if err == nil && customizations != nil && customizations.HasAnyCustomizations() {
		// populate instruction file paths
		if customizations.HasInstructionFiles() {
			info.CoworkerInstructions = &teamCoworkerInstructions{
				ClaudeMDPath: customizations.ClaudeMDPath,
				AgentsMDPath: customizations.AgentsMDPath,
				HasClaudeMD:  customizations.HasClaudeMD,
				HasAgentsMD:  customizations.HasAgentsMD,
			}
		}

		// populate discovered agents/commands
		info.Coworkers = customizations.Agents
		info.CoworkerCommands = customizations.Commands

		// populate agents index path if exists
		if customizations.HasAgentsIndex {
			info.AgentsIndexPath = customizations.AgentsIndexPath
		}

		// populate agents-level AGENTS.md content if exists
		if customizations.HasAgentsAgentsMD {
			info.AgentsAgentsMDContent = customizations.AgentsAgentsMDContent
		}

		// build coworker hint when agents are available
		// keep minimal — structured data is in Coworkers[], guidance has the commands
		if len(customizations.Agents) > 0 {
			names := make([]string, 0, len(customizations.Agents))
			for _, a := range customizations.Agents {
				names = append(names, a.Name)
			}
			info.CoworkerHint = fmt.Sprintf(
				"%d expert coworker agents available: %s. Load: 'ox coworker load <name>'",
				len(names), strings.Join(names, ", "))
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

	// v4 team memory loading
	loadTeamMemory(info, tc.Path)

	// sync health: check staleness
	syncState := daemon.LoadSyncState(tc.Path)
	if syncState.IsStale(daemon.DefaultStalenessThreshold) && !syncState.LastSync.IsZero() {
		info.Stale = true
		d := syncState.StaleDuration()
		if d >= 24*time.Hour {
			info.StaleSince = fmt.Sprintf("%dd", int(d.Hours()/24))
		} else {
			info.StaleSince = fmt.Sprintf("%dh", int(d.Hours()))
		}
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

// loadTeamMemory populates v4 memory fields on teamContextInfo.
// MEMORY.md is always fully inlined. SOUL.md and TEAM.md are reference
// pointers only (agents read on demand to save tokens).
func loadTeamMemory(info *teamContextInfo, teamDir string) {
	// MEMORY.md — load first N lines to cap context bloat
	if content := claude.ReadFirstLines(filepath.Join(teamDir, "MEMORY.md"), constants.MaxInlineContextLines); content != "" {
		info.MemoryContent = content
	}

	// SOUL.md — reference pointer only
	soulPath := filepath.Join(teamDir, "SOUL.md")
	if _, err := os.Stat(soulPath); err == nil {
		info.SoulHint = soulPath
	}

	// TEAM.md — reference pointer only
	teamPath := filepath.Join(teamDir, "TEAM.md")
	if _, err := os.Stat(teamPath); err == nil {
		info.TeamHint = teamPath
	}

	// memory/GUIDE.md — observation guidance reference pointer
	guidePath := filepath.Join(teamDir, "memory", "GUIDE.md")
	if _, err := os.Stat(guidePath); err == nil {
		info.ObservationGuideHint = guidePath
	}

	// discover memory timeline files for progressive disclosure
	info.MemoryDaily = discoverMemoryFiles(filepath.Join(teamDir, "memory", "daily"))
	info.MemoryWeekly = discoverMemoryFiles(filepath.Join(teamDir, "memory", "weekly"))
	info.MemoryMonthly = discoverMemoryFiles(filepath.Join(teamDir, "memory", "monthly"))
}

// discoverMemoryFiles lists .md files in a directory, sorted reverse-chronologically.
func discoverMemoryFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files
}

// emitTeamMemorySection outputs the v4 team memory section in text mode.
func emitTeamMemorySection(cmd *cobra.Command, tc *teamContextInfo) {
	if tc == nil {
		return
	}

	guideEnabled := tc.ObservationGuideHint != "" && auth.IsMemoryEnabled()
	hasMemory := tc.MemoryContent != "" || tc.SoulHint != "" || tc.TeamHint != "" || guideEnabled ||
		len(tc.MemoryDaily) > 0 || len(tc.MemoryWeekly) > 0 || len(tc.MemoryMonthly) > 0

	if !hasMemory {
		return
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "## Team Memory")
	fmt.Fprintln(out)

	if tc.Stale {
		fmt.Fprintf(out, "> **Warning:** Team memory may be stale (last sync %s ago). Run `ox doctor` to diagnose.\n\n", tc.StaleSince)
	}

	// MEMORY.md — always full content
	if tc.MemoryContent != "" {
		fmt.Fprintln(out, "### MEMORY.md")
		fmt.Fprintln(out, tc.MemoryContent)
		fmt.Fprintln(out)
	}

	// SOUL.md, TEAM.md, GUIDE.md — reference pointers
	if tc.SoulHint != "" || tc.TeamHint != "" || guideEnabled {
		fmt.Fprintln(out, "### Available Context")
		if tc.SoulHint != "" {
			fmt.Fprintf(out, "- SOUL.md: %s (team identity and values — read when needed)\n", tc.SoulHint)
		}
		if tc.TeamHint != "" {
			fmt.Fprintf(out, "- TEAM.md: %s (team members and patterns — read when needed)\n", tc.TeamHint)
		}
		if guideEnabled {
			fmt.Fprintf(out, "- GUIDE.md: %s (observation guidance — read before using 'ox memory put')\n", tc.ObservationGuideHint)
		}
		fmt.Fprintln(out)
	}

	// observation recording — behavioral directive (not just a tool reference)
	if guideEnabled {
		fmt.Fprintln(out, "### Observation Recording (Active)")
		fmt.Fprintln(out, "Proactively record observations throughout this session using `ox memory put`.")
		fmt.Fprintln(out, "Record decisions, discoveries, questions, and notable events as they happen — don't wait to be asked.")
		fmt.Fprintf(out, "Read GUIDE.md first for what to capture: %s\n", tc.ObservationGuideHint)
		fmt.Fprintln(out)
	}

	// progressive disclosure
	hasTimeline := len(tc.MemoryDaily) > 0 || len(tc.MemoryWeekly) > 0 || len(tc.MemoryMonthly) > 0
	if hasTimeline {
		fmt.Fprintln(out, "### Progressive Disclosure")
		fmt.Fprintln(out, "For deeper context beyond MEMORY.md:")
		if len(tc.MemoryDaily) > 0 {
			fmt.Fprintf(out, "- Recent: memory/daily/ (%d files — what happened recently)\n", len(tc.MemoryDaily))
		}
		if len(tc.MemoryWeekly) > 0 {
			fmt.Fprintf(out, "- Patterns: memory/weekly/ (%d files — weekly themes)\n", len(tc.MemoryWeekly))
		}
		if len(tc.MemoryMonthly) > 0 {
			fmt.Fprintf(out, "- Trends: memory/monthly/ (%d files — monthly consolidation)\n", len(tc.MemoryMonthly))
		}
		fmt.Fprintln(out)
	}
}

// discoverOtherTeamContexts returns lightweight entries for non-primary team contexts.
// Returns nil when the user only belongs to one team.
func discoverOtherTeamContexts(projectRoot string, primaryTeamID string) *otherTeams {
	teams := discoverAllTeams(projectRoot)
	if len(teams) == 0 {
		return nil
	}

	// get endpoint for root path
	ep := endpoint.GetForProject(projectRoot)
	if ep == "" {
		return nil
	}
	root := paths.TeamsDataDir(ep)

	var entries []otherTeamEntry
	for _, t := range teams {
		if t.Primary {
			continue
		}

		// compute content age from git log
		age := teamContextAge(t.Path)

		// extract dir relative to root
		dir := t.TeamID
		if rel, err := filepath.Rel(root, t.Path); err == nil {
			dir = rel
		}

		entries = append(entries, otherTeamEntry{
			Slug: t.Slug,
			Name: t.Name,
			Dir:  dir,
			Age:  age,
		})
	}

	if len(entries) == 0 {
		return nil
	}

	// sort by content freshness (entries with age come first, newest first)
	sortOtherTeamsByAge(entries)

	return &otherTeams{
		Root:  root,
		Hint:  "Only read when user asks about a specific team by name: 'ox agent team-ctx <slug>'",
		Teams: entries,
	}
}

// teamContextAge returns a human-readable age of the most recent content change
// in a team context directory, based on git log.
func teamContextAge(teamCtxPath string) string {
	if teamCtxPath == "" {
		return ""
	}
	if _, err := os.Stat(teamCtxPath); os.IsNotExist(err) {
		return ""
	}
	cmd := exec.Command("git", "-C", teamCtxPath, "log", "-1", "--format=%ci")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	dateStr := strings.TrimSpace(string(output))
	if dateStr == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02 15:04:05 -0700", dateStr)
	if err != nil {
		return ""
	}
	return formatAge(time.Since(t))
}

// formatAge delegates to prime.FormatAge.
func formatAge(d time.Duration) string {
	return prime.FormatAge(d)
}

// sortOtherTeamsByAge delegates to prime.SortOtherTeamsByAge.
func sortOtherTeamsByAge(entries []otherTeamEntry) {
	prime.SortOtherTeamsByAge(entries)
}

// checkTeamContextStaleness checks if team context has been synced recently.
// Uses daemon.LoadSyncState for cached sync state, with IPC fallback.
func checkTeamContextStaleness(tc *teamContextInfo, _ string) {
	if tc == nil {
		return
	}

	syncState := daemon.LoadSyncState(tc.Path)
	if syncState.LastSync.IsZero() {
		tc.Stale = true
		tc.StaleSince = "unknown"
		return
	}

	if syncState.IsStale(daemon.DefaultStalenessThreshold) {
		tc.Stale = true
		tc.StaleSince = formatAge(time.Since(syncState.LastSync))
	}
}

// discoverLedger checks whether the ledger exists and returns actionable guidance
// for agents to help users discover prior coding sessions for this repo.
// Reuses getLedgerPath() from doctor_ledger_git.go (same resolution used by session commands).
func discoverLedger(teamCtx *teamContextInfo) *ledgerInfo {
	path := getLedgerPath()
	if path == "" {
		return &ledgerInfo{Exists: false}
	}

	hint := "The ledger is a repo-specific archive of prior AI coworker coding sessions. It is NOT team context. Only consult when explicitly asked to review prior sessions. Use 'ox session list' to browse and 'ox session view <name> --text' to view one. If you encounter stub files, use 'ox fetch <path>' to get real content."

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
