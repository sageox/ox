package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/agentx/rules"
	_ "github.com/sageox/agentx/setup"
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
	// Install below rewrites them fresh. See appendFrontmatterStale for the same
	// frontmatter-aware staleness reasoning.
	removeTamperedRules(rulesDir, ruleFiles)

	written, err := rm.Install(context.Background(), p.RepoRoot, ruleFiles, true)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.InstallRulesResponse{
		Installed:    true,
		FilesWritten: written,
	}, nil
}

// removeTamperedRules deletes installed rule files whose on-disk body no longer
// matches their own agentx stamp (the body was hand-edited). agentx's Install
// short-circuits on a matching stamp hash even when the body has drifted, so
// without this pre-pass `--fix` is a no-op on tampered files. User-managed files
// (no stamp) and files whose body still matches their stamp are left untouched.
func removeTamperedRules(rulesDir string, ruleFiles []agentx.RuleFile) {
	for _, rf := range ruleFiles {
		path := filepath.Join(rulesDir, rf.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		stampHash, _, body := extractStampAnywhere(data, agentx.DefaultStampPrefix)
		if stampHash == "" {
			continue // user-managed — never touch
		}
		if agentx.ContentHash(body) != stampHash {
			_ = os.Remove(path) // body tampered: drop so Install writes fresh
		}
	}
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
	// scanning all lines for the stamp, mirroring the looksStamped workaround
	// already used for uninstall. Drop this block when agentx fixes the
	// first-line limitation upstream.
	rulesDir := rm.RulesDir(p.RepoRoot)
	stale = appendFrontmatterStale(rulesDir, ruleFiles, missing, stale)

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
		if !looksStamped(data) {
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

// looksStamped: workaround for agentx ExtractCommandHash only inspecting
// the first line — fails on files with frontmatter.
func looksStamped(data []byte) bool {
	hash, _, _ := extractStampAnywhere(data, agentx.DefaultStampPrefix)
	return hash != ""
}

// extractStampAnywhere finds the agentx stamp on ANY line of the file (not just
// line 1) and returns the 12-char content hash, the stamped version, and the
// body that follows the stamp line (the content the hash covers — i.e. without
// frontmatter or the stamp line itself). Returns empty strings when no stamp is
// present. This generalizes agentx.ExtractCommandHash / ExtractStampVersion,
// which only inspect the first line and therefore miss stamps that sit below
// YAML frontmatter.
//
// The rules managers stamp with agentx.DefaultStampPrefix ("agentx"), NOT the
// "ox" prefix used for command files — callers must pass that prefix to match
// what is actually on disk.
func extractStampAnywhere(data []byte, prefix string) (hash, version string, body []byte) {
	comment := agentx.StampComment(prefix)
	s := string(data)
	for idx := 0; idx < len(s); {
		end := strings.IndexByte(s[idx:], '\n')
		var line string
		if end < 0 {
			line = s[idx:]
		} else {
			line = s[idx : idx+end]
		}
		if strings.HasPrefix(line, comment) {
			rest := strings.TrimPrefix(line, comment)
			if len(rest) < 12 {
				return "", "", nil
			}
			hash = rest[:12]
			const marker = " ver: "
			if vIdx := strings.Index(line, marker); vIdx >= 0 {
				version = strings.TrimSpace(strings.TrimSuffix(line[vIdx+len(marker):], " -->"))
			}
			// body is everything after this stamp line's newline; matches how
			// StampedContent prepends a single "<stamp>\n" before rule.Content.
			if end >= 0 {
				body = []byte(s[idx+end+1:])
			}
			return hash, version, body
		}
		if end < 0 {
			break
		}
		idx += end + 1
	}
	return "", "", nil
}

// appendFrontmatterStale recomputes staleness for installed rule files whose
// stamp sits below YAML frontmatter (so agentx's first-line-only check misses
// it). A rule is stale when EITHER:
//
//   - the on-disk body no longer matches its own stamp hash (the body was
//     hand-edited / tampered — the stamp covers the body WITHOUT frontmatter,
//     matching how buildContent stamps), OR
//   - the stamp hash differs from the body the live binary ships (the binary
//     was upgraded), subject to the same version-downgrade guard the command
//     staleness path uses: a rule installed by a NEWER binary is never reported
//     stale by an older one.
//
// Files already flagged missing or stale by Validate, and user-managed files
// with no stamp, are left alone.
func appendFrontmatterStale(rulesDir string, ruleFiles []agentx.RuleFile, missing, stale []string) []string {
	already := make(map[string]bool, len(missing)+len(stale))
	for _, n := range missing {
		already[n] = true
	}
	for _, n := range stale {
		already[n] = true
	}

	for _, rf := range ruleFiles {
		if already[rf.Name] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rulesDir, rf.Name))
		if err != nil {
			continue
		}
		stampHash, ver, body := extractStampAnywhere(data, agentx.DefaultStampPrefix)
		if stampHash == "" {
			continue // user-managed file (no stamp) — never flag
		}

		// (1) body tampered: on-disk body doesn't match its own stamp.
		bodyTampered := agentx.ContentHash(body) != stampHash

		// (2) binary drift: live binary ships different content than what's
		// stamped, unless the install came from a newer binary (downgrade guard).
		binaryDrift := stampHash != agentx.ContentHash(rf.Content)
		if binaryDrift && ver != "" && rf.Version != "" && agentx.CompareVersions(rf.Version, ver) {
			binaryDrift = false
		}

		if bodyTampered || binaryDrift {
			stale = append(stale, rf.Name)
			already[rf.Name] = true
		}
	}
	return stale
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
- PR body (last line): ` + "`Co-Authored-By: [SageOx](https://github.com/SageOx)`" + `
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
