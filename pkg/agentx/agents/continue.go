package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// ContinueAgent implements Agent for Continue (https://continue.dev).
type ContinueAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewContinueAgent creates a new Continue agent.
func NewContinueAgent() *ContinueAgent {
	return &ContinueAgent{}
}

func (a *ContinueAgent) Type() agentx.AgentType {
	return agentx.AgentTypeContinue
}

func (a *ContinueAgent) Name() string {
	return "Continue"
}

func (a *ContinueAgent) URL() string {
	return "https://github.com/continuedev/continue"

}

func (a *ContinueAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Continue is the active agent.
//
// Detection methods:
//   - CONTINUE_AGENT=1
//   - AGENT_ENV=continue
func (a *ContinueAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check CONTINUE env var
	if env.GetEnv("CONTINUE_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "continue" {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Continue user configuration directory.
func (a *ContinueAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".continue"), nil
}

// ProjectConfigPath returns the Continue project configuration directory.
func (a *ContinueAgent) ProjectConfigPath() string {
	return ".continue"
}

// ContextFiles returns the context/instruction files Continue supports.
func (a *ContinueAgent) ContextFiles() []string {
	return []string{".continuerc.json"}
}

// SupportsXDGConfig returns false as Continue uses ~/.continue instead of XDG paths.
func (a *ContinueAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Continue's supported features.
func (a *ContinueAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // VS Code/JetBrains extension
		MCPServers:     false, // no MCP yet
		SystemPrompt:   true,  // config.json system message
		ProjectContext: true,  // .continuerc.json
		CustomCommands: true,  // slash commands
		MinVersion:     "",
	}
}

func (a *ContinueAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *ContinueAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *ContinueAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *ContinueAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion returns empty string as Continue is primarily an IDE extension.
func (a *ContinueAgent) DetectVersion(_ context.Context, _ agentx.Environment) string {
	return ""
}

// IsInstalled checks if Continue is installed on the system.
// Checks: continue binary in PATH or config directory exists.
func (a *ContinueAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if continue is in PATH (note: may conflict with shell builtin)
	if _, err := env.LookPath("continue"); err == nil {
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

var _ agentx.Agent = (*ContinueAgent)(nil)
