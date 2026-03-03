package agents

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// CodePuppyAgent implements Agent for Code Puppy.
type CodePuppyAgent struct {
	hookManager    agentx.HookManager
	commandManager agentx.CommandManager
}

// NewCodePuppyAgent creates a new Code Puppy agent.
func NewCodePuppyAgent() *CodePuppyAgent {
	return &CodePuppyAgent{}
}

func (a *CodePuppyAgent) Type() agentx.AgentType {
	return agentx.AgentTypeCodePuppy
}

func (a *CodePuppyAgent) Name() string {
	return "Code Puppy"
}

func (a *CodePuppyAgent) URL() string {
	return "https://github.com/codepuppy-ai/codepuppy"

}

func (a *CodePuppyAgent) Role() agentx.AgentRole { return agentx.RoleAgent }

// Detect checks if Code Puppy is the active agent.
//
// Detection methods:
//   - CODE_PUPPY=1 or CODE_PUPPY_AGENT=1
//   - AGENT_ENV=code-puppy or codepuppy
func (a *CodePuppyAgent) Detect(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check CODE_PUPPY env vars
	if env.GetEnv("CODE_PUPPY") == "1" || env.GetEnv("CODE_PUPPY_AGENT") == "1" {
		return true, nil
	}

	// Check AGENT_ENV
	agentEnv := env.GetEnv("AGENT_ENV")
	switch agentEnv {
	case "code-puppy", "codepuppy":
		return true, nil
	}

	return false, nil
}

// UserConfigPath returns the Code Puppy user configuration directory.
func (a *CodePuppyAgent) UserConfigPath(env agentx.Environment) (string, error) {
	configDir, err := env.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "code-puppy"), nil
}

// ProjectConfigPath returns the Code Puppy project configuration directory.
func (a *CodePuppyAgent) ProjectConfigPath() string {
	return ".codepuppy"
}

// ContextFiles returns the context/instruction files Code Puppy supports.
func (a *CodePuppyAgent) ContextFiles() []string {
	return []string{".codepuppy/config.json"}
}

// SupportsXDGConfig returns true as Code Puppy uses ~/.config/code-puppy.
func (a *CodePuppyAgent) SupportsXDGConfig() bool {
	return true
}

// Capabilities returns Code Puppy's supported features.
func (a *CodePuppyAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		Hooks:          false, // no hook system
		MCPServers:     false, // TBD
		SystemPrompt:   true,  // custom instructions
		ProjectContext: true,  // reads project context
		CustomCommands: false, // TBD
		MinVersion:     "",
	}
}

func (a *CodePuppyAgent) HookManager() agentx.HookManager {
	return a.hookManager
}

func (a *CodePuppyAgent) SetHookManager(hm agentx.HookManager) {
	a.hookManager = hm
}

func (a *CodePuppyAgent) CommandManager() agentx.CommandManager {
	return a.commandManager
}

func (a *CodePuppyAgent) SetCommandManager(cm agentx.CommandManager) {
	a.commandManager = cm
}

// DetectVersion returns empty string as Code Puppy's version detection is not yet implemented.
func (a *CodePuppyAgent) DetectVersion(_ context.Context, _ agentx.Environment) string {
	return ""
}

// IsInstalled checks if Code Puppy is installed on the system.
// Checks: codepuppy binary in PATH or config directory exists.
func (a *CodePuppyAgent) IsInstalled(ctx context.Context, env agentx.Environment) (bool, error) {
	// Check if codepuppy is in PATH
	if _, err := env.LookPath("codepuppy"); err == nil {
		return true, nil
	}

	// Also check code-puppy
	if _, err := env.LookPath("code-puppy"); err == nil {
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

func (a *CodePuppyAgent) SupportsSession() bool                 { return false }
func (a *CodePuppyAgent) SessionID(_ agentx.Environment) string { return "" }

var _ agentx.Agent = (*CodePuppyAgent)(nil)
