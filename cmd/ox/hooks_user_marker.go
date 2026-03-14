package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/ui"
)

// agentUserContextFile maps agent types to the context file (relative to
// UserConfigPath) where the ox:prime marker should be written.
// Only agents whose user-level context file is plain text or Markdown are
// included — JSON/YAML configs would break if we injected HTML comments.
var agentUserContextFile = map[agentx.AgentType]string{
	agentx.AgentTypeClaudeCode: "CLAUDE.md",
	// Verified: Cursor reads ~/.cursor/rules/*.mdc at user level, not .cursorrules.
	// User-level .cursorrules is not a supported pattern — skip for now.
	// agentx.AgentTypeCursor: ".cursorrules",
	//
	// Other agents to add once user-level context file reading is verified:
	// agentx.AgentTypeWindsurf: ".windsurfrules",
	// agentx.AgentTypeCopilot: ".github/copilot-instructions.md",
	// agentx.AgentTypeCline:   ".clinerules",
}

// updateUserAgentsMD detects the active agent and writes the ox:prime marker
// to the appropriate user-level context file.
func updateUserAgentsMD() error {
	agentType := detectActiveAgent()

	contextFile, ok := agentUserContextFile[agentType]
	if !ok {
		return fmt.Errorf("agent %q does not support user-level context markers (supported: %s)",
			agentType, supportedUserMarkerAgents())
	}

	filePath, err := userContextFilePath(agentType, contextFile)
	if err != nil {
		return err
	}

	return ensureUserLevelMarker(filePath, agentDisplayName(agentType))
}

// hasUserLevelAgentMarker checks if the ox:prime marker exists in the
// user-level context file for the given agent type.
func hasUserLevelAgentMarker(agentType agentx.AgentType) bool {
	contextFile, ok := agentUserContextFile[agentType]
	if !ok {
		return false
	}

	filePath, err := userContextFilePath(agentType, contextFile)
	if err != nil {
		return false
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}

	return strings.Contains(string(content), OxPrimeMarker)
}

// ensureUserLevelMarker writes the ox:prime marker to a user-level context
// file. Idempotent: returns early if the marker already exists.
func ensureUserLevelMarker(filePath, agentName string) error {
	// idempotent: skip if marker already present
	if content, err := os.ReadFile(filePath); err == nil {
		if strings.Contains(string(content), OxPrimeMarker) {
			fmt.Println(ui.PassStyle.Render("✓") + " SageOx marker already present in " + filePath)
			return nil
		}
	}

	// prompt for confirmation
	fmt.Printf("This will add SageOx guidance to your user-level %s configuration.\n", agentName)
	fmt.Println()
	fmt.Println("  File: " + ui.AccentStyle.Render(filePath))
	fmt.Println()
	fmt.Printf("This enables SageOx guidance for %s projects you work on with %s.\n",
		ui.AccentStyle.Render("ALL"), agentName)
	fmt.Println()
	if !cli.ConfirmYesNo(fmt.Sprintf("Enable SageOx for all %s projects?", agentName), true) {
		fmt.Println("Canceled.")
		return nil
	}

	// read existing content
	var existingContent string
	if data, err := os.ReadFile(filePath); err == nil {
		existingContent = string(data)
	}

	// append the canonical marker
	newContent := strings.TrimRight(existingContent, "\n\t ") + "\n\n" + OxPrimeLine + "\n"

	// ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// backup existing content before modification (user-level files aren't git-versioned)
	backupUserAgentFile(filePath, existingContent)

	if err := os.WriteFile(filePath, []byte(newContent), settingsPerm); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Println()
	fmt.Println(ui.PassStyle.Render("✓") + " SageOx marker added to " + filePath)
	return nil
}

// detectActiveAgent determines which coding agent is currently running.
// Uses the agentx detector first, then falls back to env var checks.
func detectActiveAgent() agentx.AgentType {
	ctx := context.Background()
	detector := agentx.NewDetector()

	if agent, err := detector.Detect(ctx); err == nil && agent != nil {
		return agent.Type()
	}

	// fallback: check AGENT_ENV
	switch strings.ToLower(os.Getenv("AGENT_ENV")) {
	case "claude-code", "claude":
		return agentx.AgentTypeClaudeCode
	case "cursor":
		return agentx.AgentTypeCursor
	case "windsurf":
		return agentx.AgentTypeWindsurf
	case "cline":
		return agentx.AgentTypeCline
	case "copilot":
		return agentx.AgentTypeCopilot
	}

	// default to Claude Code (most common)
	return agentx.AgentTypeClaudeCode
}

// userContextFilePath resolves the absolute path to the user-level context file.
func userContextFilePath(agentType agentx.AgentType, contextFile string) (string, error) {
	env := agentx.NewSystemEnvironment()
	agent, ok := agentx.DefaultRegistry.Get(agentType)
	if !ok {
		return "", fmt.Errorf("unknown agent type: %q", agentType)
	}

	userDir, err := agent.UserConfigPath(env)
	if err != nil {
		return "", fmt.Errorf("failed to get user config path for %s: %w", agentType, err)
	}

	return filepath.Join(userDir, contextFile), nil
}

// agentDisplayName returns a human-friendly name for an agent type.
func agentDisplayName(agentType agentx.AgentType) string {
	if agent, ok := agentx.DefaultRegistry.Get(agentType); ok {
		return agent.Name()
	}
	return string(agentType)
}

// supportedUserMarkerAgents returns a comma-separated list of agent types
// that support user-level context markers.
func supportedUserMarkerAgents() string {
	var names []string
	for at := range agentUserContextFile {
		names = append(names, string(at))
	}
	return strings.Join(names, ", ")
}

// backupUserAgentFile creates a safety backup of a user-level agent config file
// before modification. User-level files (e.g., ~/.claude/CLAUDE.md) are typically
// not git-versioned, so we create a timestamped backup next to the original.
// Format: <filename>.bak-YYYY-MM-DD-HH (e.g., CLAUDE.md.bak-2026-02-04-15).
// Best-effort: logs errors but never fails the caller's operation.
func backupUserAgentFile(filePath, content string) {
	if content == "" {
		return
	}

	timestamp := time.Now().Format("2006-01-02-15")
	backupPath := filePath + ".bak-" + timestamp
	if err := os.WriteFile(backupPath, []byte(content), 0644); err != nil {
		slog.Debug("failed to backup user agent file", "path", filePath, "backup", backupPath, "error", err)
	}
}
