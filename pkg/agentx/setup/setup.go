// Package setup provides initialization for the agentx package.
// Import this package to register all default agents with the registry.
//
// Usage:
//
//	import _ "github.com/sageox/ox/pkg/agentx/setup"
package setup

import (
	"github.com/sageox/ox/pkg/agentx"
	"github.com/sageox/ox/pkg/agentx/agents"
	"github.com/sageox/ox/pkg/agentx/commands"
	"github.com/sageox/ox/pkg/agentx/hooks"
	"github.com/sageox/ox/pkg/agentx/orchestrators"
)

func init() {
	RegisterDefaultAgents()
}

// RegisterDefaultAgents registers all supported agents with the default registry.
// This is called automatically when this package is imported.
func RegisterDefaultAgents() {
	env := agentx.NewSystemEnvironment()

	// Claude Code with hook manager and command manager
	claudeCode := agents.NewClaudeCodeAgent()
	claudeCode.SetHookManager(hooks.NewClaudeCodeHookManager(env))
	claudeCode.SetCommandManager(commands.NewClaudeCodeCommandManager())
	agentx.DefaultRegistry.Register(claudeCode)

	// Cursor
	agentx.DefaultRegistry.Register(agents.NewCursorAgent())

	// Windsurf
	agentx.DefaultRegistry.Register(agents.NewWindsurfAgent())

	// Copilot
	agentx.DefaultRegistry.Register(agents.NewCopilotAgent())

	// Aider
	agentx.DefaultRegistry.Register(agents.NewAiderAgent())

	// Cody
	agentx.DefaultRegistry.Register(agents.NewCodyAgent())

	// Continue
	agentx.DefaultRegistry.Register(agents.NewContinueAgent())

	// Code Puppy
	agentx.DefaultRegistry.Register(agents.NewCodePuppyAgent())

	// Kiro
	agentx.DefaultRegistry.Register(agents.NewKiroAgent())

	// OpenCode
	agentx.DefaultRegistry.Register(agents.NewOpenCodeAgent())

	// Codex
	agentx.DefaultRegistry.Register(agents.NewCodexAgent())

	// Goose
	agentx.DefaultRegistry.Register(agents.NewGooseAgent())

	// Amp
	agentx.DefaultRegistry.Register(agents.NewAmpAgent())

	// Cline
	agentx.DefaultRegistry.Register(agents.NewClineAgent())

	// Droid (Factory.ai)
	agentx.DefaultRegistry.Register(agents.NewDroidAgent())

	// Orchestrators
	agentx.DefaultRegistry.Register(orchestrators.NewOpenClawAgent())
	agentx.DefaultRegistry.Register(orchestrators.NewConductorAgent())
}
