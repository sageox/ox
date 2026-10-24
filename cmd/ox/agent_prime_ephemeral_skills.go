package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamskills"
)

// agent_prime_ephemeral_skills.go — team skills and rules for HTTP-only prime.
//
// The team-context endpoint carries each servable skill as the files a checkout
// holds at <root>/<dir>, and each rule at its checkout path. Writing them THERE
// is the whole design: discovery, classification and the digest-pinned approval
// gate then run on them exactly as on a git checkout, so an executable skill
// still waits for `ox skills approve` and nothing here decides trust itself.

// httpSkillStagePrefix names the per-skill staging directories. They sit at the
// Team Context root, outside every skill root, so discovery never walks a
// half-written skill; a crash leaves one behind for the next prime to clear.
const httpSkillStagePrefix = ".ox-http-skill-"

// httpRuleTempSuffix marks a rule mid-write. Discovery reads only *.md.
const httpRuleTempSuffix = ".ox-tmp"

// materializeTeamSkillsAndRules writes the served skills and rules into
// teamCtxDir. Fields an older server omits (nil) leave disk untouched.
func materializeTeamSkillsAndRules(teamCtxDir string, resp *api.TeamContextContentResponse) error {
	if resp.Skills == nil && resp.Rules == nil {
		return nil
	}
	// A real checkout is authoritative for its own tracked files; overwriting or
	// sweeping them from HTTP would dirty the clone the daemon syncs.
	if _, err := os.Lstat(filepath.Join(teamCtxDir, ".git")); err == nil {
		slog.Debug("ephemeral team-context fallback: git checkout present, leaving skills and rules to it", "dir", teamCtxDir)
		return nil
	}
	root, err := os.OpenRoot(teamCtxDir)
	if err != nil {
		return fmt.Errorf("open team-context dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	if resp.Skills != nil {
		if err := materializeServedSkills(root, teamCtxDir, resp.Skills, resp.SkillsTruncated); err != nil {
			return err
		}
	}
	if resp.Rules != nil {
		// skills_truncated is the bundle's only incompleteness signal — the server
		// also sets it when it could not read the Team Context at all — so it
		// stops rule retirement too: rules carry no flag of their own.
		if err := materializeServedRules(root, resp.Rules, !resp.SkillsTruncated); err != nil {
			return err
		}
	}
	return nil
}

func materializeServedSkills(root *os.Root, teamCtxDir string, served []api.TeamContextSkill, truncated bool) error {
	clearStaleSkillStages(root)
	keep := make(map[string]bool, len(served))
	written := 0
	for _, skill := range served {
		rel, reason := servedSkillLocation(skill)
		if reason == "" && keep[rel] {
			reason = "listed twice"
		}
		if reason == "" {
			reason = servedExecutableMismatch(skill)
		}
		if reason != "" {
			slog.Warn("ephemeral team-context fallback: refusing served skill", "dir", skill.Dir, "root", skill.Root, "reason", reason)
			continue
		}
		if err := writeServedSkill(root, teamCtxDir, rel, skill.Files); err != nil {
			return fmt.Errorf("write team skill %s: %w", rel, err)
		}
		keep[rel] = true
		written++
	}
	swept := 0
	if truncated {
		// Truncated means the list is incomplete — skills dropped to fit the cap,
		// or none readable at all — so an absent skill may still exist: keep what
		// earlier primes wrote rather than retire it.
		slog.Info("ephemeral team-context fallback: skills truncated by server, keeping unlisted skills", "served", len(served))
	} else {
		var err error
		if swept, err = sweepServedSkills(root, keep); err != nil {
			return err
		}
	}
	if err := writeHTTPSnapshot(root, served, truncated); err != nil {
		return err
	}
	slog.Debug("ephemeral team-context fallback: materialized skills",
		"served", len(served), "written", written, "swept", swept, "truncated", truncated)
	return nil
}

// servedSkillLocation validates where a served skill would land and returns its
// slash-separated path. The server is trusted for content, not for paths: a
// skill dir is one safe segment under a known root, the same rule the
// checkout's own discovery enforces by walking exactly one level.
func servedSkillLocation(skill api.TeamContextSkill) (string, string) {
	knownRoot := false
	for _, skillRoot := range teamdocs.SkillRoots {
		if skill.Root == skillRoot {
			knownRoot = true
		}
	}
	if !knownRoot {
		return "", "root is not a Team Context skills directory"
	}
	if !safeSegment(skill.Dir) {
		return "", "dir is not a single safe path segment"
	}
	for _, file := range skill.Files {
		if !safeRelativePath(file.Path) {
			return "", "file path escapes the skill directory"
		}
	}
	return skill.Root + "/" + skill.Dir, ""
}

// servedExecutableMismatch fails closed when the server calls a file
// executable and ox's own predicate would not. ox's classifier is what gates
// approval, so a disagreement would let runnable content through as prose.
func servedExecutableMismatch(skill api.TeamContextSkill) string {
	for _, file := range skill.Files {
		if !file.Executable {
			continue
		}
		if runnable, _ := teamskills.IsExecutableFile(file.Path, []byte(file.Content)); !runnable {
			return "server marks " + file.Path + " executable but ox would not gate it"
		}
	}
	return ""
}

func safeSegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." && !strings.ContainsAny(segment, "/\\\x00")
}

func safeRelativePath(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\\\x00") || path.IsAbs(rel) || filepath.IsAbs(rel) ||
		filepath.VolumeName(rel) != "" || path.Clean(rel) != rel {
		return false
	}
	for _, segment := range strings.Split(rel, "/") {
		if !safeSegment(segment) {
			return false
		}
	}
	return true
}

// writeServedSkill stages the whole skill, then swaps it into place, so a
// reader never sees a mix of two versions of one skill.
func writeServedSkill(root *os.Root, teamCtxDir, rel string, files []api.TeamContextSkillFile) error {
	stage, err := os.MkdirTemp(teamCtxDir, httpSkillStagePrefix)
	if err != nil {
		return fmt.Errorf("stage skill: %w", err)
	}
	stageName := filepath.Base(stage)
	installed := false
	defer func() {
		if !installed {
			_ = root.RemoveAll(stageName)
		}
	}()
	for _, file := range files {
		dest := filepath.Join(stageName, filepath.FromSlash(file.Path))
		if err := root.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		// 0644 whatever the server says: nothing reads the mode from here, and
		// projection decides what is runnable after approval.
		if err := root.WriteFile(dest, []byte(file.Content), 0o644); err != nil {
			return err
		}
	}

	final := filepath.FromSlash(rel)
	if err := root.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	previous := stageName + ".old"
	hadPrevious := false
	if _, err := root.Lstat(final); err == nil {
		if err := root.Rename(final, previous); err != nil {
			return err
		}
		hadPrevious = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.Rename(stageName, final); err != nil {
		if hadPrevious {
			_ = root.Rename(previous, final)
		}
		return err
	}
	installed = true
	if hadPrevious {
		_ = root.RemoveAll(previous)
	}
	return nil
}

func clearStaleSkillStages(root *os.Root) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), httpSkillStagePrefix) {
			_ = root.RemoveAll(entry.Name())
		}
	}
}

// sweepServedSkills retires skills the server no longer lists, so reconcile
// removes their projection just as it would after a checkout's pull.
func sweepServedSkills(root *os.Root, keep map[string]bool) (int, error) {
	swept := 0
	for _, skillRoot := range teamdocs.SkillRoots {
		entries, err := fs.ReadDir(root.FS(), skillRoot)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return swept, fmt.Errorf("read %s: %w", skillRoot, err)
		}
		for _, entry := range entries {
			rel := skillRoot + "/" + entry.Name()
			if keep[rel] {
				continue
			}
			if err := root.RemoveAll(filepath.FromSlash(rel)); err != nil {
				return swept, fmt.Errorf("retire %s: %w", rel, err)
			}
			swept++
		}
	}
	return swept, nil
}

// writeHTTPSnapshot records which skill set this directory holds. It is the
// revision prime's skill fast path keys on when there is no git HEAD, so a
// changed or retired skill re-plans instead of riding a stale revision.
func writeHTTPSnapshot(root *os.Root, served []api.TeamContextSkill, truncated bool) error {
	encoded, err := json.Marshal(struct {
		Skills    []api.TeamContextSkill `json:"skills"`
		Truncated bool                   `json:"truncated"`
	}{served, truncated})
	if err != nil {
		return fmt.Errorf("encode skill snapshot: %w", err)
	}
	sum := sha256.Sum256(encoded)
	marker := filepath.FromSlash(skillmanager.HTTPSnapshotFile)
	if err := root.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return err
	}
	return root.WriteFile(marker, []byte(hex.EncodeToString(sum[:])+"\n"), 0o644)
}

func materializeServedRules(root *os.Root, served []api.TeamContextRule, sweep bool) error {
	keep := make(map[string]bool, len(served))
	for _, rule := range served {
		if !servedRulePath(rule.Path) || keep[rule.Path] {
			slog.Warn("ephemeral team-context fallback: refusing served rule", "path", rule.Path)
			continue
		}
		dest := filepath.FromSlash(rule.Path)
		if err := root.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("write team rule %s: %w", rule.Path, err)
		}
		tmp := dest + httpRuleTempSuffix
		if err := root.WriteFile(tmp, []byte(rule.Content), 0o644); err != nil {
			return fmt.Errorf("write team rule %s: %w", rule.Path, err)
		}
		if err := root.Rename(tmp, dest); err != nil {
			_ = root.Remove(tmp)
			return fmt.Errorf("write team rule %s: %w", rule.Path, err)
		}
		keep[rule.Path] = true
	}
	if !sweep {
		return nil
	}
	for _, ruleRoot := range teamdocs.RuleRoots {
		err := fs.WalkDir(root.FS(), ruleRoot, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || keep[p] {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(p), ".md") || strings.HasSuffix(p, httpRuleTempSuffix) {
				if err := root.Remove(filepath.FromSlash(p)); err != nil {
					slog.Debug("ephemeral team-context fallback: failed to retire stale rule", "path", p, "err", err.Error())
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("sweep %s: %w", ruleRoot, err)
		}
	}
	return nil
}

// servedRulePath accepts a markdown file under a Team Context rules root —
// recursively, as rule discovery walks.
func servedRulePath(rel string) bool {
	if !safeRelativePath(rel) || !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return false
	}
	for _, ruleRoot := range teamdocs.RuleRoots {
		if strings.HasPrefix(rel, ruleRoot+"/") {
			return true
		}
	}
	return false
}
