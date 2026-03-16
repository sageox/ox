package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sageox/agentx"
	_ "github.com/sageox/agentx/setup"
	"github.com/sageox/ox/internal/cli"
)

// removeClaudeCommands removes ox slash commands from .claude/commands/.
func removeClaudeCommands(gitRoot string) error {
	agent, ok := agentx.DefaultRegistry.Get(agentx.AgentTypeClaudeCode)
	if !ok {
		return nil
	}

	cm := agent.CommandManager()
	if cm == nil {
		return nil
	}

	if uninstallDryRun {
		cmdDir := cm.CommandDir(gitRoot)
		slog.Info("would remove ox commands", "dir", cmdDir)
		return nil
	}

	ctx := context.Background()
	removed, err := cm.Uninstall(ctx, gitRoot, "ox")
	if err != nil {
		return fmt.Errorf("uninstall Claude commands: %w", err)
	}

	if len(removed) > 0 {
		cli.PrintSuccess(fmt.Sprintf("Removed %d Claude command(s)", len(removed)))
	}

	return nil
}
