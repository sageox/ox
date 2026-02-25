package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// ClaudeCodeAgent implements Agent for Claude Code.
type ClaudeCodeAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewClaudeCodeAgent creates a new Claude Code agent.
func NewClaudeCodeAgent() *ClaudeCodeAgent {
	return &ClaudeCodeAgent{}
}

func (a *ClaudeCodeAgent) Type() agentx.AgentType {
	return agentx.AgentTypeClaudeCode
}

func (a *ClaudeCodeAgent) Name() string {
	return "Claude Code"
}

func (a *ClaudeCodeAgent) URL() string {
	return "https://github.com/anthropics/claude-code"
}

func (a *ClaudeCodeAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Claude Code is the active agent.
//
// Detection methods:
//   - CLAUDECODE=1 (set by Claude Code)
//   - CLAUDE_CODE_ENTRYPOINT (set by Claude Code, e.g., "cli")
//   - CLAUDE_CODE_SESSION_ID (set by Claude Code for session tracking)
//   - AGENT_ENV=claude-code or claudecode or claude
//
// Note on hook timing: Claude Code runs SessionStart/PreCompact hooks BEFORE
// setting CLAUDECODE=1 in the subprocess environment. This means hooks that
// run agent detection may fail even though they're running inside Claude Code.
// CLAUDE_CODE_ENTRYPOINT appears to be set earlier, providing a fallback.
// For maximum reliability, hooks should also set AGENT_ENV explicitly:
//
//	"command": "AGENT_ENV=claude-code <your-command>"
func (a *ClaudeCodeAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check CLAUDECODE env var (set by Claude Code itself)
	if env.GetEnv("CLAUDECODE") == "1" {
		return true, nil
	}

	// Check CLAUDE_CODE_ENTRYPOINT (set by Claude Code, values: "cli", etc.)
	// This env var may be set earlier than CLAUDECODE=1, making it useful
	// for detection during hook execution when CLAUDECODE isn't yet available.
	if env.GetEnv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return true, nil
	}

	// Check CLAUDE_CODE_SESSION_ID (set by Claude Code for session tracking)
	if env.GetEnv("CLAUDE_CODE_SESSION_ID") != "" {
		return true, nil
	}

	// Check explicit AGENT_ENV (manual override or set by hooks)
	agentEnv := env.GetEnv("AGENT_ENV")
	switch agentEnv {
	case "claude-code", "claudecode", "claude":
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Claude Code user configuration directory (~/.claude).
func (a *ClaudeCodeAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// ProjectConfigPath returns the Claude Code project configuration directory.
func (a *ClaudeCodeAgent) ProjectConfigPath() string {
	return ".claude"
}

// ContextFiles returns the context/instruction files Claude Code supports.
func (a *ClaudeCodeAgent) ContextFiles() []string {
	return []string{"CLAUDE.md", "AGENTS.md"}
}

// SupportsXDGConfig returns false as Claude Code uses ~/.claude instead of XDG paths.
func (a *ClaudeCodeAgent) SupportsXDGConfig() bool {
	return false
}

// Capabilities returns Claude Code's supported features.
func (a *ClaudeCodeAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          true,
		MCPServers:     true,
		SystemPrompt:   true,
		ProjectContext: true,  // CLAUDE.md
		CustomCommands: true,  // .claude/commands/
		MinVersion:     "1.0", // when hooks were introduced
	}
}

// HookManager returns the hook manager for Claude Code.
func (a *ClaudeCodeAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

// SetHookManager sets the hook manager (used during registration).
func (a *ClaudeCodeAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

// CommandManager returns the command manager for Claude Code.
func (a *ClaudeCodeAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

// SetCommandManager sets the command manager (used during registration).
func (a *ClaudeCodeAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion attempts to determine the installed Claude Code version.
// Runs: claude --version
func (a *ClaudeCodeAgent) DetectVersion(ctx context.Context, env agentx.Environment) string {
	return versionFromCommand(ctx, env, "claude", "--version")
}

// IsInstalled checks if Claude Code is installed on the system.
// Checks: claude binary in PATH, or ~/.claude config directory exists.
func (a *ClaudeCodeAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if claude CLI is in PATH
	if _, err := env.LookPath("claude"); err == nil {
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

// Ensure ClaudeCodeAgent implements Agent.
var _ agentx.Agent = (*ClaudeCodeAgent)(nil)
