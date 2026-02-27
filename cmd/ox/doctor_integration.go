package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/pkg/agentx"
)

// checkAgentFileExists checks if any agent instruction file exists
func checkAgentFileExists() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Agent file", "not in git repo", "")
	}
	files := []string{"AGENTS.md", "CLAUDE.md", ".copilot-instructions.md"}
	for _, file := range files {
		if _, err := os.Stat(filepath.Join(gitRoot, file)); err == nil {
			return PassedCheck("Agent file", file)
		}
	}
	return WarningCheck("Agent file", "none found", "Create CLAUDE.md or AGENTS.md for agent integration")
}

func checkAgentsIntegration() checkResult {
	return checkAgentsIntegrationWithFix(false)
}

func checkAgentsIntegrationWithFix(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("ox agent prime integration", "not in git repo", "")
	}

	// primary check: both header and footer markers must exist
	if HasBothPrimeMarkers(gitRoot) {
		// determine which file has it for reporting
		files := []string{"AGENTS.md", "CLAUDE.md"}
		for _, file := range files {
			filePath := filepath.Join(gitRoot, file)
			if content, err := os.ReadFile(filePath); err == nil {
				if strings.Contains(string(content), OxPrimeMarker) {
					return PassedCheck(fmt.Sprintf("ox agent prime in %s", file), "canonical")
				}
			}
		}
		return PassedCheck("ox agent prime integration", "canonical")
	}

	// partial check: footer exists but header missing - needs upgrade
	if HasOxPrimeMarker(gitRoot) && !HasOxPrimeCheckMarker(gitRoot) {
		if fix {
			injected, err := EnsureOxPrimeMarker(gitRoot)
			if err != nil {
				return FailedCheck("ox agent prime integration", "header inject failed", err.Error())
			}
			if injected {
				return PassedCheck("ox agent prime integration", "upgraded (added header)")
			}
		}
		return WarningCheck("ox agent prime integration", "missing header marker",
			"Run `ox doctor --fix` to add header marker")
	}

	files := []string{"AGENTS.md", "CLAUDE.md", ".copilot-instructions.md"}

	// legacy fallback: check for old multi-line block or "ox agent prime" patterns
	legacyPatterns := []string{LegacyOxPrimeLine, "ox agent prime", "ox prime"}
	for _, file := range files {
		filePath := filepath.Join(gitRoot, file)
		if content, err := os.ReadFile(filePath); err == nil {
			contentStr := string(content)
			for _, pattern := range legacyPatterns {
				if strings.Contains(contentStr, pattern) {
					// found legacy pattern
					if fix {
						// check if running in agent context
						if agentx.IsAgentContext() {
							// output instructions for agent to perform smart upgrade
							return outputAgentUpgradeInstructions(file, filePath, contentStr)
						}
						// perform the upgrade using EnsureOxPrimeMarker
						injected, err := EnsureOxPrimeMarker(gitRoot)
						if err != nil {
							return FailedCheck(fmt.Sprintf("ox agent prime in %s", file), "upgrade failed", err.Error())
						}
						if injected {
							return PassedCheck(fmt.Sprintf("ox agent prime in %s", file), "upgraded to canonical")
						}
						return PassedCheck(fmt.Sprintf("ox agent prime in %s", file), "already canonical")
					}
					// not fixing - suggest upgrade
					return WarningCheck(fmt.Sprintf("ox agent prime in %s", file), "legacy format",
						"Run `ox doctor --fix` to update to canonical format")
				}
			}
		}
	}

	// not found at all - try to fix
	if fix {
		injected, err := EnsureOxPrimeMarker(gitRoot)
		if err != nil {
			return FailedCheck("ox agent prime integration", "inject failed", err.Error())
		}
		if injected {
			// determine which file has the marker now
			for _, file := range []string{"AGENTS.md", "CLAUDE.md"} {
				filePath := filepath.Join(gitRoot, file)
				if content, err := os.ReadFile(filePath); err == nil {
					if strings.Contains(string(content), OxPrimeMarker) {
						return PassedCheck(fmt.Sprintf("ox agent prime in %s", file), "injected")
					}
				}
			}
			return PassedCheck("ox agent prime integration", "injected")
		}
	}

	return FailedCheck("ox agent prime integration", "not configured", "Run `ox init` to inject into agent file")
}

// SageOxCanonicalSection is the complete SageOx integration section that should
// replace any legacy or partial SageOx instructions in agent config files.
// This is the comprehensive, up-to-date integration block.
// Uses the single-line OxPrimeLine format for easy grep/verification.
const SageOxCanonicalSection = `## SageOx Integration

` + OxPrimeLine

// agentInstructionStyles defines styles for agent instruction output
var agentInstructionStyles = struct {
	title       lipgloss.Style
	label       lipgloss.Style
	file        lipgloss.Style
	bullet      lipgloss.Style
	code        lipgloss.Style
	hint        lipgloss.Style
	border      lipgloss.Style
	codeBlock   lipgloss.Style
	sectionHead lipgloss.Style
}{
	title:       lipgloss.NewStyle().Bold(true).Foreground(ui.ColorAccent),
	label:       lipgloss.NewStyle().Bold(true).Foreground(ui.ColorText),
	file:        lipgloss.NewStyle().Foreground(ui.ColorWarn),
	bullet:      lipgloss.NewStyle().Foreground(ui.ColorMuted),
	code:        lipgloss.NewStyle().Foreground(ui.ColorWarn),
	hint:        lipgloss.NewStyle().Foreground(ui.ColorMuted).Italic(true),
	border:      lipgloss.NewStyle().Foreground(ui.ColorMuted),
	codeBlock:   lipgloss.NewStyle().Foreground(ui.ColorPass).MarginLeft(4),
	sectionHead: lipgloss.NewStyle().Bold(true).Foreground(ui.ColorAccent).MarginTop(1),
}

// outputAgentUpgradeInstructions outputs structured instructions for an agent
// to perform smart file upgrades rather than crude append operations.
// This enables agents to intelligently replace entire SageOx sections.
func outputAgentUpgradeInstructions(file, filePath, content string) checkResult {
	s := agentInstructionStyles

	// find all legacy SageOx-related lines/sections to identify what to replace
	var legacyLines []string
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "ox agent prime") ||
			strings.Contains(line, "ox prime") ||
			strings.Contains(trimmed, "**SageOx**") ||
			strings.Contains(trimmed, "SageOx:") {
			legacyLines = append(legacyLines, trimmed)
		}
	}

	// build the instruction content
	var b strings.Builder

	// header
	b.WriteString("\n")
	b.WriteString(s.title.Render("AGENT INSTRUCTION: Upgrade SageOx Integration"))
	b.WriteString("\n")
	b.WriteString(ui.RenderSeparator())
	b.WriteString("\n\n")

	// file info
	b.WriteString(s.label.Render("File: "))
	b.WriteString(s.file.Render(filePath))
	b.WriteString("\n\n")

	// task description
	b.WriteString(s.label.Render("Task: "))
	b.WriteString("Replace all legacy SageOx instructions with the canonical section.\n\n")

	// legacy lines to remove
	b.WriteString(s.sectionHead.Render("REMOVE"))
	b.WriteString(s.hint.Render(" (find and delete these lines)"))
	b.WriteString("\n")
	for _, line := range legacyLines {
		if len(line) > 70 {
			line = line[:67] + "..."
		}
		b.WriteString(s.bullet.Render("  • "))
		b.WriteString(s.code.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// replacement section
	b.WriteString(s.sectionHead.Render("ADD"))
	b.WriteString(s.hint.Render(" (insert this in a logical location)"))
	b.WriteString("\n")
	b.WriteString(s.border.Render("  ┌" + strings.Repeat("─", 70) + "┐"))
	b.WriteString("\n")
	for _, line := range strings.Split(SageOxCanonicalSection, "\n") {
		b.WriteString(s.border.Render("  │ "))
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", max(0, 68-len(line))))
		b.WriteString(s.border.Render(" │"))
		b.WriteString("\n")
	}
	b.WriteString(s.border.Render("  └" + strings.Repeat("─", 70) + "┘"))
	b.WriteString("\n\n")

	// placement hints
	b.WriteString(s.sectionHead.Render("PLACEMENT HINTS"))
	b.WriteString("\n")
	hints := []string{
		"Place near other agent/tool configuration sections",
		"If file has a 'Tools' or 'Commands' section, place nearby",
		"Ensure proper markdown heading hierarchy",
	}
	for _, hint := range hints {
		b.WriteString(s.bullet.Render("  • "))
		b.WriteString(s.hint.Render(hint))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// verification
	b.WriteString(s.hint.Render("After editing, verify with: "))
	b.WriteString(s.code.Render("ox doctor"))
	b.WriteString("\n")
	b.WriteString(ui.RenderSeparator())
	b.WriteString("\n")

	fmt.Print(b.String())

	return PassedCheck(fmt.Sprintf("ox agent prime in %s", file), "agent instructions provided")
}

// checkClaudeCodeHooks checks if project-level Claude Code hooks are properly installed
// in .claude/settings.local.json.
func checkClaudeCodeHooks(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Claude Code hooks", "not in git repo", "")
	}

	if HasProjectClaudeHooks(gitRoot) {
		return PassedCheck("Claude Code hooks", "installed (project)")
	}

	// hooks not installed
	if fix {
		if err := InstallProjectClaudeHooks(gitRoot); err != nil {
			return FailedCheck("Claude Code hooks", "install failed", err.Error())
		}
		return PassedCheck("Claude Code hooks", "installed (project)")
	}

	return FailedCheck("Claude Code hooks", "not installed",
		"Run `ox init` or `ox doctor --fix` to install project-level hooks")
}

// checkUserLevelIntegration checks if the ox:prime marker exists in the
// user-level context file for the detected agent.
func checkUserLevelIntegration() checkResult {
	agentType := detectActiveAgent()
	if hasUserLevelAgentMarker(agentType) {
		contextFile := agentUserContextFile[agentType]
		name := agentDisplayName(agentType)
		return PassedCheck("Global ox prime",
			fmt.Sprintf("enabled (%s/%s)", name, contextFile))
	}

	// not installed - this is optional, show as info (skipped) not warning
	return SkippedCheck("Global ox prime", "not enabled", "Run `ox integrate install --user` to enable for all projects")
}

// editorConfig defines an AI editor and its detection paths
type editorConfig struct {
	name  string
	paths []string // relative to homeDir or absolute
}

// knownEditors lists AI editors to detect with their config paths
var knownEditors = []editorConfig{
	{"OpenCode", []string{".opencode", ".config/opencode"}},
	{"Gemini CLI", []string{".gemini"}},
	{"code_puppy", []string{".code_puppy"}},
	{"Cursor", []string{".cursor", "Library/Application Support/Cursor"}},
	{"Windsurf", []string{".windsurf", "Library/Application Support/Windsurf"}},
	{"VSCode", []string{".vscode", "Library/Application Support/Code"}},
}

// detectOtherAIEditors returns a list of detected AI editors
func detectOtherAIEditors() []string {
	var detected []string

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return detected
	}

	for _, editor := range knownEditors {
		found := false
		for _, relPath := range editor.paths {
			// check both project-level and user-level paths
			paths := []string{relPath, filepath.Join(homeDir, relPath)}
			for _, p := range paths {
				if _, err := os.Stat(p); err == nil {
					detected = append(detected, editor.name)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}

	return detected
}

// checkAgentHooks checks if hooks are properly installed for the given agent
// Uses tiered detection:
// - Project config exists → error if hooks not installed
// - CLI in PATH only → suggestion to install hooks
func checkAgentHooks(agent Agent, agentName string, fix bool) checkResult {
	projectDetected := agent.DetectProject()
	cliDetected := agent.DetectCLI()

	// not detected at all
	if !projectDetected && !cliDetected {
		return SkippedCheck(agentName+" hooks", agentName+" not detected", "")
	}

	projectInstalled := agent.HasHooks(false)
	userInstalled := agent.HasHooks(true)

	if projectInstalled || userInstalled {
		location := "user"
		if projectInstalled {
			location = "project"
		}
		return PassedCheck(agentName+" hooks", fmt.Sprintf("installed (%s)", location))
	}

	// hooks not installed - behavior depends on detection type
	if projectDetected {
		// project has config - hooks should be installed (error)
		if fix {
			if err := agent.Install(false); err != nil {
				return FailedCheck(agentName+" hooks", "install failed", err.Error())
			}
			return PassedCheck(agentName+" hooks", "installed (project)")
		}
		return FailedCheck(agentName+" hooks", "not installed",
			fmt.Sprintf("Run `ox hooks install --%s` or `ox doctor --fix`", strings.ToLower(agentName)))
	}

	// CLI detected but no project config - suggest hooks (info/skipped, not error)
	return SkippedCheck(agentName+" hooks", "CLI detected, no project config",
		fmt.Sprintf("Run `ox hooks install --%s` to enable for this project", strings.ToLower(agentName)))
}

// checkOpenCodeHooks checks if OpenCode hooks are properly installed
func checkOpenCodeHooks(fix bool) checkResult {
	return checkAgentHooks(&OpenCodeAgent{}, "OpenCode", fix)
}

// checkGeminiHooks checks if Gemini CLI hooks are properly installed
func checkGeminiHooks(fix bool) checkResult {
	return checkAgentHooks(&GeminiAgent{}, "Gemini CLI", fix)
}

// checkCodePuppyHooks checks if code_puppy hooks are properly installed
func checkCodePuppyHooks(fix bool) checkResult {
	return checkAgentHooks(&CodePuppyAgent{}, "code_puppy", fix)
}

// detection functions for AI coding tools

// detectClaudeCode checks if Claude Code is installed or configured
func detectClaudeCode() bool {
	gitRoot := findGitRoot()
	if gitRoot != "" {
		// check for .claude directory in project
		if _, err := os.Stat(filepath.Join(gitRoot, ".claude")); err == nil {
			return true
		}
		// check for CLAUDE.md in project
		if _, err := os.Stat(filepath.Join(gitRoot, "CLAUDE.md")); err == nil {
			return true
		}
	}
	// check for user-level Claude config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".claude")); err == nil {
		return true
	}
	return false
}

// detectOpenCode checks if OpenCode is installed or configured
func detectOpenCode() bool {
	return (&OpenCodeAgent{}).Detect()
}

// detectGemini checks if Gemini CLI is installed or configured
func detectGemini() bool {
	return (&GeminiAgent{}).Detect()
}

// detectCodex checks if OpenAI Codex CLI is configured in this project
func detectCodex() bool {
	return (&CodexAgent{}).Detect()
}

// detectCodePuppy checks if code_puppy is installed or configured
func detectCodePuppy() bool {
	return (&CodePuppyAgent{}).Detect()
}

// checkCodexIntegration checks if Codex is detected (uses AGENTS.md, no hooks needed)
func checkCodexIntegration() checkResult {
	agent := &CodexAgent{}
	projectDetected := agent.DetectProject()
	manualLifecycle := "Manual lifecycle (optional): run `ox agent prime` at session start, and use `ox agent session start` / `ox agent session stop` for recording."

	if projectDetected {
		// project has .codex/ - show as integrated via AGENTS.md
		return PassedCheck("Codex", "uses AGENTS.md (no hooks needed). "+manualLifecycle)
	}

	// CLI detected but no project config - suggest creating .codex/
	return SkippedCheck("Codex", "CLI detected, no project config",
		"Codex reads AGENTS.md directly when .codex/ exists. "+manualLifecycle)
}
