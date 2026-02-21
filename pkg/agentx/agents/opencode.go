package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// OpenCodeAgent implements Agent for OpenCode (https://opencode.ai).
type OpenCodeAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewOpenCodeAgent creates a new OpenCode agent.
func NewOpenCodeAgent() *OpenCodeAgent {
	return &OpenCodeAgent{}
}

func (a *OpenCodeAgent) Type() agentx.AgentType {
	return agentx.AgentTypeOpenCode
}

func (a *OpenCodeAgent) Name() string {
	return "OpenCode"
}

func (a *OpenCodeAgent) URL() string {
	return "https://github.com/opencode-ai/opencode"
}

// Detect checks if OpenCode is the active agent.
//
// Detection methods:
//   - OPENCODE=1 or OPENCODE_AGENT=1
//   - AGENT_ENV=opencode
func (a *OpenCodeAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check OPENCODE env vars
	if env.GetEnv("OPENCODE") == "1" || env.GetEnv("OPENCODE_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "opencode" {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the OpenCode user configuration directory.
func (a *OpenCodeAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".opencode"), nil
}

// ProjectConfigPath returns the OpenCode project configuration directory.
func (a *OpenCodeAgent) ProjectConfigPath() string {
	return ".opencode"
}

// ContextFiles returns the context/instruction files OpenCode supports.
func (a *OpenCodeAgent) ContextFiles() []string {
	return []string{"AGENTS.md"}
}

// SupportsXDGConfig returns false as OpenCode uses ~/.opencode instead of XDG paths.
func (a *OpenCodeAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns OpenCode's supported features.
func (a *OpenCodeAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // TBD
		MCPServers:     true,  // supports MCP
		SystemPrompt:   true,  // custom instructions
		ProjectContext: true,  // AGENTS.md
		CustomCommands: false, // TBD
		MinVersion:     "",
	}
}

func (a *OpenCodeAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *OpenCodeAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *OpenCodeAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *OpenCodeAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed OpenCode version.
// Runs: opencode --version
func (a *OpenCodeAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "opencode", "--version")
}

// IsInstalled checks if OpenCode is installed on the system.
// Checks: opencode binary in PATH or config directory exists.
func (a *OpenCodeAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if opencode is in PATH
	if _, err := env.LookPath("opencode"); err == nil {
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

var _ agentx.Agent = (*OpenCodeAgent)(nil)
