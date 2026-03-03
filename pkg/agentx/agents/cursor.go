package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// CursorAgent implements Agent for Cursor.
type CursorAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewCursorAgent creates a new Cursor agent.
func NewCursorAgent() *CursorAgent {
	return &CursorAgent{}
}

func (a *CursorAgent) Type() agentx.AgentType {
	return agentx.AgentTypeCursor
}

func (a *CursorAgent) Name() string {
	return "Cursor"
}

func (a *CursorAgent) URL() string {
	return "https://github.com/getcursor/cursor"

}

func (a *CursorAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Cursor is the active agent.
//
// Detection methods:
//   - CURSOR_AGENT=1 (future standard)
//   - AGENT_ENV=cursor
//   - Running from cursor CLI (heuristic)
func (a *CursorAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check explicit CURSOR_AGENT env var (future standard)
	if env.GetEnv("CURSOR_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	if env.GetEnv("AGENT_ENV") == "cursor" {
		return true, nil
	}

	// Heuristic: check if running from cursor CLI
	// The _ env var often contains the path of the executing binary
	if execPath := env.GetEnv("_"); strings.Contains(strings.ToLower(execPath), "cursor") {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Cursor user configuration directory (~/.cursor).
func (a *CursorAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor"), nil
}

// ProjectConfigPath returns the Cursor project configuration directory.
func (a *CursorAgent) ProjectConfigPath() string {
	return ".cursor"
}

// ContextFiles returns the context/instruction files Cursor supports.
func (a *CursorAgent) ContextFiles() []string {
	return []string{".cursorrules"}
}

// SupportsXDGConfig returns false as Cursor uses ~/.cursor instead of XDG paths.
func (a *CursorAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Cursor's supported features.
func (a *CursorAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          true,  // .cursor/rules/
		MCPServers:     true,  // cursor supports MCP
		SystemPrompt:   true,  // .cursorrules
		ProjectContext: true,  // .cursorrules at project level
		CustomCommands: false, // no custom commands yet
		MinVersion:     "",    // TBD
	}
}

// HookManager returns the hook manager for Cursor.
func (a *CursorAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

// SetHookManager sets the hook manager.
func (a *CursorAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *CursorAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *CursorAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Cursor version.
// Reads ~/.cursor/package.json for the version field.
func (a *CursorAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	configPath, err := a.UserConfigPath(env)
	if err != nil {
		return ""
	}
	return versionFromPackageJSON(env, filepath.Join(configPath, "package.json"))
}

// IsInstalled checks if Cursor is installed on the system.
// Checks: cursor binary in PATH, macOS app bundle, or ~/.cursor config directory.
func (a *CursorAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if cursor CLI is in PATH
	if _, err := env.LookPath("cursor"); err == nil {
		return true, nil
	}

	// Check for macOS application bundle
	if env.GOOS() == "darwin" {
		if env.IsDir("/Applications/Cursor.app") {
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

// EventPhases returns Cursor's native event-to-phase mapping.
// Reference: https://cursor.com/docs/agent/hooks
func (a *CursorAgent) EventPhases() agentx.EventPhaseMap {
	return agentx.EventPhaseMap{
		agentx.CursorEventSessionStart:       agentx.PhaseStart,
		agentx.CursorEventSessionEnd:         agentx.PhaseEnd,
		agentx.CursorEventPreToolUse:         agentx.PhaseBeforeTool,
		agentx.CursorEventPostToolUse:        agentx.PhaseAfterTool,
		agentx.CursorEventBeforeSubmitPrompt: agentx.PhasePrompt,
		agentx.CursorEventStop:               agentx.PhaseStop,
		agentx.CursorEventPreCompact:         agentx.PhaseCompact,
	}
}

// AgentENVAliases returns the AGENT_ENV values that identify Cursor.
func (a *CursorAgent) AgentENVAliases() []string {
	return []string{"cursor"}
}

// SupportsSession returns true; Cursor provides session IDs via hook stdin JSON.
func (a *CursorAgent) SupportsSession() bool                 { return true }
func (a *CursorAgent) SessionID(_ agentx.Environment) string { return "" }

// Ensure CursorAgent implements Agent and LifecycleEventMapper.
var _ agentx.Agent = (*CursorAgent)(nil)
var _ agentx.LifecycleEventMapper = (*CursorAgent)(nil)
