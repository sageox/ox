package agents

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// CodexAgent implements Agent for OpenAI Codex CLI.
type CodexAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewCodexAgent creates a new Codex agent.
func NewCodexAgent() *CodexAgent {
	return &CodexAgent{}
}

func (a *CodexAgent) Type() agentx.AgentType {
	return agentx.AgentTypeCodex
}

func (a *CodexAgent) Name() string {
	return "Codex"
}

func (a *CodexAgent) URL() string {
	return "https://github.com/openai/codex"
}

func (a *CodexAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Codex is the active agent.
//
// Detection precedence:
//   - AGENT_ENV=codex (manual override)
//   - Native Codex runtime env vars (CODEX_CI, CODEX_SANDBOX, CODEX_THREAD_ID)
//   - Secondary hints (.codex directory in cwd/PWD)
func (a *CodexAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Explicit AGENT_ENV takes precedence over all native/runtime heuristics.
	agentEnv := strings.ToLower(strings.TrimSpace(env.GetEnv("AGENT_ENV")))
	if agentEnv != "" {
		return agentEnv == "codex", nil
	}

	if env.GetEnv("CODEX_CI") != "" || env.GetEnv("CODEX_SANDBOX") != "" || env.GetEnv("CODEX_THREAD_ID") != "" {
		return true, nil
	}

	// Secondary hints for environments where Codex vars are unavailable.
	if env.IsDir(".codex") {
		return true, nil
	}
	if pwd := env.GetEnv("PWD"); pwd != "" && env.IsDir(filepath.Join(pwd, ".codex")) {
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Codex user configuration directory.
func (a *CodexAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// ProjectConfigPath returns the Codex project configuration directory.
func (a *CodexAgent) ProjectConfigPath() string {
	return ".codex"
}

// ContextFiles returns the context/instruction files Codex supports.
func (a *CodexAgent) ContextFiles() []string {
	return []string{"AGENTS.md"}
}

// SupportsXDGConfig returns false as Codex typically uses ~/.codex.
func (a *CodexAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Codex's supported features.
func (a *CodexAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // Codex reads AGENTS.md directly
		MCPServers:     false, // no MCP management through ox in this integration
		SystemPrompt:   true,  // project guidance via AGENTS.md
		ProjectContext: true,  // AGENTS.md
		CustomCommands: false,
		MinVersion:     "",
	}
}

func (a *CodexAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *CodexAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *CodexAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *CodexAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Codex version.
// Runs: codex --version
func (a *CodexAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "codex", "--version")
}

// IsInstalled checks if Codex is installed.
// Checks for codex binary in PATH or ~/.codex config directory.
func (a *CodexAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	if _, err := env.LookPath("codex"); err == nil {
		return true, nil
	}

	configPath, err := a.UserConfigPath(env)
	if err != nil {
		return false, nil
	}
	if env.IsDir(configPath) {
		return true, nil
	}

	return false, nil
}

func (a *CodexAgent) SupportsSession() bool { return true }
func (a *CodexAgent) SessionID(env agentx.Environment) string {
	return env.GetEnv("CODEX_THREAD_ID")
}

var _ agentx.Agent = (*CodexAgent)(nil)
