package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/claude"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// Claude skills are installed as a DIRECTORY per skill:
//
//	.claude/skills/<name>/SKILL.md
//
// Unlike commands (one .md, stamp on line 1), a skill's YAML frontmatter MUST be
// the first bytes of SKILL.md so Claude can parse `name`/`description`. So the
// drift stamp is placed INSIDE the file but AFTER the closing `---` of the
// frontmatter — never on line 1. The stamp hash covers only the body that
// follows it (the same contract extractStampAnywhere assumes for rules), so a
// hand-edited body changes the body hash without touching the stamp, and drift
// detection reads the stamp via adapterstamp.ExtractStampAnywhere (shared with
// rules.go).
//
// On-disk layout written by Install:
//
//	---
//	name: ox-plan
//	description: ...
//	---
//	<!-- ox-hash: <12hex> ver: <version> -->
//	<body...>
const (
	// skillsEmbedDir is the embed.FS subdir holding skills/<name>/SKILL.md.
	skillsEmbedDir = "skills"
	// skillFileName is the canonical per-skill markdown filename.
	skillFileName = "SKILL.md"
	// oxSkillStampPrefix matches the command stamp prefix so all ox-installed
	// surfaces share one stamp vocabulary ("<!-- ox-hash: ...").
	oxSkillStampPrefix = oxStampPrefix
)

// skillFile is one skill to install: its directory name plus the embedded
// SKILL.md content (frontmatter + body, unstamped).
type skillFile struct {
	Name    string // skill/dir name, e.g. "ox-plan"
	Content []byte // embedded SKILL.md bytes (frontmatter-first, no stamp)
	Version string // ox version for the stamp + downgrade guard
}

// skillsDir returns the Claude skills directory for a project root.
func skillsDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "skills")
}

// oxSkillFiles reads the embedded skills tree into skillFile records.
// Each immediate subdirectory of skills/ that contains a SKILL.md becomes one
// skill; the dir name is the skill name.
func oxSkillFiles(version string) ([]skillFile, error) {
	entries, err := fs.ReadDir(claude.SkillFS, skillsEmbedDir)
	if err != nil {
		return nil, fmt.Errorf("read embedded skills dir: %w", err)
	}

	var skills []skillFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		embedPath := skillsEmbedDir + "/" + name + "/" + skillFileName
		content, err := fs.ReadFile(claude.SkillFS, embedPath)
		if err != nil {
			// a subdir without a SKILL.md is not a skill — skip quietly.
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read embedded skill %s: %w", name, err)
		}
		skills = append(skills, skillFile{
			Name:    name,
			Content: content,
			Version: version,
		})
	}
	return skills, nil
}

// stampedSkillContent inserts the drift stamp immediately AFTER the closing
// `---` of the YAML frontmatter (never on line 1). The stamp's hash covers only
// the body that follows the frontmatter, matching extractStampAnywhere. If the
// content has no frontmatter (defensive — all shipped skills have it), the stamp
// is prepended at the top and the hash covers the whole content.
func stampedSkillContent(content []byte, version string) []byte {
	frontmatter, body, hasFM := splitFrontmatter(content)
	hashTarget := body
	if !hasFM {
		hashTarget = content
	}
	stampLine := fmt.Sprintf("%s%s ver: %s -->\n",
		agentx.StampComment(oxSkillStampPrefix), agentx.ContentHash(hashTarget), version)

	if !hasFM {
		return append([]byte(stampLine), content...)
	}
	var out []byte
	out = append(out, frontmatter...)
	out = append(out, []byte(stampLine)...)
	out = append(out, body...)
	return out
}

// splitFrontmatter separates a leading YAML frontmatter block (`---\n...\n---\n`)
// from the body. Returns the frontmatter (including both fences and the trailing
// newline), the body that follows, and whether a frontmatter block was present.
// The content MUST start with `---` for a frontmatter block to be recognized.
func splitFrontmatter(content []byte) (frontmatter, body []byte, ok bool) {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") && s != "---" && !strings.HasPrefix(s, "---\r\n") {
		return nil, content, false
	}
	// find the closing fence: a line that is exactly "---" after the opener.
	// scan line-by-line starting after the first line.
	firstNL := strings.IndexByte(s, '\n')
	if firstNL < 0 {
		return nil, content, false
	}
	idx := firstNL + 1
	for idx < len(s) {
		end := strings.IndexByte(s[idx:], '\n')
		var line string
		if end < 0 {
			line = s[idx:]
		} else {
			line = s[idx : idx+end]
		}
		if strings.TrimRight(line, "\r") == "---" {
			// closing fence found; frontmatter spans [0, end-of-this-line+newline).
			if end < 0 {
				return content, nil, true
			}
			fmEnd := idx + end + 1 // include the closing fence's newline
			return []byte(s[:fmEnd]), []byte(s[fmEnd:]), true
		}
		if end < 0 {
			break
		}
		idx += end + 1
	}
	// no closing fence — treat as no frontmatter (defensive).
	return nil, content, false
}

// frontmatterDiffers reports whether the YAML frontmatter of the on-disk skill
// differs from the embedded skill — including the case where one has a
// frontmatter block and the other doesn't. The drift stamp's hash covers only
// the body, so name/description edits in the frontmatter are otherwise invisible
// to staleness detection. Callers MUST gate this on the file being ox-stamped so
// it never forces a rewrite of a user-authored unstamped SKILL.md.
func frontmatterDiffers(existing, embedded []byte) bool {
	existingFM, _, existingHasFM := splitFrontmatter(existing)
	embedFM, _, embedHasFM := splitFrontmatter(embedded)
	return existingHasFM != embedHasFM || !bytes.Equal(existingFM, embedFM)
}

func handleInstallSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.InstallSkillsResponse, error) {
	skills, err := oxSkillFiles(p.Version)
	if err != nil {
		return nil, err
	}

	dir := skillsDir(p.RepoRoot)
	var written []string
	for _, sk := range skills {
		skillDir := filepath.Join(dir, sk.Name)
		dstPath := filepath.Join(skillDir, skillFileName)

		var existing []byte
		if data, err := os.ReadFile(dstPath); err == nil {
			existing = data
		}
		if !shouldWriteSkill(existing, sk) {
			continue
		}

		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return nil, fmt.Errorf("create skill directory %s: %w", sk.Name, err)
		}
		stamped := stampedSkillContent(sk.Content, sk.Version)
		if err := os.WriteFile(dstPath, stamped, 0o644); err != nil {
			return nil, fmt.Errorf("write skill file %s: %w", sk.Name, err)
		}
		// report repo-relative (e.g. .claude/skills/ox-plan/SKILL.md) per the
		// FilesWritten contract. Reporting it relative to the skills dir —
		// as this did until GH #731 — made ox resolve it to <root>/ox-plan/
		// SKILL.md, which does not exist, and one bad pathspec fails the
		// whole `git add`, so nothing at all got staged.
		written = append(written, dstPath)
	}

	// Command→skill migration cleanup: when a surface moves from a slash command
	// to a skill (e.g. ox-plan, ox-session-review), an existing install keeps the
	// stale .claude/commands/<id>.md alongside the new skill — a duplicate Layer-2
	// surface, because the commands installer only writes the embedded set and
	// never prunes ids it no longer ships. Prune any ox-stamped legacy command
	// file whose id is now an embedded skill so the migration is self-cleaning.
	cleanupLegacyCommandFilesForSkills(p.RepoRoot, skills)

	return &adapterprotocol.InstallSkillsResponse{
		Installed:    true,
		FilesWritten: adapterprotocol.RepoRelativePaths(p.RepoRoot, dir, written),
	}, nil
}

// cleanupLegacyCommandFilesForSkills removes legacy slash-command files that have
// been superseded by an embedded skill of the same id. For each embedded skill id,
// if <gitRoot>/.claude/commands/<id>.md exists AND carries the ox command stamp on
// line 1 (so we only ever delete files ox itself installed — user-authored
// unstamped files are preserved), the stale command file is removed.
//
// This is best-effort: any error is logged and ignored, and never fails the
// install. It detects ox-stamped files with adapterstamp.ExtractStampAnywhere
// using the same "ox" prefix the command installer stamps with — a legacy command
// file carries "<!-- ox-hash: ... -->" on line 1, which ExtractStampAnywhere matches.
func cleanupLegacyCommandFilesForSkills(repoRoot string, skills []skillFile) {
	commandsDir := filepath.Join(repoRoot, ".claude", "commands")
	for _, sk := range skills {
		legacyPath := filepath.Join(commandsDir, sk.Name+".md")
		data, err := os.ReadFile(legacyPath)
		if err != nil {
			continue // no legacy command file (the common case) — nothing to clean
		}
		// only remove files ox stamped; never delete user-authored command files.
		if hash, _, _ := adapterstamp.ExtractStampAnywhere(data, oxSkillStampPrefix); hash == "" {
			continue
		}
		if err := os.Remove(legacyPath); err != nil {
			slog.Warn("skills: failed to remove legacy command file superseded by skill",
				"id", sk.Name, "path", legacyPath, "error", err)
			continue
		}
		slog.Info("skills: removed legacy command file superseded by skill",
			"id", sk.Name, "path", legacyPath)
	}
}

func handleCheckSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.CheckSkillsResponse, error) {
	skills, err := oxSkillFiles(p.Version)
	if err != nil {
		return nil, err
	}

	dir := skillsDir(p.RepoRoot)
	var missing, stale []string
	for _, sk := range skills {
		dstPath := filepath.Join(dir, sk.Name, skillFileName)
		existing, err := os.ReadFile(dstPath)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, sk.Name)
				continue
			}
			return nil, fmt.Errorf("read skill file %s: %w", sk.Name, err)
		}
		if isSkillStale(existing, sk) {
			stale = append(stale, sk.Name)
		}
	}

	return &adapterprotocol.CheckSkillsResponse{
		Installed: len(missing) == 0 && len(stale) == 0,
		Missing:   missing,
		Stale:     stale,
		SkillsDir: dir,
		Total:     len(skills),
	}, nil
}

func handleUninstallSkills(p adapterprotocol.SkillsParams) (*adapterprotocol.UninstallSkillsResponse, error) {
	dir := skillsDir(p.RepoRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapterprotocol.UninstallSkillsResponse{Uninstalled: false}, nil
		}
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	var removed []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		skillPath := filepath.Join(dir, name, skillFileName)
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		// only remove skills we stamped (leave user-authored skills alone).
		if hash, _, _ := adapterstamp.ExtractStampAnywhere(data, oxSkillStampPrefix); hash == "" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			slog.Warn("skills: failed to remove skill directory", "name", name, "error", err)
			continue
		}
		removed = append(removed, filepath.Join(name, skillFileName))
	}

	// best-effort cleanup of an empty skills dir.
	if remaining, _ := os.ReadDir(dir); len(remaining) == 0 {
		_ = os.Remove(dir)
	}

	return &adapterprotocol.UninstallSkillsResponse{
		Uninstalled:  len(removed) > 0,
		FilesRemoved: removed,
	}, nil
}

// shouldWriteSkill decides whether to (over)write an installed skill. It mirrors
// agentx.ShouldWriteCommand's logic but reads the stamp from after the
// frontmatter (extractStampAnywhere) rather than line 1:
//
//   - file doesn't exist -> write
//   - no stamp -> skip (user-managed)
//   - frontmatter differs from the embedded skill -> write (restore) — the stamp
//     hash only covers the body, so name/description edits are otherwise invisible
//   - body matches the live binary's body hash -> skip (identical)
//   - body tampered (on-disk body != its own stamp hash) -> write (restore)
//   - installed by a newer ox version -> skip (downgrade guard)
//   - otherwise -> write
func shouldWriteSkill(existing []byte, sk skillFile) bool {
	if existing == nil {
		return true
	}
	stampHash, ver, body := adapterstamp.ExtractStampAnywhere(existing, oxSkillStampPrefix)
	if stampHash == "" {
		return false // user-managed — never overwrite
	}
	// Frontmatter is NOT covered by the stamp's body hash, so a name/description
	// edit slips past both the body-match and tamper checks below. Only ox-stamped
	// files reach here (the no-stamp guard above already returned), so forcing a
	// rewrite on a frontmatter diff can't clobber a user's unstamped SKILL.md.
	if frontmatterDiffers(existing, sk.Content) {
		return true
	}
	_, embedBody, _ := splitFrontmatter(sk.Content)
	wantHash := agentx.ContentHash(embedBody)

	// on-disk body still matches the live binary's content AND its own stamp:
	// nothing to do.
	if stampHash == wantHash && agentx.ContentHash(body) == stampHash {
		return false
	}
	// tampered body (stamp no longer covers the on-disk body): always restore.
	if agentx.ContentHash(body) != stampHash {
		return true
	}
	// binary drift only: respect the downgrade guard.
	if ver != "" && sk.Version != "" && agentx.CompareVersions(sk.Version, ver) {
		return false
	}
	return true
}

// isSkillStale reports whether an installed skill is outdated or tampered.
// Mirrors adapterstamp.AppendFrontmatterStale's two-part rule (body tampered OR
// binary drift) with the same version-downgrade guard. User-managed files (no
// stamp) are never stale.
func isSkillStale(existing []byte, sk skillFile) bool {
	stampHash, ver, body := adapterstamp.ExtractStampAnywhere(existing, oxSkillStampPrefix)
	if stampHash == "" {
		return false // user-managed
	}
	// (0) frontmatter tampered: the stamp hash covers only the body, so a
	// name/description edit is invisible to the body checks below. Only ox-stamped
	// files reach here, so this never flags a user's unstamped SKILL.md.
	if frontmatterDiffers(existing, sk.Content) {
		return true
	}
	// (1) body tampered: on-disk body doesn't match its own stamp.
	if agentx.ContentHash(body) != stampHash {
		return true
	}
	// (2) binary drift: live binary ships a different body, unless installed by a
	// newer ox (downgrade guard).
	_, embedBody, _ := splitFrontmatter(sk.Content)
	if stampHash != agentx.ContentHash(embedBody) {
		if ver != "" && sk.Version != "" && agentx.CompareVersions(sk.Version, ver) {
			return false
		}
		return true
	}
	return false
}
