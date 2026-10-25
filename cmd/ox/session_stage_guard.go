package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
)

// Session artifacts (raw.jsonl, context-trace.jsonl, ...) are committed to a
// Ledger as LFS pointers. Anything that hydrates a session into the working
// tree (the doctor secret scan, `ox session hydrate`, `ox fetch`) leaves real
// content at the pointer's path, and a blind `git add <sessionsDir>` then
// commits those bytes. The push validator rejects every such commit, so the
// Ledger wedges (#1174). Every automatic writer that stages sessions goes
// through the guard below instead.

// sessionStageGuard classifies session artifacts for one repository before
// they are staged.
type sessionStageGuard struct {
	repoDir     string // directory git commands run in; session paths are relative to it
	sessionsDir string // absolute sessions directory: <sessionsDir>/<session>/<file>
	cacheRoot   string // where hydrated content is preserved when a pointer is restored
}

func newSessionStageGuard(repoDir, sessionsDir string) *sessionStageGuard {
	return &sessionStageGuard{
		repoDir:     repoDir,
		sessionsDir: sessionsDir,
		cacheRoot:   filepath.Join(repoDir, ".sageox", "cache", "sessions"),
	}
}

// sessionGuardOutcome is the guard's verdict on a set of paths.
type sessionGuardOutcome struct {
	Stage    []string // safe to stage or commit as they are on disk now
	Restored []string // rewritten to a pointer first; included in Stage
	Skipped  []string // raw content nothing vouches for; never staged, left untouched
}

// check returns a verdict for every path (relative to repoDir). Raw content
// whose sha256 matches a committed pointer or the session manifest is rewritten
// to that pointer; raw content with no known OID is skipped with one WARN.
func (g *sessionStageGuard) check(rels []string) sessionGuardOutcome {
	var out sessionGuardOutcome
	for _, rel := range rels {
		restored, skipReason := g.guardOne(rel)
		if skipReason != "" {
			out.Skipped = append(out.Skipped, rel)
			slog.Warn("session artifact not staged",
				"path", filepath.ToSlash(rel), "reason", skipReason)
			continue
		}
		out.Stage = append(out.Stage, rel)
		if restored {
			out.Restored = append(out.Restored, rel)
		}
	}
	return out
}

func (g *sessionStageGuard) guardOne(rel string) (restored bool, skipReason string) {
	abs := filepath.Join(g.repoDir, rel)
	within, err := filepath.Rel(g.sessionsDir, abs)
	if err != nil {
		return false, ""
	}
	parts := strings.Split(filepath.ToSlash(within), "/")
	if len(parts) < 2 || parts[0] == ".." {
		return false, ""
	}
	name := strings.Join(parts[1:], "/")
	if info, err := os.Lstat(abs); err != nil || !info.Mode().IsRegular() {
		return false, "" // a deletion or an oddity; nothing to restore
	}

	sessionDir := filepath.Join(g.sessionsDir, parts[0])
	meta, metaErr := lfs.ReadSessionMeta(sessionDir)
	if metaErr != nil {
		meta = nil
	}
	// a draft directory holds only meta.json (.claude/rules/cache-only-design.md)
	if meta.IsDraft() && name != "meta.json" {
		return false, "session is a draft; a draft directory holds only meta.json"
	}
	isContent := lfs.IsContentArtifact(name)
	// unreadable metadata hides whether this is a draft directory; fail closed for anything but meta.json
	if metaErr != nil && !errors.Is(metaErr, fs.ErrNotExist) && !isContent && name != "meta.json" {
		return false, fmt.Sprintf("cannot read session metadata to rule out a draft: %v", metaErr)
	}
	if !isContent || lfs.IsPointerFile(abs) {
		return false, ""
	}
	if meta.StoredInGit(name) {
		return false, "" // Storage=git: raw content is correct here
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return false, fmt.Sprintf("cannot read file: %v", err)
	}
	ref, source, ok := lfs.ResolveRestorableRef(content, name, meta, g.committedPointer(rel))
	if !ok {
		return false, "no meta.json entry or committed pointer carries this content's sha256; hydrate-only content, left untouched"
	}
	cachePath := filepath.Join(g.cacheRoot, filepath.FromSlash(parts[0]), filepath.FromSlash(name))
	if err := lfs.RestorePointer(abs, cachePath, content, ref); err != nil {
		return false, fmt.Sprintf("cannot restore pointer from %s: %v", source, err)
	}
	slog.Info("session artifact restored to LFS pointer", "path", filepath.ToSlash(rel), "source", string(source))
	return true, ""
}

// committedPointer returns the pointer HEAD holds at rel, or nil.
func (g *sessionStageGuard) committedPointer(rel string) *lfs.FileRef {
	out, err := exec.Command("git", "-C", g.repoDir, "show", "HEAD:./"+filepath.ToSlash(rel)).Output()
	if err != nil {
		return nil
	}
	oid, size, err := lfs.ParsePointer(string(out))
	if err != nil {
		return nil
	}
	return &lfs.FileRef{Storage: lfs.StorageLFS, OID: oid, Size: size}
}

// listCandidates lists session files git would stage under sessionsDir: changed
// and untracked files (deletions included), plus files already staged by hand.
func (g *sessionStageGuard) listCandidates(ctx context.Context) ([]string, error) {
	listings := [][]string{
		{"ls-files", "-z", "--modified", "--others", "--exclude-standard", "--", g.sessionsDir},
		{"diff", "--cached", "--name-only", "-z", "--relative", "--", g.sessionsDir},
	}
	seen := map[string]bool{}
	var rels []string
	for _, args := range listings {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", g.repoDir}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		for _, rel := range strings.Split(string(out), "\x00") {
			if rel != "" && !seen[rel] {
				seen[rel] = true
				rels = append(rels, rel)
			}
		}
	}
	return rels, nil
}

// stage guards every candidate under sessionsDir and `git add`s only the safe
// ones, one explicit path each, never the directory.
func (g *sessionStageGuard) stage(ctx context.Context) (sessionGuardOutcome, error) {
	rels, err := g.listCandidates(ctx)
	if err != nil {
		return sessionGuardOutcome{}, err
	}
	// `git add` would commit conflict markers as if resolved (#1055, #1189); leave them for a human
	var clean, marked []string
	for _, rel := range rels {
		abs := filepath.Join(g.repoDir, rel)
		// a symlink is staged as its target path, so reading through it would scan unrelated bytes
		if info, err := os.Lstat(abs); err == nil && info.Mode().IsRegular() {
			if has, err := gitutil.HasConflictMarkers(abs); err == nil && has {
				slog.Warn("session file not staged", "path", filepath.ToSlash(rel), "reason", "contains unresolved conflict markers")
				marked = append(marked, rel)
				continue
			}
		}
		clean = append(clean, rel)
	}
	out := g.check(clean)
	out.Skipped = append(out.Skipped, marked...)
	if err := g.gitPathspec(ctx, out.Stage, "add", "--sparse"); err != nil {
		return out, fmt.Errorf("stage sessions: %w", err)
	}
	return out, nil
}

// guardIndex re-checks the files already staged under sessions/ by a broader
// `git add` (doctor repair paths use `add -A`). Restored files are re-staged as
// pointers; skipped files are unstaged (index only, content stays on disk).
func (g *sessionStageGuard) guardIndex(ctx context.Context) (sessionGuardOutcome, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", g.repoDir,
		"diff", "--cached", "--name-only", "-z", "--diff-filter=d", "--relative", "--", g.sessionsDir).Output()
	if err != nil {
		return sessionGuardOutcome{}, fmt.Errorf("list staged sessions: %w", err)
	}
	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			rels = append(rels, rel)
		}
	}
	outcome := g.check(rels)
	if err := g.gitPathspec(ctx, outcome.Restored, "add", "--sparse"); err != nil {
		return outcome, fmt.Errorf("re-stage restored pointers: %w", err)
	}
	if err := g.gitPathspec(ctx, outcome.Skipped, "reset", "-q"); err != nil {
		return outcome, fmt.Errorf("unstage raw session content: %w", err)
	}
	return outcome, nil
}

// gitPathspec runs `git <args> --pathspec-from-file=- --pathspec-file-nul` for rels.
func (g *sessionStageGuard) gitPathspec(ctx context.Context, rels []string, args ...string) error {
	if len(rels) == 0 {
		return nil
	}
	var stdin bytes.Buffer
	for _, rel := range rels {
		stdin.WriteString(rel)
		stdin.WriteByte(0)
	}
	full := append([]string{"-C", g.repoDir}, args...)
	full = append(full, "--pathspec-from-file=-", "--pathspec-file-nul")
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Stdin = &stdin
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
