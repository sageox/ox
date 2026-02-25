package orchestrators

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// OpenClawAgent implements Agent for the OpenClaw orchestrator.
// OpenClaw is an open-source AI agent that launches coding agents
// via its exec tool with pty:true for pseudo-terminal allocation.
// https://github.com/openclaw/openclaw
type OpenClawAgent struct{}

func NewOpenClawAgent() *OpenClawAgent {
	return &OpenClawAgent{}
}

func (a *OpenClawAgent) Type() agentx.AgentType { return agentx.AgentTypeOpenClaw }
func (a *OpenClawAgent) Name() string           { return "OpenClaw" }
func (a *OpenClawAgent) URL() string            { return "https://github.com/openclaw/openclaw" }
func (a *OpenClawAgent) Role() agentx.AgentRole { return agentx.RoleOrchestrator }

// Detect checks if running inside an OpenClaw-managed environment.
//
// Detection methods:
//   - ORCHESTRATOR_ENV=openclaw (explicit override)
//   - OPENCLAW_STATE_DIR set (gateway process env, most reliable)
//   - OPENCLAW_HOME set (config override)
//   - OPENCLAW_GATEWAY_TOKEN set (gateway auth)
func (a *OpenClawAgent) Detect(_ context.Context, env agentx.Environment) (bool, error) {
	if env.GetEnv("ORCHESTRATOR_ENV") == "openclaw" {
		return true, nil
	}
	if env.GetEnv("OPENCLAW_STATE_DIR") != "" {
		return true, nil
	}
	if env.GetEnv("OPENCLAW_HOME") != "" {
		return true, nil
	}
	if env.GetEnv("OPENCLAW_GATEWAY_TOKEN") != "" {
		return true, nil
	}
	return false, nil
}

func (a *OpenClawAgent) UserConfigPath(env agentx.Environment) (string, error) {
	if stateDir := env.GetEnv("OPENCLAW_STATE_DIR"); stateDir != "" {
		return stateDir, nil
	}
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".openclaw"), nil
}

func (a *OpenClawAgent) ProjectConfigPath() string { return "" }
func (a *OpenClawAgent) ContextFiles() []string    { return []string{"AGENTS.md"} }
func (a *OpenClawAgent) SupportsXDGConfig() bool   { return false }

func (a *OpenClawAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{
		MCPServers:     true,
		SystemPrompt:   true,
		ProjectContext: true,
	}
}

func (a *OpenClawAgent) HookManager() agentx.HookManager       { return nil }
func (a *OpenClawAgent) CommandManager() agentx.CommandManager   { return nil }

func (a *OpenClawAgent) DetectVersion(_ context.Context, env agentx.Environment) string {
	return versionFromCommand(env, "openclaw", "--version")
}

func (a *OpenClawAgent) IsInstalled(_ context.Context, env agentx.Environment) (bool, error) {
	if _, err := env.LookPath("openclaw"); err == nil {
		return true, nil
	}
	home, err := env.HomeDir()
	if err != nil {
		return false, nil
	}
	if env.IsDir(filepath.Join(home, ".openclaw")) {
		return true, nil
	}
	return false, nil
}

var _ agentx.Agent = (*OpenClawAgent)(nil)
