package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/pkg/agentx"
)

// staleInstanceThreshold defines when an agent instance is considered stale (no activity in 24 hours)
const staleInstanceThreshold = 24 * time.Hour

// checkAgentEnvironment detects if running inside a coding agent
// Note: This function is called from ox doctor context, not from a Cobra command,
// so it cannot access cmd.Context() and must create its own context.
func checkAgentEnvironment() checkResult {
	ctx := context.Background()
	detector := agentx.NewDetector()

	agent, _ := detector.Detect(ctx)
	if agent != nil {
		return PassedCheck("Agent Environment",
			fmt.Sprintf("Running in %s", agent.Name()))
	}

	// not in agent context - check if AGENT_ENV is set but unrecognized
	agentEnv := os.Getenv("AGENT_ENV")
	if agentEnv != "" {
		return WarningCheck("Agent Environment",
			fmt.Sprintf("AGENT_ENV=%s (unrecognized)", agentEnv),
			"Agent type not in registry. ox agent commands may not work correctly.")
	}

	return SkippedCheck("Agent Environment",
		"Not running in agent context",
		"This is normal for manual CLI usage")
}

// checkAgentEnvValidity verifies AGENT_ENV value if set
func checkAgentEnvValidity() checkResult {
	agentEnv := os.Getenv("AGENT_ENV")
	if agentEnv == "" {
		return SkippedCheck("AGENT_ENV",
			"Not set",
			"Set AGENT_ENV to override agent detection")
	}

	// Normalize aliases to canonical slugs so doctor output matches agentx
	// AgentType constants. Keep legacy aliases accepted for compatibility.
	aliases := map[string]string{
		"claude-code": "claude",
		"claudecode":  "claude",
		"claude":      "claude",
		"cursor":      "cursor",
		"windsurf":    "windsurf",
		"cline":       "cline",
		"aider":       "aider",
		"codex":       "codex",
		"opencode":    "opencode",
		"gemini":      "gemini",
		"codepuppy":   "code-puppy",
		"code-puppy":  "code-puppy",
		"droid":       "droid",
	}
	agentEnvLower := strings.ToLower(agentEnv)
	if canonical, ok := aliases[agentEnvLower]; ok {
		return PassedCheck("AGENT_ENV",
			fmt.Sprintf("Valid: %s (canonical: %s)", agentEnv, canonical))
	}

	knownAgents := []string{
		"claude", "cursor", "windsurf", "cline", "aider",
		"codex", "opencode", "gemini", "code-puppy", "droid",
	}

	return WarningCheck("AGENT_ENV",
		fmt.Sprintf("Unknown value: %s", agentEnv),
		fmt.Sprintf("Known agents: %s", strings.Join(knownAgents, ", ")))
}

// checkConflictingAgentEnvVars checks for multiple agent env vars that might conflict
func checkConflictingAgentEnvVars() checkResult {
	// env var signals grouped by agent so multiple vars from one agent don't
	// look like cross-agent conflicts.
	agentSignals := map[string][]string{
		"Claude Code": {"CLAUDE_CODE", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
		"Cursor":      {"CURSOR_TRACE_ID"},
		"Windsurf":    {"WINDSURF_SESSION"},
		"Cline":       {"CLINE_TASK_ID"},
		"Aider":       {"AIDER_SESSION"},
		"Codex":       {"CODEX_CI", "CODEX_SANDBOX", "CODEX_THREAD_ID"},
	}

	var detected []string
	for agentName, signals := range agentSignals {
		for _, envVar := range signals {
			if os.Getenv(envVar) != "" {
				detected = append(detected, agentName)
				break
			}
		}
	}

	if len(detected) == 0 {
		return SkippedCheck("Agent Conflicts",
			"No agent env vars detected",
			"")
	}

	if len(detected) == 1 {
		return PassedCheck("Agent Conflicts",
			fmt.Sprintf("Single agent: %s", detected[0]))
	}

	// multiple agents detected - potential conflict
	return WarningCheck("Agent Conflicts",
		fmt.Sprintf("Multiple agents detected: %s", strings.Join(detected, ", ")),
		"Multiple agent env vars set. This may cause unexpected behavior.")
}

// checkInstanceStale detects agent instances that have not had recent activity.
// An instance is considered stale if it was created more than 24 hours ago and has not expired.
// This helps identify abandoned instances that may be consuming resources.
func checkInstanceStale(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Stale instances",
			"not in git repo",
			"")
	}

	// check if .sageox directory exists
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return SkippedCheck("Stale instances",
			"not initialized",
			"")
	}

	// check if agent_instances directory exists
	instancesDir := filepath.Join(sageoxDir, "agent_instances")
	if _, err := os.Stat(instancesDir); os.IsNotExist(err) {
		return SkippedCheck("Stale instances",
			"no instances directory",
			"")
	}

	// scan all user instance directories
	var staleInstanceInfos []staleInstanceInfo
	var totalInstances int

	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		return SkippedCheck("Stale instances",
			"could not read instances directory",
			"")
	}

	now := time.Now()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		userSlug := entry.Name()
		store, err := agentinstance.NewStoreForUser(gitRoot, userSlug)
		if err != nil {
			continue
		}

		instances, err := store.List()
		if err != nil {
			continue
		}

		for _, inst := range instances {
			totalInstances++

			// an instance is stale if:
			// 1. It was created more than 24 hours ago
			// 2. It has not expired yet (expired instances are already pruned)
			instanceAge := now.Sub(inst.CreatedAt)
			if instanceAge > staleInstanceThreshold {
				staleInstanceInfos = append(staleInstanceInfos, staleInstanceInfo{
					agentID:   inst.AgentID,
					userSlug:  userSlug,
					createdAt: inst.CreatedAt,
					agentType: inst.AgentType,
					age:       instanceAge,
				})
			}
		}
	}

	if len(staleInstanceInfos) == 0 {
		if totalInstances == 0 {
			return SkippedCheck("Stale instances",
				"no active instances",
				"")
		}
		return PassedCheck("Stale instances",
			fmt.Sprintf("%d active, none stale", totalInstances))
	}

	// collect stale instance IDs for display
	var staleIDs []string
	for _, info := range staleInstanceInfos {
		ageStr := formatInstanceAge(info.age)
		staleIDs = append(staleIDs, fmt.Sprintf("%s (%s old)", info.agentID, ageStr))
	}

	if fix {
		// prune stale instances by running Prune() on each store
		pruned := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			userSlug := entry.Name()
			store, err := agentinstance.NewStoreForUser(gitRoot, userSlug)
			if err != nil {
				continue
			}
			count, err := store.Prune()
			if err == nil {
				pruned += count
			}
		}

		if pruned > 0 {
			return PassedCheck("Stale instances",
				fmt.Sprintf("pruned %d stale instances", pruned))
		}
		// instances are stale by age but not expired, Prune won't remove them
		// since they haven't hit ExpiresAt yet
		return WarningCheck("Stale instances",
			fmt.Sprintf("%d stale: %s", len(staleInstanceInfos), strings.Join(staleIDs, ", ")),
			"Instances are old but not expired. They will auto-expire based on ExpiresAt.")
	}

	return WarningCheck("Stale instances",
		fmt.Sprintf("%d stale: %s", len(staleInstanceInfos), strings.Join(staleIDs, ", ")),
		"Run `ox doctor --fix` to prune expired instances")
}

// staleInstanceInfo holds information about a stale instance for reporting
type staleInstanceInfo struct {
	agentID   string
	userSlug  string
	createdAt time.Time
	agentType string
	age       time.Duration
}

// formatInstanceAge formats a duration in a human-readable way
func formatInstanceAge(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1d"
	}
	return fmt.Sprintf("%dd", days)
}

// checkDaemonInstanceStale queries the daemon for agent instances with stale heartbeats.
// Uses the daemon's StaleThreshold (5 minutes) to detect instances that have stopped
// sending heartbeats but are still registered with the daemon.
// This is informational - no fix is available since the user should investigate why
// the agent stopped sending heartbeats.
func checkDaemonInstanceStale(_ bool) checkResult {
	// skip if daemon is not running
	if !daemon.IsRunning() {
		return SkippedCheck("Daemon agent instances",
			"daemon not running",
			"")
	}

	// connect to daemon and get status
	client := daemon.NewClient()
	status, err := client.Status()
	if err != nil {
		return SkippedCheck("Daemon agent instances",
			"could not connect to daemon",
			"")
	}

	// check if activity data is available
	if status.Activity == nil || len(status.Activity.Agents) == 0 {
		return SkippedCheck("Daemon agent instances",
			"no agent instances tracked",
			"")
	}

	// check for stale instances (no heartbeat in 5 minutes)
	now := time.Now()
	var staleAgents []daemonStaleAgent
	var activeCount int

	for _, agent := range status.Activity.Agents {
		sinceLast := now.Sub(agent.Last)
		if sinceLast >= daemon.StaleThreshold {
			staleAgents = append(staleAgents, daemonStaleAgent{
				agentID:       agent.Key,
				lastHeartbeat: agent.Last,
				sinceLastHB:   sinceLast,
			})
		} else {
			activeCount++
		}
	}

	if len(staleAgents) == 0 {
		return PassedCheck("Daemon agent instances",
			fmt.Sprintf("%d active", activeCount))
	}

	// build stale instance descriptions
	var descriptions []string
	for _, agent := range staleAgents {
		descriptions = append(descriptions,
			fmt.Sprintf("%s (last heartbeat %s ago)", agent.agentID, formatInstanceAge(agent.sinceLastHB)))
	}

	// informational warning - no fix available
	return InfoCheck("Daemon agent instances",
		fmt.Sprintf("%d stale", len(staleAgents)),
		fmt.Sprintf("Stale: %s. These agents stopped sending heartbeats.", strings.Join(descriptions, ", ")))
}

// daemonStaleAgent holds information about a stale daemon-tracked agent instance
type daemonStaleAgent struct {
	agentID       string
	lastHeartbeat time.Time
	sinceLastHB   time.Duration
}

// init registers the stale instance check with the doctor registry
func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugInstanceStale,
		Name:        "Stale agent instances",
		Category:    "Agent Health",
		FixLevel:    FixLevelSuggested,
		Description: "Detects agent instances with no recent activity",
		Run:         checkInstanceStale,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugDaemonInstanceStale,
		Name:        "Daemon agent instances",
		Category:    "Agent Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Detects daemon-tracked agent instances with stale heartbeats",
		Run:         checkDaemonInstanceStale,
	})
}
