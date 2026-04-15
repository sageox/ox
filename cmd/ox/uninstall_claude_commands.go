package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/cli"
)

// removeClaudeCommands removes ox slash commands from .claude/commands/.
func removeClaudeCommands(gitRoot string) error {
	cm, ok := getClaudeCommandManager()
	if !ok {
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
