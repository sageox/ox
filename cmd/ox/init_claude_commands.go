package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/sageox/agentx"
	_ "github.com/sageox/agentx/setup"
	"github.com/sageox/ox/extensions/claude"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/version"
)

// installClaudeCommands installs ox slash commands to .claude/commands/.
// Returns the list of installed file paths (relative to gitRoot) for git staging.
func installClaudeCommands(gitRoot string, quiet bool) []string {
	agent, ok := agentx.DefaultRegistry.Get(agentx.AgentTypeClaudeCode)
	if !ok {
		slog.Debug("claude code agent not in registry")
		return nil
	}

	cm := agent.CommandManager()
	if cm == nil {
		slog.Debug("claude code agent has no command manager")
		return nil
	}

	commands, err := agentx.ReadCommandFiles(claude.CommandFS, "commands")
	if err != nil {
		slog.Warn("failed to read embedded command files", "error", err)
		return nil
	}

	for i := range commands {
		commands[i].Version = version.Version
	}

	ctx := context.Background()
	written, err := cm.Install(ctx, gitRoot, commands, true)
	if err != nil {
		if !quiet {
			cli.PrintWarning(fmt.Sprintf("Could not install Claude commands: %v", err))
		}
		return nil
	}

	if !quiet {
		if len(written) > 0 {
			cli.PrintSuccess(fmt.Sprintf("Installed %d Claude command(s)", len(written)))
		} else {
			cli.PrintPreserved("Claude commands")
		}
	}

	// return relative paths for git staging
	var paths []string
	cmdDir := cm.CommandDir(gitRoot)
	for _, name := range written {
		rel, err := relFromRoot(gitRoot, cmdDir, name)
		if err == nil {
			paths = append(paths, rel)
		}
	}
	return paths
}

// relFromRoot computes a relative path from gitRoot for a file in cmdDir.
func relFromRoot(gitRoot, cmdDir, name string) (string, error) {
	return filepath.Rel(gitRoot, filepath.Join(cmdDir, name))
}
