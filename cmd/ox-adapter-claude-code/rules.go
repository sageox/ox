package main

import (
	"context"

	"github.com/sageox/agentx"
	"github.com/sageox/agentx/rules"
	_ "github.com/sageox/agentx/setup"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleInstallRules(p adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error) {
	rm := rules.NewClaudeCodeRulesManager()
	ruleFiles := oxRuleFiles(p.Version)

	written, err := rm.Install(context.Background(), p.RepoRoot, ruleFiles, true)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.InstallRulesResponse{
		Installed:    true,
		FilesWritten: written,
	}, nil
}

func handleCheckRules(p adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error) {
	rm := rules.NewClaudeCodeRulesManager()
	ruleFiles := oxRuleFiles(p.Version)

	missing, stale, err := rm.Validate(context.Background(), p.RepoRoot, ruleFiles)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.CheckRulesResponse{
		Installed: len(missing) == 0 && len(stale) == 0,
		Missing:   missing,
		Stale:     stale,
		RulesDir:  rm.RulesDir(p.RepoRoot),
	}, nil
}

func handleUninstallRules(p adapterprotocol.RulesParams) (*adapterprotocol.UninstallRulesResponse, error) {
	rm := rules.NewClaudeCodeRulesManager()

	removed, err := rm.Uninstall(context.Background(), p.RepoRoot, "ox")
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.UninstallRulesResponse{
		Uninstalled:  len(removed) > 0,
		FilesRemoved: removed,
	}, nil
}

// oxRuleFiles returns the rule files to install for ox.
func oxRuleFiles(version string) []agentx.RuleFile {
	return []agentx.RuleFile{
		{
			Name:        "ox.md",
			Content:     oxRulesContent,
			Version:     version,
			Description: "SageOx behavioral guidance for AI coworkers",
		},
	}
}

// oxRulesContent is the static behavioral guidance installed to .claude/rules/ox.md.
var oxRulesContent = []byte(`# SageOx — You're Part of a Team

This project uses SageOx for team coordination. You are an AI coworker —
your teammates (human and AI) can see your work, and you can see theirs.

## Session Start

Run ` + "`ox agent prime`" + ` before any other action. This loads:
- Team conventions, architectural decisions, and norms
- Prior session history from teammates
- Your agent identity (needed for all ox commands)

Re-run after context compaction or clear.

## Being a Good Teammate

### Share What You're Working On
When starting significant work or making architectural decisions:

    ox murmur --topic=wip "what you're building, which files"

Murmurs are lightweight, ephemeral coordination signals. Your teammates'
murmurs appear as whispers in your context — read them to avoid conflicts.

### Learn from Your Team
Before diving into unfamiliar code or making design decisions:

    ox query "your question"              # search discussions + sessions
    ox code search "<symbol or pattern>"  # search code + git history
    ox code insights                      # hotspots, contention, open PRs

### Use Team Expertise
Your team has expert coworker agents for deep domain knowledge:

    ox coworker load <name>

### Credit Your Sources
When SageOx guidance shapes your approach:
- In responses: "SageOx found [name]'s session on [topic]..."
- Commit footer: ` + "`Co-Authored-By: SageOx <ox@sageox.ai>`" + `
- PR body: ` + "`Co-Authored-By: [SageOx](https://github.com/SageOx)`" + `

### Record Your Session
Sessions auto-record after priming. Use ` + "`/ox-session-stop`" + ` to end.
Your session becomes part of the project ledger — teammates learn from it.

## Quick Reference

| When you need... | Run |
|------------------|-----|
| Team context at session start | ` + "`ox agent prime`" + ` |
| Team decisions & conventions | ` + "`ox agent team-ctx`" + ` |
| Prior work on this topic | ` + "`ox query \"<question>\"`" + ` |
| Code symbols & git history | ` + "`ox code search \"<query>\"`" + ` |
| Change risk & hotspots | ` + "`ox code insights`" + ` |
| To signal teammates | ` + "`ox murmur --topic=wip \"...\"`" + ` |
| Domain expertise | ` + "`ox coworker load <name>`" + ` |
| Health check | ` + "`ox status`" + ` / ` + "`ox doctor`" + ` |
`)
