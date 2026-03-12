package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sageox/ox/extensions/claude"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/agentx"
	_ "github.com/sageox/ox/pkg/agentx/setup"
)

// checkClaudeCommands validates that ox slash commands are installed in .claude/commands/.
func checkClaudeCommands(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Claude commands", "not in git repo", "")
	}

	agent, ok := agentx.DefaultRegistry.Get(agentx.AgentTypeClaudeCode)
	if !ok {
		return SkippedCheck("Claude commands", "agent not in registry", "")
	}

	cm := agent.CommandManager()
	if cm == nil {
		return SkippedCheck("Claude commands", "no command manager", "")
	}

	commands, err := agentx.ReadCommandFiles(claude.CommandFS, "commands")
	if err != nil {
		slog.Warn("failed to read embedded command files", "error", err)
		return SkippedCheck("Claude commands", "failed to read embedded commands", "")
	}

	for i := range commands {
		commands[i].Version = version.Version
	}

	ctx := context.Background()
	missing, stale, err := cm.Validate(ctx, gitRoot, commands)
	if err != nil {
		return WarningCheck("Claude commands", "validation error", err.Error())
	}

	if len(missing) == 0 && len(stale) == 0 {
		return PassedCheck("Claude commands", fmt.Sprintf("%d installed", len(commands)))
	}

	// build problem description
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("%d missing: %s", len(missing), strings.Join(missing, ", ")))
	}
	if len(stale) > 0 {
		problems = append(problems, fmt.Sprintf("%d outdated: %s", len(stale), strings.Join(stale, ", ")))
	}
	problemStr := strings.Join(problems, "; ")

	if fix {
		// install missing and update stale (overwrite=true replaces only differing content)
		written, installErr := cm.Install(ctx, gitRoot, commands, true)
		if installErr != nil {
			return FailedCheck("Claude commands", problemStr,
				fmt.Sprintf("Fix failed: %v", installErr))
		}
		return PassedCheck("Claude commands",
			fmt.Sprintf("restored %d command(s)", len(written)))
	}

	return FailedCheck("Claude commands", problemStr,
		"Run `ox doctor --fix` or `ox init` to restore")
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugClaudeCommands,
		Name:        "Claude commands",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Verifies ox slash commands are installed in .claude/commands/",
		Run:         checkClaudeCommands,
	})
}
