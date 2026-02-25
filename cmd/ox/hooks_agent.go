package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Agent represents an AI coding agent with hooks capability
type Agent interface {
	// Name returns the agent display name
	Name() string

	// Install installs ox prime hooks. If user is true, installs to user-level config.
	Install(user bool) error

	// Uninstall removes ox prime hooks. If user is true, removes from user-level config.
	Uninstall(user bool) error

	// HasHooks checks if ox prime hooks are installed
	HasHooks(user bool) bool

	// List returns installation status for display
	List() map[string]bool

	// Detect returns true if this agent is installed/configured on the system
	// (either project config exists OR CLI is in PATH)
	Detect() bool

	// DetectProject returns true only if project has this agent's config directory
	// Used to determine if hooks should be required vs suggested
	DetectProject() bool

	// DetectCLI returns true if the agent's CLI is installed (in PATH)
	DetectCLI() bool

	// SupportsHooks returns true if this agent supports hooks (false for Codex which uses AGENTS.md)
	SupportsHooks() bool
}

// ClaudeAgent implements Agent interface for Claude Code
type ClaudeAgent struct{}

func (a *ClaudeAgent) Name() string {
	return "Claude"
}

func (a *ClaudeAgent) Install(user bool) error {
	if user {
		return updateUserAgentsMD()
	}
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	return InstallProjectClaudeHooks(gitRoot)
}

func (a *ClaudeAgent) Uninstall(user bool) error {
	// claude doesn't differentiate user flag for uninstall in current implementation
	return uninstallClaudeHooks()
}

func (a *ClaudeAgent) HasHooks(user bool) bool {
	if user {
		return hasUserLevelOxPrime()
	}
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return false
	}
	return HasProjectClaudeHooks(gitRoot)
}

func (a *ClaudeAgent) List() map[string]bool {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		// fall back to user-level if not in a git repo
		status, err := listClaudeHooks()
		if err != nil {
			return make(map[string]bool)
		}
		return status
	}
	return listProjectClaudeHooks(gitRoot)
}

func (a *ClaudeAgent) Detect() bool {
	return a.DetectProject() || a.DetectCLI()
}

func (a *ClaudeAgent) DetectProject() bool {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return false
	}
	// check for .claude directory in project
	if _, err := os.Stat(filepath.Join(gitRoot, ".claude")); err == nil {
		return true
	}
	// also check for CLAUDE.md at project root
	if _, err := os.Stat(filepath.Join(gitRoot, "CLAUDE.md")); err == nil {
		return true
	}
	return false
}

func (a *ClaudeAgent) DetectCLI() bool {
	// claude is special - also check user-level config since most users have it
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".claude")); err == nil {
		return true
	}
	return false
}

func (a *ClaudeAgent) SupportsHooks() bool {
	return true
}

// OpenCodeAgent implements Agent interface for OpenCode
type OpenCodeAgent struct{}

func (o *OpenCodeAgent) Name() string {
	return "OpenCode"
}

func (o *OpenCodeAgent) Install(user bool) error {
	return installOpenCodeHooks(user)
}

func (o *OpenCodeAgent) Uninstall(user bool) error {
	return uninstallOpenCodeHooks(user)
}

func (o *OpenCodeAgent) HasHooks(user bool) bool {
	return hasOpenCodeHooks(user)
}

func (o *OpenCodeAgent) List() map[string]bool {
	return listOpenCodeHooks()
}

func (o *OpenCodeAgent) Detect() bool {
	return o.DetectProject() || o.DetectCLI()
}

func (o *OpenCodeAgent) DetectProject() bool {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return false
	}
	projectDir := filepath.Join(gitRoot, ".opencode")
	_, err := os.Stat(projectDir)
	return err == nil
}

func (o *OpenCodeAgent) DetectCLI() bool {
	_, err := exec.LookPath("opencode")
	return err == nil
}

func (o *OpenCodeAgent) SupportsHooks() bool {
	return true
}

// GeminiAgent implements Agent interface for Gemini CLI
type GeminiAgent struct{}

func (g *GeminiAgent) Name() string {
	return "Gemini"
}

func (g *GeminiAgent) Install(user bool) error {
	return installGeminiHooks(user)
}

func (g *GeminiAgent) Uninstall(user bool) error {
	return uninstallGeminiHooks(user)
}

func (g *GeminiAgent) HasHooks(user bool) bool {
	return hasGeminiHooks(user)
}

func (g *GeminiAgent) List() map[string]bool {
	return listGeminiHooks()
}

func (g *GeminiAgent) Detect() bool {
	return g.DetectProject() || g.DetectCLI()
}

func (g *GeminiAgent) DetectProject() bool {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return false
	}
	projectDir := filepath.Join(gitRoot, ".gemini")
	_, err := os.Stat(projectDir)
	return err == nil
}

func (g *GeminiAgent) DetectCLI() bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}

func (g *GeminiAgent) SupportsHooks() bool {
	return true
}

// CodexAgent implements Agent interface for OpenAI Codex CLI
// Note: Codex reads AGENTS.md directly, no hooks needed
type CodexAgent struct{}

func (c *CodexAgent) Name() string {
	return "Codex"
}

func (c *CodexAgent) Install(user bool) error {
	// codex doesn't need hooks - it reads AGENTS.md directly
	return nil
}

func (c *CodexAgent) Uninstall(user bool) error {
	// codex doesn't have hooks to uninstall
	return nil
}

func (c *CodexAgent) HasHooks(user bool) bool {
	// codex reads AGENTS.md directly, so if AGENTS.md has ox prime, it's "installed"
	// return true if we detect codex is being used (meaning AGENTS.md integration applies)
	return c.Detect()
}

func (c *CodexAgent) List() map[string]bool {
	// codex uses AGENTS.md, not hooks
	return map[string]bool{
		"AGENTS.md": c.Detect(),
	}
}

func (c *CodexAgent) Detect() bool {
	return c.DetectProject() || c.DetectCLI()
}

func (c *CodexAgent) DetectProject() bool {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return false
	}
	projectDir := filepath.Join(gitRoot, ".codex")
	_, err := os.Stat(projectDir)
	return err == nil
}

func (c *CodexAgent) DetectCLI() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

func (c *CodexAgent) SupportsHooks() bool {
	// codex reads AGENTS.md directly, no hooks needed
	return false
}

// CodePuppyAgent implements Agent interface for code_puppy
type CodePuppyAgent struct{}

func (c *CodePuppyAgent) Name() string {
	return "CodePuppy"
}

func (c *CodePuppyAgent) Install(user bool) error {
	return installCodePuppyHooks(user)
}

func (c *CodePuppyAgent) Uninstall(user bool) error {
	return uninstallCodePuppyHooks(user)
}

func (c *CodePuppyAgent) HasHooks(user bool) bool {
	return hasCodePuppyHooks(user)
}

func (c *CodePuppyAgent) List() map[string]bool {
	return listCodePuppyHooks()
}

func (c *CodePuppyAgent) Detect() bool {
	return c.DetectProject() || c.DetectCLI()
}

func (c *CodePuppyAgent) DetectProject() bool {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return false
	}
	// check for .code_puppy directory in project
	projectDir := filepath.Join(gitRoot, ".code_puppy")
	_, err := os.Stat(projectDir)
	return err == nil
}

func (c *CodePuppyAgent) DetectCLI() bool {
	_, err := exec.LookPath("code-puppy")
	return err == nil
}

func (c *CodePuppyAgent) SupportsHooks() bool {
	return true
}

// AgentRegistry holds all registered agents
var AgentRegistry = []Agent{
	&ClaudeAgent{},
	&OpenCodeAgent{},
	&GeminiAgent{},
	&CodexAgent{},
	&CodePuppyAgent{},
}

// GetAgent returns the agent by name (case-insensitive)
func GetAgent(name string) Agent {
	for _, agent := range AgentRegistry {
		if strings.EqualFold(agent.Name(), name) {
			return agent
		}
	}
	return nil
}
