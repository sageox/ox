package daemon

// Per-project knowledge-bubble symlink reconciliation.
//
// Materialize an ergonomic symlink set under
// <project>/.sageox/kb/team/<slug> pointing at the canonical
// paths.KBDir(kb_id). The reconciler is the daemon-side "F2 + D2"
// surface from the kb plan: bubbles live in one canonical XDG location
// (single copy per machine) and the project gets a slug-keyed view.
//
// Policy (ADR-028 §4): every bubble returned by the ambient scoped
// fetch links into the CURRENT project. The scoped list is already
// per-team and members-only (ADR-073), so a row's presence implies
// relevance — there is no per-type policy switch and no repo_id
// matching anymore.
//
// Layout: links live under a scope subdirectory that mirrors the
// server's URL split (/t/<team>/kb/<slug>):
//
//	<project>/.sageox/kb/team/<slug>   — team-scope bubbles (v1)
//	<project>/.sageox/kb/me/<slug>     — RESERVED for the deferred
//	                                     personal scope; nothing is
//	                                     created there today.
//
// Stale symlinks directly under .sageox/kb/<slug> are from the old flat
// layout and are removed on reconcile (the dir is gitignored, so the
// removal never touches tracked files).
//
// Scoping: reconciliation is restricted to the daemon's OWN project
// root (s.config.ProjectRoot). Under the scoped fetch the bubbles
// belong to THIS project's team; fanning out to every initialized
// project on the machine (the old behavior) would link another team's
// bubbles into unrelated projects.
//
// Critical safety:
//   - We only ever create/replace/remove symlinks. The canonical kb
//     dir at paths.KBDir(...) is GC'd elsewhere; this code never
//     follows or recurses into a symlink's target.
//   - A per-project advisory file lock serializes concurrent
//     reconciliations against a foreground `ox init` in the same
//     project root.
//   - Per-symlink failures are isolated; one bad slug never aborts
//     the rest of the pass.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// projectGitignoreLine is the single entry the reconciler appends to a
// project's root .gitignore so per-project bubble symlinks never leak
// into a commit. Idempotent: appended only when missing.
const projectGitignoreLine = ".sageox/kb/"

// kbTeamScopeDir is the scope subdirectory under <project>/.sageox/kb/
// that holds team-scope bubble symlinks. Mirrors the server's
// /t/<team>/kb/<slug> URL split. "me/" is reserved for the deferred
// personal scope (ox-cag9.8) — nothing creates it yet.
const kbTeamScopeDir = "team"

// symlinkOps is a tiny seam used by tests to count syscalls (proving
// idempotency: a no-op pass must not call os.Symlink). Production uses
// the realSymlinkOps backed by the standard library.
type symlinkOps struct {
	symlink   func(target, link string) error
	readlink  func(link string) (string, error)
	remove    func(link string) error
	rename    func(oldPath, newPath string) error
	mkdirAll  func(path string, perm os.FileMode) error
	lstat     func(name string) (os.FileInfo, error)
	createSym int // counter — bumped by the reconciler, not by ops themselves
}

func defaultSymlinkOps() *symlinkOps {
	return &symlinkOps{
		symlink:  os.Symlink,
		readlink: os.Readlink,
		remove:   os.Remove,
		rename:   os.Rename,
		mkdirAll: os.MkdirAll,
		lstat:    os.Lstat,
	}
}

// reconcileOwnProjectSymlinks reconciles the daemon's own project root
// against the bubbles list. Called once per kb sync pass after every
// per-bubble clone/pull has finished, so the canonical KBDir(kb_id)
// targets exist before we point at them.
//
// Own-project only: the scoped fetch returns bubbles for THIS project's
// team, so other projects on the machine (each with their own daemon)
// reconcile their own team's bubbles themselves.
func (s *SyncScheduler) reconcileOwnProjectSymlinks(ctx context.Context, bubbles []api.KB) {
	root := s.config.ProjectRoot
	if root == "" {
		return
	}
	if !config.IsInitialized(root) {
		s.logger.Debug("kb_symlink: project not initialized, skipping", "project", root)
		return
	}
	s.reconcileProjectSymlinks(ctx, root, bubbles)
}

// reconcileProjectSymlinks brings <projectRoot>/.sageox/kb/ into the
// state implied by `bubbles`. Idempotent. Holds an advisory file lock
// for the duration so a foreground `ox init` in the same root cannot
// race with us.
func (s *SyncScheduler) reconcileProjectSymlinks(ctx context.Context, projectRoot string, bubbles []api.KB) {
	if projectRoot == "" {
		return
	}
	ops := defaultSymlinkOps()
	s.reconcileProjectSymlinksWithOps(ctx, projectRoot, bubbles, ops)
}

// reconcileProjectSymlinksWithOps is the testable core. The ops seam
// lets tests count os.Symlink invocations (idempotency proof) and
// inject failures (per-symlink failure-isolation proof).
func (s *SyncScheduler) reconcileProjectSymlinksWithOps(ctx context.Context, projectRoot string, bubbles []api.KB, ops *symlinkOps) {
	desired := desiredSymlinks(bubbles)

	kbDir := filepath.Join(projectRoot, ".sageox", "kb")
	lockTarget := filepath.Join(projectRoot, ".sageox", "kb.lock-target")

	// 30s is generous; the whole pass is symlink syscalls, but a
	// foreground `ox init` may hold the lock briefly during
	// gitignore/file writes.
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err := fileutil.WithFileLock(lockCtx, lockTarget, func() error {
		// ensure project's root .gitignore excludes .sageox/kb/. Done
		// inside the lock so two daemons don't both append the line.
		if err := ensureProjectGitignoreEntry(projectRoot); err != nil {
			s.logger.Warn("kb_symlink gitignore update failed", "project", projectRoot, "error", err)
			// non-fatal — symlink reconciliation continues.
		}

		if err := ops.mkdirAll(kbDir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", kbDir, err)
		}

		current, err := readCurrentSymlinks(kbDir, ops)
		if err != nil {
			return fmt.Errorf("read current symlinks: %w", err)
		}

		// 1. create/replace links present in desired.
		for rel, kbID := range desired {
			target := paths.KBDir(s.kbEndpoint(), kbID)
			if target == "" {
				continue
			}
			linkPath := filepath.Join(kbDir, filepath.FromSlash(rel))
			existing, ok := current[rel]
			if ok && existing == target {
				continue // already correct — true no-op
			}
			if err := ops.mkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
				s.logger.Warn("kb_symlink scope dir create failed", "project", projectRoot, "link", rel, "error", err)
				continue
			}
			if err := writeSymlinkAtomic(linkPath, target, ops); err != nil {
				s.logger.Warn("kb_symlink write failed", "project", projectRoot, "link", rel, "error", err)
				continue
			}
			ops.createSym++
			action := "created"
			if ok {
				action = "replaced"
			}
			s.logger.Info("kb_symlink", "project", projectRoot, "link", rel, "kb_id", kbID, "action", action)
		}

		// 2. prune links present locally but not in desired. This covers
		// revoked/renamed bubbles AND stale flat-layout symlinks directly
		// under .sageox/kb/ (pre-scope layout): flat entries can never be
		// in desired (its keys are "team/<slug>"), so they're swept here.
		for rel := range current {
			if _, want := desired[rel]; want {
				continue
			}
			linkPath := filepath.Join(kbDir, filepath.FromSlash(rel))
			if err := ops.remove(linkPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.logger.Warn("kb_symlink prune failed", "project", projectRoot, "link", rel, "error", err)
				continue
			}
			s.logger.Info("kb_symlink", "project", projectRoot, "link", rel, "action", "pruned")
		}
		return nil
	})

	if err != nil {
		s.logger.Warn("kb_symlink reconciliation failed", "project", projectRoot, "error", err)
	}
}

// desiredSymlinks computes the link-relative-path -> kb_id map for a
// project. Every bubble returned by the ambient scoped fetch gets a
// link under the team scope subdirectory ("team/<slug>") — the scoped
// list already guarantees relevance, so there is no per-type policy.
// Centralized so tests can verify the mapping without spinning the daemon.
func desiredSymlinks(bubbles []api.KB) map[string]string {
	out := make(map[string]string, len(bubbles))
	for _, b := range bubbles {
		if b.KBID == "" || b.Slug == "" {
			continue
		}
		// Slugs are server-normalized kebab-case, but the daemon must not
		// trust that: a slug carrying a path separator or dot-segment would
		// let filepath.Join escape .sageox/kb/team/. Reject anything that
		// isn't a clean single path component.
		if !isSafeSlugComponent(b.Slug) {
			slog.Warn("kb_symlink skipping bubble with unsafe slug", "kb_id", b.KBID, "slug", b.Slug)
			continue
		}
		out[kbTeamScopeDir+"/"+b.Slug] = b.KBID
	}
	return out
}

// isSafeSlugComponent reports whether s is usable as exactly one relative
// path component: no separators, no traversal, no platform-specific prefixes.
func isSafeSlugComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	// filepath.Base/Clean round-trip catches anything platform-specific
	// (e.g. Windows volume names) the character check misses.
	return filepath.Clean(s) == s && filepath.Base(s) == s
}

// readCurrentSymlinks returns link-relative-path -> resolved-target for
// every symlink inside kbDir, descending exactly one level into scope
// subdirectories (team/, and any future sibling like me/). Top-level
// entries are keyed by bare name ("<slug>", the legacy flat layout);
// scoped entries by "<scope>/<slug>". Skips non-symlinks defensively (a
// stray file someone dropped in the dir is left alone — never deleted)
// so the reconciler only ever owns the symlinks it created.
func readCurrentSymlinks(kbDir string, ops *symlinkOps) (map[string]string, error) {
	out := make(map[string]string)
	entries, err := os.ReadDir(kbDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		full := filepath.Join(kbDir, e.Name())
		info, err := ops.lstat(full)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			recordSymlink(out, e.Name(), full, ops)
			continue
		}
		if !info.IsDir() {
			// plain file — leave it alone, never delete.
			continue
		}
		// scope subdirectory (team/ or a future sibling): collect its
		// symlinks one level down. Never recurse deeper.
		subEntries, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			subFull := filepath.Join(full, se.Name())
			subInfo, err := ops.lstat(subFull)
			if err != nil || subInfo.Mode()&os.ModeSymlink == 0 {
				continue // non-symlinks inside scope dirs are left alone too
			}
			recordSymlink(out, e.Name()+"/"+se.Name(), subFull, ops)
		}
	}
	return out, nil
}

// recordSymlink resolves one symlink's target into the current-state
// map. An unreadable symlink is recorded with an empty target so the
// next pass replaces it.
func recordSymlink(out map[string]string, rel, full string, ops *symlinkOps) {
	target, err := ops.readlink(full)
	if err != nil {
		out[rel] = ""
		return
	}
	out[rel] = target
}

// writeSymlinkAtomic creates or replaces the symlink at linkPath
// pointing at target. Uses the temp-name + rename pattern so a reader
// observing the path either sees the old link or the new one — never
// "no link".
func writeSymlinkAtomic(linkPath, target string, ops *symlinkOps) error {
	tmp := linkPath + ".tmp-" + tmpSuffix()
	// in case a previous tmp got left behind by a crash.
	_ = ops.remove(tmp)
	if err := ops.symlink(target, tmp); err != nil {
		return fmt.Errorf("create temp symlink: %w", err)
	}
	if err := ops.rename(tmp, linkPath); err != nil {
		// best-effort cleanup of the orphan temp.
		_ = ops.remove(tmp)
		return fmt.Errorf("rename symlink into place: %w", err)
	}
	return nil
}

// tmpSuffix returns a per-call suffix for the temp symlink. Time-based
// is enough — the lock above already serializes writers per project,
// and collisions across projects share neither the directory nor the
// link path.
func tmpSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ensureProjectGitignoreEntry appends `.sageox/kb/` to the project's
// root `.gitignore` exactly once. Idempotent: re-reads the file each
// call and only appends when the line is genuinely missing.
//
// Why touch the project's root .gitignore (and not just .sageox/.gitignore):
// The existing .sageox/.gitignore already excludes most cache content
// nested under the dir, but the bead spec is explicit: write to the
// project's root .gitignore. This keeps the rule visible to humans
// browsing the repo from the top.
func ensureProjectGitignoreEntry(projectRoot string) error {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")
	existing, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	if hasGitignoreLine(string(existing), projectGitignoreLine) {
		return nil
	}
	// preserve trailing newline expectations: append on its own line
	// even if the existing file didn't end with one.
	var buf strings.Builder
	buf.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(projectGitignoreLine)
	buf.WriteString("\n")
	return os.WriteFile(gitignorePath, []byte(buf.String()), 0o644)
}

// hasGitignoreLine reports whether `content` already lists `line` as
// a non-comment, non-blank entry. Comment-aware so a line buried in a
// `# .sageox/kb/` comment doesn't fool us into skipping the append.
func hasGitignoreLine(content, line string) bool {
	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == line {
			return true
		}
	}
	return false
}
