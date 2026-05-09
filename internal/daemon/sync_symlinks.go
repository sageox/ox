package daemon

// Per-project knowledge-bubble symlink reconciliation.
//
// For every initialized project on this machine, materialize an
// ergonomic symlink set under <project>/.sageox/kb/<slug> pointing at
// the canonical paths.KBDir(kb_id). The reconciler is the daemon-side
// "F2 + D2" surface from the kb plan: bubbles live in one canonical
// XDG location (single copy per machine) and each project gets a
// slug-keyed view of the subset relevant to it.
//
// Policy (matches "Which bubbles get a symlink in a given project"):
//
//   - personal, profile  -> link in every initialized project
//   - team               -> link in every initialized project (the API
//                            already filters to bubbles the caller can
//                            access, so a team row implies membership)
//   - repo               -> link only in its OWN project, matched by
//                            kb.repo_id == projectConfig.repo_id
//   - custom, unknown    -> skip (deferred until explicit subscribe)
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
	"fmt"
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

// reconcileAllProjectSymlinks walks every initialized project on this
// machine (as known to the daemon registry) and reconciles its
// .sageox/kb/ symlink set against the bubbles list. Called once per
// kb sync pass after every per-bubble clone/pull has finished, so the
// canonical KBDir(kb_id) targets exist before we point at them.
//
// Failure isolation: a single bad project (permissions, deleted root,
// dirty registry entry) does not abort the others.
func (s *SyncScheduler) reconcileAllProjectSymlinks(ctx context.Context, bubbles []api.KB) {
	roots := discoverInitializedProjectRoots(s.config.ProjectRoot)
	if len(roots) == 0 {
		s.logger.Debug("kb_symlink: no initialized projects discovered")
		return
	}

	for _, root := range roots {
		// per-project containment — never let one project corrupt the
		// scheduler tick for the others.
		s.reconcileProjectSymlinks(ctx, root, bubbles)
	}
}

// discoverInitializedProjectRoots returns the set of initialized
// project roots on this machine. Reuses the daemon registry (the same
// store used to find ledger sync targets) plus the current daemon's
// own project root, which may not yet be in the registry on first run.
// Filters via config.IsInitialized so a stale registry entry pointing
// at a deleted directory is silently ignored.
func discoverInitializedProjectRoots(currentRoot string) []string {
	seen := make(map[string]bool)
	var out []string

	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if seen[abs] {
			return
		}
		if !config.IsInitialized(abs) {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}

	add(currentRoot)

	// Registry.LoadRegistry is best-effort — a missing or corrupted
	// registry just means we only operate on the current project.
	reg, err := LoadRegistry()
	if err != nil || reg == nil {
		return out
	}
	for _, info := range reg.Daemons {
		add(info.WorkspacePath)
	}
	return out
}

// reconcileProjectSymlinks brings <projectRoot>/.sageox/kb/ into the
// state implied by `bubbles` for this project's policy. Idempotent.
// Holds an advisory file lock for the duration so a foreground
// `ox init` in the same root cannot race with us.
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
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		s.logger.Debug("kb_symlink load project config failed", "project", projectRoot, "error", err)
		// keep going — repo policy below tolerates an empty repo_id
		// (will skip repo bubbles for this project, which is the
		// correct safe default).
		cfg = &config.ProjectConfig{}
	}

	desired := desiredSymlinks(bubbles, cfg.RepoID)

	kbDir := filepath.Join(projectRoot, ".sageox", "kb")
	lockTarget := filepath.Join(projectRoot, ".sageox", "kb.lock-target")

	// 30s is generous; the whole pass is symlink syscalls, but a
	// foreground `ox init` may hold the lock briefly during
	// gitignore/file writes.
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err = fileutil.WithFileLock(lockCtx, lockTarget, func() error {
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
		for slug, kbID := range desired {
			target := paths.KBDir(kbID)
			if target == "" {
				continue
			}
			linkPath := filepath.Join(kbDir, slug)
			existing, ok := current[slug]
			if ok && existing == target {
				continue // already correct — true no-op
			}
			if err := writeSymlinkAtomic(linkPath, target, ops); err != nil {
				s.logger.Warn("kb_symlink write failed", "project", projectRoot, "slug", slug, "error", err)
				continue
			}
			ops.createSym++
			action := "created"
			if ok {
				action = "replaced"
			}
			s.logger.Info("kb_symlink", "project", projectRoot, "slug", slug, "kb_id", kbID, "action", action)
		}

		// 2. prune links present locally but not in desired (kb GC'd
		// or revoked, or slug renamed — the desired-side already wrote
		// the new slug above; here we just sweep the old name).
		for slug := range current {
			if _, want := desired[slug]; want {
				continue
			}
			linkPath := filepath.Join(kbDir, slug)
			if err := ops.remove(linkPath); err != nil && !os.IsNotExist(err) {
				s.logger.Warn("kb_symlink prune failed", "project", projectRoot, "slug", slug, "error", err)
				continue
			}
			s.logger.Info("kb_symlink", "project", projectRoot, "slug", slug, "action", "pruned")
		}
		return nil
	})

	if err != nil {
		s.logger.Warn("kb_symlink reconciliation failed", "project", projectRoot, "error", err)
	}
}

// desiredSymlinks computes the slug -> kb_id map of bubbles that
// should be linked into a project whose config.RepoID is `projectRepoID`.
// Centralized so tests can verify policy without spinning the daemon.
func desiredSymlinks(bubbles []api.KB, projectRepoID string) map[string]string {
	out := make(map[string]string, len(bubbles))
	for _, b := range bubbles {
		if b.KBID == "" || b.Slug == "" {
			continue
		}
		switch b.KBType {
		case api.KBTypePersonal, api.KBTypeProfile, api.KBTypeTeam:
			// every initialized project gets a link.
		case api.KBTypeRepo:
			// repo bubbles only land in their own project. If we
			// don't know the project's repo_id (un-initialized config
			// or legacy state) or the bubble doesn't carry one,
			// we skip rather than silently linking everywhere.
			if projectRepoID == "" || b.RepoID == "" || b.RepoID != projectRepoID {
				continue
			}
		default:
			// custom, unknown — deferred, skip.
			continue
		}
		out[b.Slug] = b.KBID
	}
	return out
}

// readCurrentSymlinks returns slug -> resolved-target for every entry
// inside kbDir. Skips non-symlinks defensively (a stray file someone
// dropped in the dir is left alone — never deleted) so the reconciler
// only ever owns the symlinks it created.
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
		if info.Mode()&os.ModeSymlink == 0 {
			// not a symlink — leave it alone, never delete.
			continue
		}
		target, err := ops.readlink(full)
		if err != nil {
			// unreadable symlink — treat as "current = empty" so the
			// next pass replaces it.
			out[e.Name()] = ""
			continue
		}
		out[e.Name()] = target
	}
	return out, nil
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
