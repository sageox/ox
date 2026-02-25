package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// CodyAgent implements Agent for Sourcegraph Cody.
type CodyAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewCodyAgent creates a new Cody agent.
func NewCodyAgent() *CodyAgent {
	return &CodyAgent{}
}

func (a *CodyAgent) Type() agentx.AgentType {
	return agentx.AgentTypeCody
}

func (a *CodyAgent) Name() string {
	return "Cody"
}

func (a *CodyAgent) URL() string {
	return "https://github.com/sourcegraph/cody"

}

func (a *CodyAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Cody is the active agent.
//
// Detection methods:
//   - CODY_AGENT=1
//   - AGENT_ENV=cody
func (a *CodyAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check CODY env var
	if env.GetEnv("CODY_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "cody" {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Cody user configuration directory.
func (a *CodyAgent) UserConfigPath(env agentx.Environment) (string, error) {
	configDir, err := env.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "cody"), nil
}

// ProjectConfigPath returns the Cody project configuration directory.
func (a *CodyAgent) ProjectConfigPath() string {
	return ".cody"
}

// ContextFiles returns the context/instruction files Cody supports.
func (a *CodyAgent) ContextFiles() []string {
	return []string{".cody/cody.json"}
}

// SupportsXDGConfig returns true as Cody uses ~/.config/cody.
func (a *CodyAgent) SupportsXDGConfig() bool {
	return true
}

// Capabilities returns Cody's supported features.
func (a *CodyAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // VS Code extension
		MCPServers:     false, // no MCP
		SystemPrompt:   true,  // custom instructions
		ProjectContext: true,  // reads codebase
		CustomCommands: true,  // custom commands
		MinVersion:     "",
	}
}

func (a *CodyAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *CodyAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *CodyAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *CodyAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion returns empty string as Cody is primarily a VS Code extension.
func (a *CodyAgent) DetectVersion(_ context.Context, _ agentx.Environment) string {
	return ""
}

// IsInstalled checks if Cody is installed on the system.
// Checks: cody binary in PATH or config directory exists.
func (a *CodyAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if cody is in PATH
	if _, err := env.LookPath("cody"); err == nil {
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

var _ agentx.Agent = (*CodyAgent)(nil)
