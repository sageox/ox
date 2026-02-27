package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// ClineAgent implements Agent for Cline (formerly Claude Dev).
// Cline is a VS Code extension for AI-assisted coding.
// https://github.com/cline/cline
type ClineAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewClineAgent creates a new Cline agent.
func NewClineAgent() *ClineAgent {
	return &ClineAgent{}
}

func (a *ClineAgent) Type() agentx.AgentType {
	return agentx.AgentTypeCline
}

func (a *ClineAgent) Name() string {
	return "Cline"
}

func (a *ClineAgent) URL() string {
	return "https://github.com/cline/cline"

}

func (a *ClineAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Cline is the active agent.
//
// Detection methods:
//   - CLINE=1 or CLINE_AGENT=1
//   - AGENT_ENV=cline
//   - VS Code extension context (heuristic)
func (a *ClineAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check explicit CLINE env vars
	if env.GetEnv("CLINE") == "1" || env.GetEnv("CLINE_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	agentEnv := strings.ToLower(env.GetEnv("AGENT_ENV"))
	if agentEnv == "cline" || agentEnv == "claude-dev" {
		return true, nil
	}

	// Note: VSCODE_PID indicates VS Code environment but isn't specific to Cline.
	// More specific detection would require checking the extension list.
	// For now, rely on explicit env vars for accurate detection.

	return false, nil
}

// UserConfigPath returns the Cline user configuration directory.
// Cline stores config in VS Code's extension storage.
func (a *ClineAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}

	// Cline uses VS Code extension storage
	// On macOS: ~/Library/Application Support/Code/User/globalStorage/saoudrizwan.claude-dev
	// On Linux: ~/.config/Code/User/globalStorage/saoudrizwan.claude-dev
	// On Windows: %APPDATA%/Code/User/globalStorage/saoudrizwan.claude-dev
	switch env.GOOS() {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "saoudrizwan.claude-dev"), nil
	case "windows":
		appData := env.GetEnv("APPDATA")
		if appData != "" {
			return filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "Code", "User", "globalStorage", "saoudrizwan.claude-dev"), nil
	default:
		return filepath.Join(home, ".config", "Code", "User", "globalStorage", "saoudrizwan.claude-dev"), nil
	}
}

// ProjectConfigPath returns the Cline project configuration directory.
func (a *ClineAgent) ProjectConfigPath() string {
	return ".cline"
}

// ContextFiles returns the context/instruction files Cline supports.
func (a *ClineAgent) ContextFiles() []string {
	return []string{".clinerules", ".cline/instructions.md"}
}

// SupportsXDGConfig returns true on Linux as VS Code follows XDG on Linux.
func (a *ClineAgent) SupportsXDGConfig() bool {
	return true
}

// Capabilities returns Cline's supported features.
func (a *ClineAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          true, // Cline supports hooks (v3.36+)
		MCPServers:     true,  // Cline supports MCP servers
		SystemPrompt:   true,  // Custom instructions
		ProjectContext: true,  // .clinerules at project level
		CustomCommands: false, // No custom commands
		MinVersion:     "",
	}
}

// HookManager returns the hook manager for Cline.
func (a *ClineAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

// SetHookManager sets the hook manager.
func (a *ClineAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *ClineAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *ClineAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion returns empty string as Cline is a VS Code extension
// without a standalone CLI for version detection.
func (a *ClineAgent) DetectVersion(_ context.Context, _ agentx.Environment) string {
	return ""
}

// IsInstalled checks if Cline is installed.
// Checks for VS Code extension storage directory.
func (a *ClineAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if VS Code extension storage exists
	configPath, err := a.UserConfigPath(env)
	if err != nil {
		return false, nil
	}
	if env.IsDir(configPath) {
		return true, nil
	}

	// Check if VS Code is installed (suggests possible Cline)
	if _, err := env.LookPath("code"); err == nil {
		// VS Code is installed, Cline might be available
		// Can't definitively say Cline is installed without checking extensions
		return false, nil
	}

	return false, nil
}

// EventPhases returns Cline's native event-to-phase mapping.
// Reference: https://docs.cline.bot/features/hooks
func (a *ClineAgent) EventPhases() agentx.EventPhaseMap {
	return agentx.EventPhaseMap{
		agentx.ClineEventTaskStart:        agentx.PhaseStart,
		agentx.ClineEventTaskResume:       agentx.PhaseStart,
		agentx.ClineEventTaskCancel:       agentx.PhaseEnd,
		agentx.ClineEventTaskComplete:     agentx.PhaseEnd,
		agentx.ClineEventPreToolUse:       agentx.PhaseBeforeTool,
		agentx.ClineEventPostToolUse:      agentx.PhaseAfterTool,
		agentx.ClineEventUserPromptSubmit: agentx.PhasePrompt,
		agentx.ClineEventPreCompact:       agentx.PhaseCompact,
	}
}

// AgentENVAliases returns the AGENT_ENV values that identify Cline.
func (a *ClineAgent) AgentENVAliases() []string {
	return []string{"cline", "claude-dev"}
}

// Ensure ClineAgent implements Agent and LifecycleEventMapper.
var _ agentx.Agent = (*ClineAgent)(nil)
var _ agentx.LifecycleEventMapper = (*ClineAgent)(nil)
