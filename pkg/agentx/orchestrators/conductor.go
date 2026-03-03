package orchestrators

import (
	"context"
	"path/filepath"

	"github.com/sageox/ox/pkg/agentx"
)

// ConductorAgent implements Agent for the Conductor orchestrator.
// Conductor is a Mac app that runs multiple coding agents in parallel.
// https://conductor.build
type ConductorAgent struct{}

func NewConductorAgent() *ConductorAgent {
	return &ConductorAgent{}
}

func (a *ConductorAgent) Type() agentx.AgentType { return agentx.AgentTypeConductor }
func (a *ConductorAgent) Name() string           { return "Conductor" }
func (a *ConductorAgent) URL() string            { return "https://conductor.build" }
func (a *ConductorAgent) Role() agentx.AgentRole { return agentx.RoleOrchestrator }

// Detect checks if running inside a Conductor workspace.
//
// Detection methods:
//   - ORCHESTRATOR_ENV=conductor (explicit override)
//   - CONDUCTOR_WORKSPACE_NAME set (always present in Conductor sessions)
//   - CONDUCTOR_WORKSPACE_PATH set (fallback)
//   - __CFBundleIdentifier=com.conductor.app (macOS app bundle)
func (a *ConductorAgent) Detect(_ context.Context, env agentx.Environment) (bool, error) {
	if env.GetEnv("ORCHESTRATOR_ENV") == "conductor" {
		return true, nil
	}
	if env.GetEnv("CONDUCTOR_WORKSPACE_NAME") != "" {
		return true, nil
	}
	if env.GetEnv("CONDUCTOR_WORKSPACE_PATH") != "" {
		return true, nil
	}
	if env.GetEnv("__CFBundleIdentifier") == "com.conductor.app" {
		return true, nil
	}
	return false, nil
}

func (a *ConductorAgent) UserConfigPath(env agentx.Environment) (string, error) {
	home, err := env.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "com.conductor.app"), nil
}

func (a *ConductorAgent) ProjectConfigPath() string { return ".context" }
func (a *ConductorAgent) ContextFiles() []string    { return nil }
func (a *ConductorAgent) SupportsXDGConfig() bool   { return false }

func (a *ConductorAgent) Capabilities() agentx.Capabilities {
	return agentx.Capabilities{}
}

func (a *ConductorAgent) HookManager() agentx.HookManager       { return nil }
func (a *ConductorAgent) CommandManager() agentx.CommandManager   { return nil }
func (a *ConductorAgent) DetectVersion(_ context.Context, _ agentx.Environment) string { return "" }

func (a *ConductorAgent) IsInstalled(_ context.Context, env agentx.Environment) (bool, error) {
	if env.GetEnv("CONDUCTOR_BIN_DIR") != "" {
		return true, nil
	}
	if env.GetEnv("__CFBundleIdentifier") == "com.conductor.app" {
		return true, nil
	}
	return false, nil
}

func (a *ConductorAgent) SupportsSession() bool                 { return false }
func (a *ConductorAgent) SessionID(_ agentx.Environment) string { return "" }

var _ agentx.Agent = (*ConductorAgent)(nil)
