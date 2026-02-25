package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// CopilotAgent implements Agent for GitHub Copilot.
type CopilotAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewCopilotAgent creates a new GitHub Copilot agent.
func NewCopilotAgent() *CopilotAgent {
	return &CopilotAgent{}
}

func (a *CopilotAgent) Type() agentx.AgentType {
	return agentx.AgentTypeCopilot
}

func (a *CopilotAgent) Name() string {
	return "GitHub Copilot"
}

func (a *CopilotAgent) URL() string {
	return "https://github.com/features/copilot"

}

func (a *CopilotAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if GitHub Copilot is the active agent.
//
// Detection methods:
//   - COPILOT_AGENT=1 (future standard)
//   - AGENT_ENV=copilot or github-copilot
//
// Note: Copilot agent mode runs in GitHub's backend, so local detection
// may not always work. When running in Copilot's cloud environment,
// detection will rely on AGENT_ENV being explicitly set.
func (a *CopilotAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check explicit COPILOT_AGENT env var
	if env.GetEnv("COPILOT_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	agentEnv := env.GetEnv("AGENT_ENV")
	switch agentEnv {
	case "copilot", "github-copilot":
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the GitHub Copilot user configuration directory.
func (a *CopilotAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "github-copilot"), nil
}

// ProjectConfigPath returns the GitHub Copilot project configuration directory.
func (a *CopilotAgent) ProjectConfigPath() string {
	return ".github"
}

// ContextFiles returns the context/instruction files Copilot supports.
func (a *CopilotAgent) ContextFiles() []string {
	return []string{".github/copilot-instructions.md"}
}

// SupportsXDGConfig returns true as Copilot uses ~/.config/github-copilot.
func (a *CopilotAgent) SupportsXDGConfig() bool {
	return true
}

// Capabilities returns Copilot's supported features.
// Note: Copilot's agent mode has different capabilities than the autocomplete.
func (a *CopilotAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // no hook system yet
		MCPServers:     false, // runs in GitHub backend
		SystemPrompt:   true,  // .github/copilot-instructions.md
		ProjectContext: true,  // reads project context
		CustomCommands: false, // no custom commands
		MinVersion:     "",    // TBD
	}
}

// HookManager returns the hook manager for Copilot.
// Returns nil as Copilot doesn't support hooks yet.
func (a *CopilotAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

// SetHookManager sets the hook manager.
func (a *CopilotAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *CopilotAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *CopilotAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion returns empty string as Copilot is a VS Code extension
// without a standalone CLI for version detection.
func (a *CopilotAgent) DetectVersion(_ context.Context, _ agentx.Environment) string {
	return ""
}

// IsInstalled checks if GitHub Copilot is installed on the system.
// Checks: gh copilot extension or config directory exists.
func (a *CopilotAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if gh CLI is in PATH (Copilot is a gh extension)
	if _, err := env.LookPath("gh"); err == nil {
		// Could also check `gh extension list` for copilot but that's slow
		return true, nil
	}

	// Fallback: check if config directory exists
	configPath, err := a.UserConfigPath(env)
	if err != nil {
		return false, nil
	}
	if env.IsDir(configPath) {
		return true, nil
	}

	return false, nil
}

// Ensure CopilotAgent implements Agent.
var _ agentx.Agent = (*CopilotAgent)(nil)
