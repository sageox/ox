package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// WindsurfAgent implements Agent for Windsurf (Codeium).
type WindsurfAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewWindsurfAgent creates a new Windsurf agent.
func NewWindsurfAgent() *WindsurfAgent {
	return &WindsurfAgent{}
}

func (a *WindsurfAgent) Type() agentx.AgentType {
	return agentx.AgentTypeWindsurf
}

func (a *WindsurfAgent) Name() string {
	return "Windsurf"
}

func (a *WindsurfAgent) URL() string {
	return "https://github.com/codeium/windsurf"

}

func (a *WindsurfAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Windsurf is the active agent.
//
// Detection methods:
//   - WINDSURF_AGENT=1 (future standard)
//   - CODEIUM_AGENT=1 (alternative)
//   - AGENT_ENV=windsurf or codeium
func (a *WindsurfAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check explicit WINDSURF_AGENT env var
	if env.GetEnv("WINDSURF_AGENT") == "1" {
		return true, nil
	}

	// Check CODEIUM_AGENT (Windsurf was formerly Codeium)
	if env.GetEnv("CODEIUM_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	agentEnv := env.GetEnv("AGENT_ENV")
	switch agentEnv {
	case "windsurf", "codeium":
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Windsurf user configuration directory (~/.codeium).
func (a *WindsurfAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codeium"), nil
}

// ProjectConfigPath returns the Windsurf project configuration directory.
func (a *WindsurfAgent) ProjectConfigPath() string {
	return ".windsurf"
}

// ContextFiles returns the context/instruction files Windsurf supports.
func (a *WindsurfAgent) ContextFiles() []string {
	return []string{".windsurfrules"}
}

// SupportsXDGConfig returns false as Windsurf uses ~/.codeium instead of XDG paths.
func (a *WindsurfAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Windsurf's supported features.
func (a *WindsurfAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          true,  // supports rules
		MCPServers:     false, // TBD - needs verification
		SystemPrompt:   true,  // cascade memories
		ProjectContext: true,  // reads project context
		CustomCommands: false, // TBD
		MinVersion:     "",    // TBD
	}
}

// HookManager returns the hook manager for Windsurf.
func (a *WindsurfAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

// SetHookManager sets the hook manager.
func (a *WindsurfAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *WindsurfAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *WindsurfAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Windsurf version.
// Reads ~/.codeium/windsurf/package.json for the version field.
func (a *WindsurfAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	configPath, err := a.UserConfigPath(env)
	if err != nil {
		return ""
	}
	// Windsurf stores its app data under the codeium config, try windsurf subdir
	return versionFromPackageJSON(env, filepath.Join(configPath, "windsurf", "package.json"))
}

// IsInstalled checks if Windsurf is installed on the system.
// Checks: windsurf binary in PATH, macOS app bundle, or config directory.
func (a *WindsurfAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if windsurf CLI is in PATH
	if _, err := env.LookPath("windsurf"); err == nil {
		return true, nil
	}

	// Check for macOS application bundle
	if env.GOOS() == "darwin" {
		if env.IsDir("/Applications/Windsurf.app") {
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

// Ensure WindsurfAgent implements Agent.
// EventPhases returns Windsurf's native event-to-phase mapping.
// Reference: https://docs.windsurf.com/windsurf/cascade/hooks
func (a *WindsurfAgent) EventPhases() agentx.EventPhaseMap {
	return agentx.EventPhaseMap{
		agentx.WindsurfEventPreReadCode:          agentx.PhaseBeforeTool,
		agentx.WindsurfEventPostWriteCode:        agentx.PhaseAfterTool,
		agentx.WindsurfEventPreRunCommand:        agentx.PhaseBeforeTool,
		agentx.WindsurfEventPostRunCommand:       agentx.PhaseAfterTool,
		agentx.WindsurfEventPreUserPrompt:        agentx.PhasePrompt,
		agentx.WindsurfEventPostCascadeResponse:  agentx.PhaseStop,
	}
}

// AgentENVAliases returns the AGENT_ENV values that identify Windsurf.
func (a *WindsurfAgent) AgentENVAliases() []string {
	return []string{"windsurf", "codeium"}
}

var _ agentx.Agent = (*WindsurfAgent)(nil)
var _ agentx.LifecycleEventMapper = (*WindsurfAgent)(nil)
