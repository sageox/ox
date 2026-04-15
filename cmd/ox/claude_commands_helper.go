package main

import (
	"github.com/sageox/agentx"
	"github.com/sageox/agentx/commands"
	_ "github.com/sageox/agentx/setup"
)

// oxStampPrefix is the stamp prefix used for ox command files.
// ox uses "ox" (not the agentx default "agentx") to match existing installations.
const oxStampPrefix = "ox"

// getClaudeCommandManager returns the Claude Code CommandManager with the ox stamp prefix.
// Returns nil, false if Claude Code agent is not registered or has no command manager.
func getClaudeCommandManager() (agentx.CommandManager, bool) {
	agent, ok := agentx.DefaultRegistry.Get(agentx.AgentTypeClaudeCode)
	if !ok {
		return nil, false
	}

	cm := agent.CommandManager()
	if cm == nil {
		return nil, false
	}

	// override stamp prefix to match existing installed files (ox-hash, not agentx-hash)
	if ccm, ok := cm.(*commands.ClaudeCodeCommandManager); ok {
		ccm.StampPrefix = oxStampPrefix
	}

	return cm, true
}
