package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// AiderAgent implements Agent for Aider (https://aider.chat).
type AiderAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewAiderAgent creates a new Aider agent.
func NewAiderAgent() *AiderAgent {
	return &AiderAgent{}
}

func (a *AiderAgent) Type() agentx.AgentType {
	return agentx.AgentTypeAider
}

func (a *AiderAgent) Name() string {
	return "Aider"
}

func (a *AiderAgent) URL() string {
	return "https://github.com/Aider-AI/aider"

}

func (a *AiderAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Aider is the active agent.
//
// Detection methods:
//   - AIDER=1 or AIDER_AGENT=1
//   - AGENT_ENV=aider
//   - Running from aider command
func (a *AiderAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check AIDER env vars
	if env.GetEnv("AIDER") == "1" || env.GetEnv("AIDER_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "aider" {
		return true, nil
	}

	// Heuristic: check if running from aider
	if execPath := env.GetEnv("_"); strings.Contains(strings.ToLower(execPath), "aider") {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Aider user configuration directory.
func (a *AiderAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aider"), nil
}

// ProjectConfigPath returns the Aider project configuration directory.
func (a *AiderAgent) ProjectConfigPath() string {
	return ".aider"
}

// ContextFiles returns the context/instruction files Aider supports.
func (a *AiderAgent) ContextFiles() []string {
	return []string{".aider.conf.yml", "CONVENTIONS.md"}
}

// SupportsXDGConfig returns false as Aider uses ~/.aider instead of XDG paths.
func (a *AiderAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Aider's supported features.
func (a *AiderAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // CLI-based, no hooks
		MCPServers:     false, // no MCP support
		SystemPrompt:   true,  // .aider.conf.yml, conventions files
		ProjectContext: true,  // reads .aider* files
		CustomCommands: true,  // /commands
		MinVersion:     "",
	}
}

func (a *AiderAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *AiderAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *AiderAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *AiderAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Aider version.
// Runs: aider --version
func (a *AiderAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "aider", "--version")
}

// IsInstalled checks if Aider is installed on the system.
// Checks: aider binary in PATH or config directory exists.
func (a *AiderAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if aider is in PATH
	if _, err := env.LookPath("aider"); err == nil {
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

var _ agentx.Agent = (*AiderAgent)(nil)
