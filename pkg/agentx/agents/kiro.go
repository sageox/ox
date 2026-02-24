package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// KiroAgent implements Agent for Kiro (AWS).
type KiroAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewKiroAgent creates a new Kiro agent.
func NewKiroAgent() *KiroAgent {
	return &KiroAgent{}
}

func (a *KiroAgent) Type() agentx.AgentType {
	return agentx.AgentTypeKiro
}

func (a *KiroAgent) Name() string {
	return "Kiro"
}

func (a *KiroAgent) URL() string {
	return "https://kiro.dev"

}

func (a *KiroAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Kiro is the active agent.
//
// Detection methods:
//   - KIRO_AGENT=1 or KIRO=1
//   - AGENT_ENV=kiro
func (a *KiroAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check KIRO env vars
	if env.GetEnv("KIRO_AGENT") == "1" || env.GetEnv("KIRO") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "kiro" {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Kiro user configuration directory.
func (a *KiroAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kiro"), nil
}

// ProjectConfigPath returns the Kiro project configuration directory.
func (a *KiroAgent) ProjectConfigPath() string {
	return ".kiro"
}

// ContextFiles returns the context/instruction files Kiro supports.
// Kiro is backwards compatible with Amazon Q rules.
func (a *KiroAgent) ContextFiles() []string {
	return []string{".kiro/rules", ".amazonq/rules"}
}

// SupportsXDGConfig returns false as Kiro uses ~/.kiro instead of XDG paths.
func (a *KiroAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Kiro's supported features.
func (a *KiroAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          true,  // supports hooks/rules
		MCPServers:     true,  // MCP support
		SystemPrompt:   true,  // custom instructions
		ProjectContext: true,  // reads project context
		CustomCommands: false, // TBD
		MinVersion:     "",
	}
}

func (a *KiroAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *KiroAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *KiroAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *KiroAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion returns empty string as Kiro's version detection is not yet implemented.
func (a *KiroAgent) DetectVersion(_ context.Context, _ agentx.Environment) string {
	return ""
}

// IsInstalled checks if Kiro is installed on the system.
// Checks: kiro binary in PATH, macOS app bundle, or config directory.
func (a *KiroAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if kiro is in PATH
	if _, err := env.LookPath("kiro"); err == nil {
		return true, nil
	}

	// Check for macOS application bundle
	if env.GOOS() == "darwin" {
		if env.IsDir("/Applications/Kiro.app") {
			return true, nil
		}
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

var _ agentx.Agent = (*KiroAgent)(nil)
