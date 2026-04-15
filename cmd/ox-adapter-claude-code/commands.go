package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sageox/agentx"
	"github.com/sageox/agentx/commands"
	_ "github.com/sageox/agentx/setup"
	"github.com/sageox/ox/extensions/claude"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// oxStampPrefix is the stamp prefix used for ox command files.
// ox uses "ox" (not the agentx default "agentx") to match existing installations.
const oxStampPrefix = "ox"

func handleInstallCommands(p adapterprotocol.CommandsParams) (*adapterprotocol.InstallCommandsResponse, error) {
	cm, err := getCommandManager()
	if err != nil {
		return nil, err
	}

	cmdFiles, err := oxCommandFiles(p.Version)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	written, err := cm.Install(ctx, p.RepoRoot, cmdFiles, true)
	if err != nil {
		return nil, err
	}

	// convert to relative paths from repo root
	cmdDir := cm.CommandDir(p.RepoRoot)
	relPaths := make([]string, 0, len(written))
	for _, name := range written {
		if rel, err := filepath.Rel(p.RepoRoot, filepath.Join(cmdDir, name)); err == nil {
			relPaths = append(relPaths, rel)
		}
	}

	return &adapterprotocol.InstallCommandsResponse{
		Installed:    true,
		FilesWritten: relPaths,
	}, nil
}

func handleCheckCommands(p adapterprotocol.CommandsParams) (*adapterprotocol.CheckCommandsResponse, error) {
	cm, err := getCommandManager()
	if err != nil {
		return nil, err
	}

	cmdFiles, err := oxCommandFiles(p.Version)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	missing, stale, err := cm.Validate(ctx, p.RepoRoot, cmdFiles)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.CheckCommandsResponse{
		Installed:   len(missing) == 0 && len(stale) == 0,
		Missing:     missing,
		Stale:       stale,
		CommandsDir: cm.CommandDir(p.RepoRoot),
		Total:       len(cmdFiles),
	}, nil
}

func handleUninstallCommands(p adapterprotocol.CommandsParams) (*adapterprotocol.UninstallCommandsResponse, error) {
	cm, err := getCommandManager()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	removed, err := cm.Uninstall(ctx, p.RepoRoot, "ox")
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.UninstallCommandsResponse{
		Uninstalled:  len(removed) > 0,
		FilesRemoved: removed,
	}, nil
}

// getCommandManager returns the Claude Code CommandManager with the ox stamp prefix.
func getCommandManager() (agentx.CommandManager, error) {
	agent, ok := agentx.DefaultRegistry.Get(agentx.AgentTypeClaudeCode)
	if !ok {
		return nil, fmt.Errorf("claude-code agent not registered")
	}

	cm := agent.CommandManager()
	if cm == nil {
		return nil, fmt.Errorf("claude-code agent has no command manager")
	}

	if ccm, ok := cm.(*commands.ClaudeCodeCommandManager); ok {
		ccm.StampPrefix = oxStampPrefix
	}

	return cm, nil
}

// oxCommandFiles reads the embedded command files and sets the version stamp.
func oxCommandFiles(version string) ([]agentx.CommandFile, error) {
	cmdFiles, err := agentx.ReadCommandFiles(claude.CommandFS, "commands")
	if err != nil {
		return nil, fmt.Errorf("reading embedded commands: %w", err)
	}

	for i := range cmdFiles {
		cmdFiles[i].Version = version
	}

	return cmdFiles, nil
}
