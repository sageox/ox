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

// sageoxRulesNamespace is the LEGACY subdirectory under .claude/rules/ where ox
// used to install its non-primary rules, kept solely so existing repositories can
// be cleaned up. Nothing is written there any more.
//
// As of the 0.15.0 rename every ox rule is installed FLAT under the reserved
// ox-cli-* prefix (ox-cli.md, ox-cli-use-team-context.md). The subdirectory
// existed to avoid "polluting the rules root with ox-* prefixed siblings"; the
// reserved prefix now does that job in the filename, which is what lets a single
// glob in .claude/.gitignore cover the whole ox rule surface. A nested directory
// cannot be covered by the same one-line rule, so flattening is a prerequisite
// for keeping rules out of the customer's pull requests.
//
// Design note (rule pointer pattern), unchanged: rather than mirroring every team
// rule into .claude/rules/, the adapter installs ONE pointer rule that teaches
// the agent to discover team rules in their canonical home
// (<team-context>/agents/rules/). Team rules stay where the team writes them.
const sageoxRulesNamespace = "sageox"

const (
	oxRuleDescription          = "SageOx behavioral guidance for AI coworkers"
	teamContextRuleDescription = "How to discover and use team-context rules and knowledge from the SageOx ox CLI"
)

func handleInstallRules(p adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error) {
	rm := rules.NewClaudeCodeRulesManager()

	// Every ox rule now lives flat under the ox-cli-* prefix, so there is no
	// subdirectory to pre-create: agentx MkdirAlls the rules root itself. The
	// legacy sageox/ directory is only ever REMOVED from here on (see
	// uninstallNamespaceFiles).
	rulesDir := rm.RulesDir(p.RepoRoot)

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

	// Retire the pre-0.15.0 rule surface only after its replacement is safely
	// installed. Existing projects otherwise risk losing their only guidance if
	// the current install fails partway through.
	retireLegacyRules(rulesDir)

	// agentx returns names relative to the rules dir (ox.md,
	// sageox/use-team-context.md). The FilesWritten contract is
	// repo-relative — see GH #731, where handing ox `ox.md` made it stage
	// nothing at all.
	return &adapterprotocol.InstallRulesResponse{
		Installed:    true,
		FilesWritten: adapterprotocol.RepoRelativePaths(p.RepoRoot, rulesDir, written),
	}, nil
}

func handleCheckRules(p adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error) {
	rm := rules.NewClaudeCodeRulesManager()
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
	rm := rules.NewClaudeCodeRulesManager()

	// Two passes: top-level (ox.md) via agentx.Uninstall (prefix match on
	// "ox" returns stamped top-level files), then walk the sageox/
	// namespace ourselves since agentx doesn't recurse into subdirs.
	removedTop, err := rm.Uninstall(context.Background(), p.RepoRoot, "ox")
	if err != nil {
		return nil, err
	}

	rulesDir := rm.RulesDir(p.RepoRoot)
	removedCurrent := adapterstamp.RemoveVerifiedRules(rulesDir, oxRuleFiles(p.Version))
	removedNS, err := uninstallNamespaceFiles(rulesDir)
	if err != nil {
		return nil, err
	}

	removed := append(removedTop, removedCurrent...)
	removed = append(removed, removedNS...)
	return &adapterprotocol.UninstallRulesResponse{
		Uninstalled:  len(removed) > 0,
		FilesRemoved: removed,
	}, nil
}

// legacyRuleFiles are the top-level rule filenames ox installed before the
// 0.15.0 flattening. They are removed on install, and only when their ox stamp
// verifies them as ours.
var legacyRuleFiles = []string{"ox.md"}

// retireLegacyRules deletes the pre-0.15.0 ox rule surface: the top-level
// legacy files and the whole sageox/ subdirectory. Best-effort — a failure here
// must never block installing the current rules.
func retireLegacyRules(rulesDir string) {
	for _, name := range legacyRuleFiles {
		path := filepath.Join(rulesDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if !adapterstamp.RuleStampVerifies(data, agentx.DefaultStampPrefix, oxRuleDescription) {
			continue // user-authored; not ours to remove
		}
		_ = os.Remove(path)
	}
	_, _ = uninstallNamespaceFiles(rulesDir)
}

// uninstallNamespaceFiles removes ox-stamped rules from the LEGACY sageox/
// subdirectory, then removes the directory itself if empty. Files without an ox
// stamp (added by the user) are preserved.
//
// After the 0.15.0 flattening this is pure migration: it is how a repository
// initialized by an older ox stops carrying a nested rule tree that no single
// .gitignore line can cover.
//
// Implementation note: agentx v0.1.10's ExtractCommandHash inspects only the
// first line of file content, so files with YAML frontmatter — exactly what this
// adapter used to write here — appear unstamped to agentx. We scan the full
// content for the stamp marker instead. When agentx fixes that upstream this can
// collapse back to a single ExtractCommandHash call.
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
		if !adapterstamp.RuleStampVerifies(data, agentx.DefaultStampPrefix, teamContextRuleDescription) {
			continue // not ours
		}
		if err := os.Remove(path); err == nil {
			removed = append(removed, sageoxRulesNamespace+"/"+name)
		}
	}

	// best-effort cleanup of empty namespace dir
	if remaining, _ := os.ReadDir(nsDir); len(remaining) == 0 {
		_ = os.Remove(nsDir)
	}

	return removed, nil
}

// oxRuleFiles returns the rule files to install for ox.
//
// Two install locations:
//
//  1. Top-level (.claude/rules/ox.md) — the canonical SageOx behavioral
//     guidance file ("be a good teammate", session start, attribution,
//     command quick reference). Long-lived; teams have built muscle
//     memory around this filename. Stays here.
//
//  2. Namespace (.claude/rules/sageox/<name>.md) — every NEW rule
//     SageOx adds going forward. Rather than polluting the rules root
//     with ox-feature1.md, ox-feature2.md, ... siblings, we group them
//     under sageox/. Future knowledge-bubble pointer rules
//     (use-team-context.md today; potentially use-user-context.md when
//     the user knowledge bubble lands) live here.
//
// Today the namespace contains one pointer rule:
//   - sageox/use-team-context.md — "MORE RULES → here" pattern.
//     Tells the agent that team-wide rules live in the team-context
//     repo (NOT replicated into .claude/rules/) and how to discover
//     and read them on demand. This avoids continuous-mirror sync of
//     every team rule into every cloned repo: one pointer rule, no
//     conflict resolution, no cleanup, no per-adapter coverage gap.
//
// Both files are charged to the "sageox" budget bucket — the content
// is a SageOx product decision. Team rules pointed to remain team-
// authored content and are accounted separately when they enter prime
// XML.
func oxRuleFiles(version string) []agentx.RuleFile {
	return []agentx.RuleFile{
		{
			Name:        "ox-cli.md",
			Content:     oxRulesContent,
			Version:     version,
			Description: oxRuleDescription,
		},
		{
			Name:        "ox-cli-use-team-context.md",
			Content:     useTeamContextContent,
			Version:     version,
			Description: teamContextRuleDescription,
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

Attribution is **conditional**: attribute to SageOx only when SageOx-delivered team context measurably changed what you did. If it did not, add nothing — no note, no footer, and report the score as ` + "`none`" + `. Fabricating attribution is worse than omitting it.

- In responses: "SageOx found [name]'s session on [topic]..."
- Commit footer: ` + "`Co-Authored-By: SageOx <ox@sageox.ai>`" + `
- PR body: ` + "`Co-Authored-By: [SageOx](https://github.com/SageOx)`" + `
- PR body last line (while a session is recording): the ` + "`SageOx-Session:`" + ` link
  from prime output — copy it exactly, never construct or guess it; stop adding
  it after the session is stopped or aborted.

### Record Your Session
Sessions auto-record after priming. Use ` + "`ox agent session stop`" + ` to end.
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

// useTeamContextContent is the pointer rule installed at
// .claude/rules/sageox/use-team-context.md. It tells the agent that
// team-wide rules, conventions, and knowledge are NOT in this repo's
// .claude/rules/ — they live in the team-context repo and are loaded
// either via `ox agent prime` (always) or via on-demand reads.
//
// The rule deliberately does NOT duplicate team content into Claude's
// rule store. It points to the canonical location and explains how to
// fetch the parts the agent needs.
var useTeamContextContent = []byte(`# Team Context — More Rules Live Outside This Repo

This repo uses SageOx. Behavioral rules and conventions that apply to your
WHOLE TEAM (not just this repo) live in your team's SageOx team-context
repo, NOT in ` + "`.claude/rules/`" + `. SageOx will not auto-sync them here —
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

To read an indexed team rule: use the Read tool with the absolute path
shown in the prime output's ` + "`<team-rules>`" + ` block.

To search team-wide knowledge (discussions, sessions, docs):
- ` + "`ox query \"<question>\"`" + ` — semantic search across the team's
  recorded discussions and prior coding sessions
- ` + "`ox agent team-ctx`" + ` — distilled team knowledge for AI agents

To learn the team-rule format (when authoring or promoting a rule):
- ` + "`ox guide team-rules`" + `

## When you write a project-local rule

If a user adds or edits a rule in ` + "`.claude/rules/`" + ` (this repo's
local rules) that looks generally applicable — not specific to this
repo's paths/services/schemas — ASK them whether to also publish it as
a team rule under ` + "`<team-context>/agents/rules/`" + `. Default to
asking; do not silently publish. Repo-specific rules stay project-local.

Team rules apply to every supported AI coding agent (Claude, Codex, Amp,
Cursor, etc.) used by teammates running ox — but only for teammates
running ox. Project-local ` + "`.claude/rules/`" + ` only reaches Claude
users. That asymmetry is the reason to promote durable conventions
team-wide.

## Why this rule exists (instead of syncing team rules here)

Syncing team rules from team-context into ` + "`.claude/rules/`" + ` would
require: continuous mirror semantics (write on change, remove on
disappearance), namespace management to avoid project-local conflicts,
and per-adapter coverage (Claude has rules; Codex / Amp don't yet).
Pointing here instead keeps the team-context repo as the single source
of truth and works uniformly across every coding agent that supports
rules.
`)
