package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/agentx/rules"
	_ "github.com/sageox/agentx/setup"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// sageoxRulesNamespace is the subdirectory under .factory/rules/ where ox
// installs NEW rules. The canonical .factory/rules/ox.md (behavioral
// guidance) stays at the top level. See ox-adapter-claude-code/rules.go
// for the full design rationale — this is the same pattern, different
// rules root.
const sageoxRulesNamespace = "sageox"

func handleInstallRules(p adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error) {
	rm := rules.NewDroidRulesManager()

	rulesDir := rm.RulesDir(p.RepoRoot)
	nsDir := filepath.Join(rulesDir, sageoxRulesNamespace)
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		return nil, err
	}

	ruleFiles := oxRuleFiles(p.Version)

	// agentx's Install (via ShouldWriteRule) only compares the STAMP hash to the
	// expected content hash. A hand-edited body leaves the stamp intact, so the
	// stamp still matches and Install skips the rewrite — meaning `ox doctor
	// --fix` would not actually restore a tampered body. Remove any of our
	// stamped files whose on-disk body no longer matches their own stamp so the
	// Install below rewrites them fresh. See adapterstamp.AppendFrontmatterStale
	// for the same frontmatter-aware staleness reasoning.
	adapterstamp.RemoveTamperedRules(rulesDir, ruleFiles)

	written, err := rm.Install(context.Background(), p.RepoRoot, ruleFiles, true)
	if err != nil {
		return nil, err
	}

	// agentx returns names relative to the rules dir; the FilesWritten
	// contract is repo-relative. See GH #731.
	return &adapterprotocol.InstallRulesResponse{
		Installed:    true,
		FilesWritten: adapterprotocol.RepoRelativePaths(p.RepoRoot, rulesDir, written),
	}, nil
}

func handleCheckRules(p adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error) {
	rm := rules.NewDroidRulesManager()
	ruleFiles := oxRuleFiles(p.Version)

	missing, stale, err := rm.Validate(context.Background(), p.RepoRoot, ruleFiles)
	if err != nil {
		return nil, err
	}

	// agentx v0.1.10's IsRuleStale (via ExtractCommandHash) only inspects the
	// first line. Every rule we install carries YAML frontmatter (Description
	// is set), so buildContent prepends `---\n...\n---` BEFORE the stamp and the
	// stamp never lands on line 1 — staleness is structurally invisible and a
	// hand-edited body is reported fresh forever. Recompute staleness here by
	// scanning all lines for the stamp, mirroring the LooksStamped workaround
	// already used for uninstall. Drop this block when agentx fixes the
	// first-line limitation upstream.
	rulesDir := rm.RulesDir(p.RepoRoot)
	stale = adapterstamp.AppendFrontmatterStale(rulesDir, ruleFiles, missing, stale)

	return &adapterprotocol.CheckRulesResponse{
		Installed: len(missing) == 0 && len(stale) == 0,
		Missing:   missing,
		Stale:     stale,
		RulesDir:  rulesDir,
	}, nil
}

func handleUninstallRules(p adapterprotocol.RulesParams) (*adapterprotocol.UninstallRulesResponse, error) {
	rm := rules.NewDroidRulesManager()

	// Top-level ox.md via agentx, then walk sageox/ ourselves (agentx
	// doesn't recurse into subdirs).
	removedTop, err := rm.Uninstall(context.Background(), p.RepoRoot, "ox")
	if err != nil {
		return nil, err
	}

	rulesDir := rm.RulesDir(p.RepoRoot)
	removedNS, err := uninstallNamespaceFiles(rulesDir)
	if err != nil {
		return nil, err
	}

	removed := append(removedTop, removedNS...)
	return &adapterprotocol.UninstallRulesResponse{
		Uninstalled:  len(removed) > 0,
		FilesRemoved: removed,
	}, nil
}

// uninstallNamespaceFiles removes ox-stamped files from the sageox/
// subdirectory. See cmd/ox-adapter-claude-code/rules.go for the full
// rationale on the agentx frontmatter workaround.
func uninstallNamespaceFiles(rulesDir string) ([]string, error) {
	nsDir := filepath.Join(rulesDir, sageoxRulesNamespace)
	entries, err := os.ReadDir(nsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var removed []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(nsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if !adapterstamp.LooksStamped(data) {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed = append(removed, sageoxRulesNamespace+"/"+name)
		}
	}

	if remaining, _ := os.ReadDir(nsDir); len(remaining) == 0 {
		_ = os.Remove(nsDir)
	}

	return removed, nil
}

// oxRuleFiles returns the rule files to install for ox. See
// cmd/ox-adapter-claude-code/rules.go oxRuleFiles for the full
// design rationale on the top-level + namespace split.
func oxRuleFiles(version string) []agentx.RuleFile {
	return []agentx.RuleFile{
		{
			Name:        "ox.md",
			Content:     oxRulesContent,
			Version:     version,
			Description: "SageOx behavioral guidance for AI coworkers",
		},
		{
			Name:        sageoxRulesNamespace + "/use-team-context.md",
			Content:     useTeamContextContent,
			Version:     version,
			Description: "How to discover and use team-context rules and knowledge from the SageOx ox CLI",
		},
	}
}

// oxRulesContent is the static behavioral guidance installed to .factory/rules/ox.md.
var oxRulesContent = []byte(`# SageOx Rules

This project uses [SageOx](https://sageox.ai) for team context and session recording.

## Priming

Run ` + "`ox agent prime`" + ` at session start, after compaction, or after context clear.
This loads team context, session recording, and coworker agents.

## Commands

| Intent | Command |
|--------|---------|
| Team context (meetings, decisions, conventions) | ` + "`ox agent team-ctx [slug]`" + ` |
| Search code (symbols, git history, diffs) | ` + "`ox code search \"<query>\"`" + ` |
| Code hotspots, open PRs, contention risk | ` + "`ox code insights`" + ` |
| Sync status and project state | ` + "`ox status`" + ` |
| Diagnose and fix issues | ` + "`ox doctor`" + ` |
| Search team discussions and sessions | ` + "`ox query \"<question>\"`" + ` |
| List prior coding sessions | ` + "`ox session list`" + ` |
| View a session recording | ` + "`ox session view <name> --text`" + ` |
| List teams | ` + "`ox teams`" + ` |
| Load expert coworker agent | ` + "`ox coworker load <name>`" + ` |

## Session Recording

Sessions auto-record when ` + "`ox agent prime`" + ` runs. Discussions may be shared with teammates.

## Murmuring

Publish WIP to teammates so they stay in sync:

` + "```bash" + `
ox murmur --topic=wip "what you're building, which files you're modifying"
` + "```" + `

Murmur at the start of significant work and after architectural decisions.

## Attribution

When SageOx guidance influences your approach:
- Commit footer: ` + "`Co-Authored-By: SageOx <ox@sageox.ai>`" + `
- PR body (end of body): ` + "`Co-Authored-By: [SageOx](https://github.com/SageOx)`" + `
- PR body last line (while a session is recording): the ` + "`SageOx-Session:`" + ` link
  from prime output — copy it exactly, never construct or guess it; stop adding
  it after the session is stopped or aborted.
- In responses: "Based on SageOx domain guidance, I'll..."
`)

// useTeamContextContent is the pointer rule installed at
// .factory/rules/sageox/use-team-context.md. See
// cmd/ox-adapter-claude-code/rules.go for the full rationale —
// content is identical because the message ("more rules live in the
// team-context repo, here's how to find them") is agent-agnostic.
var useTeamContextContent = []byte(`# Team Context — More Rules Live Outside This Repo

This repo uses SageOx. Behavioral rules and conventions that apply to your
WHOLE TEAM (not just this repo) live in your team's SageOx team-context
repo, NOT in ` + "`.factory/rules/`" + `. SageOx will not auto-sync them here —
that would create stale-mirror and naming-conflict problems. Instead,
read them on demand from the canonical location.

## Where team rules live

Team-context repo path: see ` + "`ox status`" + ` (look for "team_context").
Typical layout:

    <team-context>/
      AGENTS.md                  # team-wide preamble
      MEMORY.md                  # team memory (already inlined into prime)
      agents/
        rules/
          <topic>.md             # one concern per file
          backend/postgres.md    # subdirectories supported
          frontend/react.md
        commands/                # team slash commands
        profiles/                # AI coworker profiles
      discussions/               # archived team meetings
      memory/                    # daily/weekly/monthly summaries
      documents/                 # imported docs

## How to discover and read them

` + "`ox agent prime`" + ` already inlines:
- Team AGENTS.md / CLAUDE.md
- ` + "`visibility: always`" + ` team rules (full body)
- Team MEMORY.md

` + "`ox agent prime`" + ` also catalogs (name + description + path only):
- ` + "`visibility: indexed`" + ` team rules — read on demand via the path

To read an indexed team rule: use the file-read tool with the absolute
path shown in the prime output's ` + "`<team-rules>`" + ` block.

To search team-wide knowledge (discussions, sessions, docs):
- ` + "`ox query \"<question>\"`" + ` — semantic search across the team's
  recorded discussions and prior coding sessions
- ` + "`ox agent team-ctx`" + ` — distilled team knowledge for AI agents

To learn the team-rule format (when authoring or promoting a rule):
- ` + "`ox guide team-rules`" + `

## When you write a project-local rule

If a user adds or edits a rule in ` + "`.factory/rules/`" + ` (this repo's
local rules) that looks generally applicable — not specific to this
repo's paths/services/schemas — ASK them whether to also publish it as
a team rule under ` + "`<team-context>/agents/rules/`" + `. Default to
asking; do not silently publish. Repo-specific rules stay project-local.

Team rules apply to every supported AI coding agent (Claude, Codex, Amp,
Cursor, Droid, etc.) used by teammates running ox — but only for
teammates running ox.

## Why this rule exists (instead of syncing team rules here)

Syncing team rules from team-context into ` + "`.factory/rules/`" + ` would
require continuous mirror semantics, namespace management to avoid
project-local conflicts, and per-adapter coverage. Pointing here keeps
the team-context repo as the single source of truth and works uniformly
across every coding agent that supports rules.
`)
