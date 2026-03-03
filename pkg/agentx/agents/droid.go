package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// DroidAgent implements Agent for Factory Droid.
// Droid is Factory.ai's terminal-based coding agent.
// https://factory.ai/
type DroidAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewDroidAgent creates a new Droid agent.
func NewDroidAgent() *DroidAgent {
	return &DroidAgent{}
}

func (a *DroidAgent) Type() agentx.AgentType {
	return agentx.AgentTypeDroid
}

func (a *DroidAgent) Name() string {
	return "Droid"
}

func (a *DroidAgent) URL() string {
	return "https://factory.ai/"

}

func (a *DroidAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Droid is the active agent.
//
// Detection methods:
//   - DROID=1 or DROID_AGENT=1
//   - FACTORY_DROID=1
//   - AGENT_ENV=droid or AGENT_ENV=factory-droid
//   - Running from droid CLI (heuristic)
func (a *DroidAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check explicit DROID env vars
	if env.GetEnv("DROID") == "1" || env.GetEnv("DROID_AGENT") == "1" {
		return true, nil
	}

	// Check Factory-specific env var
	if env.GetEnv("FACTORY_DROID") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	agentEnv := strings.ToLower(env.GetEnv("AGENT_ENV"))
	if agentEnv == "droid" || agentEnv == "factory-droid" || agentEnv == "factory" {
		return true, nil
	}

	// Heuristic: check if running from droid CLI
	if execPath := env.GetEnv("_"); strings.Contains(strings.ToLower(execPath), "droid") {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Droid user configuration directory.
func (a *DroidAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}

	// Check XDG_CONFIG_HOME first
	if xdgConfig := env.GetEnv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "factory"), nil
	}

	// Default locations
	switch env.GOOS() {
	case "darwin":
		return filepath.Join(home, ".config", "factory"), nil
	case "windows":
		appData := env.GetEnv("APPDATA")
		if appData != "" {
			return filepath.Join(appData, "factory"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "factory"), nil
	default:
		return filepath.Join(home, ".config", "factory"), nil
	}
}

// ProjectConfigPath returns the Droid project configuration directory.
func (a *DroidAgent) ProjectConfigPath() string {
	return ".factory"
}

// ContextFiles returns the context/instruction files Droid supports.
func (a *DroidAgent) ContextFiles() []string {
	return []string{"DROID.md", ".factory/instructions.md", "AGENTS.md"}
}

// SupportsXDGConfig returns true as Droid follows XDG paths.
func (a *DroidAgent) SupportsXDGConfig() bool {
	return true
}

// Capabilities returns Droid's supported features.
func (a *DroidAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          true, // Droid supports hooks
		MCPServers:     true, // Droid supports MCP servers
		SystemPrompt:   true, // Custom instructions via DROID.md
		ProjectContext: true, // Project-level context files
		CustomCommands: true, // Slash commands
		MinVersion:     "",
	}
}

// HookManager returns the hook manager for Droid.
func (a *DroidAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

// SetHookManager sets the hook manager.
func (a *DroidAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *DroidAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *DroidAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Droid version.
// Runs: droid --version
func (a *DroidAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "droid", "--version")
}

// IsInstalled checks if Droid is installed.
// Checks for droid binary in PATH or config directory.
func (a *DroidAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if droid CLI is in PATH
	if _, err := env.LookPath("droid"); err == nil {
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

// EventPhases returns Droid's native event-to-phase mapping.
// Reference: https://docs.factory.ai/reference/hooks-reference
func (a *DroidAgent) EventPhases() agentx.EventPhaseMap {
	return agentx.EventPhaseMap{
		agentx.HookEventSessionStart:     agentx.PhaseStart,
		agentx.HookEventSessionEnd:       agentx.PhaseEnd,
		agentx.HookEventPreToolUse:       agentx.PhaseBeforeTool,
		agentx.HookEventPostToolUse:      agentx.PhaseAfterTool,
		agentx.HookEventUserPromptSubmit: agentx.PhasePrompt,
		agentx.HookEventStop:             agentx.PhaseStop,
		agentx.HookEventPreCompact:       agentx.PhaseCompact,
	}
}

// AgentENVAliases returns the AGENT_ENV values that identify Droid.
func (a *DroidAgent) AgentENVAliases() []string {
	return []string{"droid", "factory-droid", "factory"}
}

// SupportsSession returns true; Droid provides session IDs via hook stdin JSON.
func (a *DroidAgent) SupportsSession() bool                 { return true }
func (a *DroidAgent) SessionID(_ agentx.Environment) string { return "" }

// Ensure DroidAgent implements Agent and LifecycleEventMapper.
var _ agentx.Agent = (*DroidAgent)(nil)
var _ agentx.LifecycleEventMapper = (*DroidAgent)(nil)
