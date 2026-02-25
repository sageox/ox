package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// GooseAgent implements Agent for Goose by Block (https://block.github.io/goose/).
type GooseAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewGooseAgent creates a new Goose agent.
func NewGooseAgent() *GooseAgent {
	return &GooseAgent{}
}

func (a *GooseAgent) Type() agentx.AgentType {
	return agentx.AgentTypeGoose
}

func (a *GooseAgent) Name() string {
	return "Goose"
}

func (a *GooseAgent) URL() string {
	return "https://github.com/block/goose"

}

func (a *GooseAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Goose is the active agent.
//
// Detection methods:
//   - GOOSE_AGENT=1 or GOOSE=1
//   - AGENT_ENV=goose
//   - Running from goose command (heuristic)
func (a *GooseAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check GOOSE env vars
	if env.GetEnv("GOOSE") == "1" || env.GetEnv("GOOSE_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "goose" {
		return true, nil
	}

	// Heuristic: check if running from goose CLI
	if execPath := env.GetEnv("_"); strings.Contains(strings.ToLower(execPath), "goose") {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Goose user configuration directory.
// Goose uses XDG-compliant paths (~/.config/goose).
func (a *GooseAgent) UserConfigPath(env agentx.Environment) (string, error) {
	configDir, err := env.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "goose"), nil
}

// ProjectConfigPath returns empty as Goose is primarily user-level configuration.
func (a *GooseAgent) ProjectConfigPath() string {
	return ""
}

// ContextFiles returns the context/instruction files Goose supports.
func (a *GooseAgent) ContextFiles() []string {
	return []string{".goose/config.yaml", ".goosehints"}
}

// SupportsXDGConfig returns true as Goose uses ~/.config/goose.
func (a *GooseAgent) SupportsXDGConfig() bool {
	return true
}

// Capabilities returns Goose's supported features.
func (a *GooseAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // CLI-based
		MCPServers:     true,  // supports MCP extensions
		SystemPrompt:   true,  // config.yaml
		ProjectContext: true,  // .goosehints
		CustomCommands: false, // TBD
		MinVersion:     "",
	}
}

func (a *GooseAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *GooseAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *GooseAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *GooseAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Goose version.
// Runs: goose --version
func (a *GooseAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "goose", "--version")
}

// IsInstalled checks if Goose is installed on the system.
// Checks: goose binary in PATH or config directory exists.
func (a *GooseAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if goose is in PATH
	if _, err := env.LookPath("goose"); err == nil {
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

var _ agentx.Agent = (*GooseAgent)(nil)
