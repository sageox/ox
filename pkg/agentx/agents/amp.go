package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// AmpAgent implements Agent for Amp by Sourcegraph (https://ampcode.io).
type AmpAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewAmpAgent creates a new Amp agent.
func NewAmpAgent() *AmpAgent {
	return &AmpAgent{}
}

func (a *AmpAgent) Type() agentx.AgentType {
	return agentx.AgentTypeAmp
}

func (a *AmpAgent) Name() string {
	return "Amp"
}

func (a *AmpAgent) URL() string {
	return "https://github.com/sourcegraph/amp"

}

func (a *AmpAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Amp is the active agent.
//
// Detection methods:
//   - AMP_AGENT=1 or AMP=1
//   - AGENT_ENV=amp
func (a *AmpAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check AMP env vars
	if env.GetEnv("AMP") == "1" || env.GetEnv("AMP_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "amp" {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Amp user configuration directory (~/.amp).
func (a *AmpAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".amp"), nil
}

// ProjectConfigPath returns empty as Amp is primarily user-level configuration.
func (a *AmpAgent) ProjectConfigPath() string {
	return ""
}

// ContextFiles returns the context/instruction files Amp supports.
func (a *AmpAgent) ContextFiles() []string {
	return []string{"AGENTS.md"}
}

// SupportsXDGConfig returns false as Amp uses ~/.amp instead of XDG paths.
func (a *AmpAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Amp's supported features.
func (a *AmpAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // TBD
		MCPServers:     true,  // supports MCP
		SystemPrompt:   true,  // custom instructions
		ProjectContext: true,  // AGENTS.md
		CustomCommands: false, // TBD
		MinVersion:     "",
	}
}

func (a *AmpAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *AmpAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *AmpAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *AmpAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Amp version.
// Runs: amp --version
func (a *AmpAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "amp", "--version")
}

// IsInstalled checks if Amp is installed on the system.
// Checks: amp binary in PATH or config directory exists.
func (a *AmpAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if amp is in PATH
	if _, err := env.LookPath("amp"); err == nil {
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

var _ agentx.Agent = (*AmpAgent)(nil)
