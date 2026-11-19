package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
)

// CheckSlugBackupCleanup is the slug for the backup directory cleanup check.
const CheckSlugBackupCleanup = "backup-cleanup"

// staleBackupThreshold is the minimum age a .bak directory must have before
// it is flagged for cleanup. Recent backups may still be needed if a clone
// retry is in progress.
const staleBackupThreshold = 7 * 24 * time.Hour

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugBackupCleanup,
		Name:        "Backup directories",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelSuggested,
		Description: "Detects stale .bak.* directories (older than 7 days) left by failed clone operations; removes only those git can fully restore",
		Run: func(fix bool) checkResult {
			return checkBackupDirectories(fix)
		},
	})
}

// backupDirInfo holds metadata about a discovered backup directory.
type backupDirInfo struct {
	path        string
	timestamp   time.Time
	size        int64
	hasGit      bool
	dirty       bool     // holds something git cannot restore; --fix never deletes it
	dirtyFiles  []string // why dirty: porcelain lines, file paths (no .git), unpushed history, or inspection errors
	sessionDirs []string // .sageox/cache/sessions/<name> folders: Sacred-tier recordings
}

// checkBackupDirectories detects .bak.* sibling directories created by the daemon
// during failed clone operations and offers to clean them up.
//
// The daemon creates backups via: fmt.Sprintf("%s.bak.%d", payload.RepoPath, time.Now().Unix())
// These accumulate over time and waste disk space.
func checkBackupDirectories(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Backup directories", "not in git repo", "")
	}

	allBackups := discoverBackupDirs(gitRoot)

	// filter to only stale backups (older than threshold)
	now := time.Now()
	var backups []backupDirInfo
	for _, b := range allBackups {
		if now.Sub(b.timestamp) >= staleBackupThreshold {
			backups = append(backups, b)
		}
	}

	if len(backups) == 0 {
		return PassedCheck("Backup directories", "none found")
	}

	// calculate totals
	var totalSize int64
	var cleanCount, dirtyCount int
	for _, b := range backups {
		totalSize += b.size
		if b.dirty {
			dirtyCount++
		} else {
			cleanCount++
		}
	}

	if fix {
		return fixBackupDirectories(backups)
	}

	// report mode: show what was found
	sizeStr := humanSize(totalSize)
	msg := fmt.Sprintf("%d stale backup(s) older than 7 days, %s", len(backups), sizeStr)

	var detailLines []string
	for _, b := range backups {
		name := filepath.Base(b.path)
		dateStr := b.timestamp.Format("2006-01-02")
		bSizeStr := humanSize(b.size)
		status := "clean"
		switch {
		case len(b.sessionDirs) > 0:
			status = fmt.Sprintf("%d session recording folder(s), kept by --fix", len(b.sessionDirs))
		case b.dirty:
			status = fmt.Sprintf("%d thing(s) its remote cannot restore, kept by --fix", len(b.dirtyFiles))
		}
		detailLines = append(detailLines, fmt.Sprintf("  %s  %s  %s  (%s)", name, dateStr, bSizeStr, status))
	}
	detailLines = append(detailLines, "  Run `ox doctor --fix` to clean up")

	return WarningCheck("Backup directories", msg, strings.Join(detailLines, "\n"))
}

// discoverBackupDirs finds all .bak.* directories for the ledger and team context paths.
func discoverBackupDirs(gitRoot string) []backupDirInfo {
	var allBackups []backupDirInfo

	// collect paths to check for backups
	var pathsToCheck []string

	// ledger path from local config or default
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err == nil && localCfg != nil {
		if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
			pathsToCheck = append(pathsToCheck, localCfg.Ledger.Path)
		} else {
			// try default ledger path
			if defaultPath, err := ledger.DefaultPath(); err == nil && defaultPath != "" {
				pathsToCheck = append(pathsToCheck, defaultPath)
			}
		}

		// team context paths
		for _, tc := range localCfg.TeamContexts {
			if tc.Path != "" {
				pathsToCheck = append(pathsToCheck, tc.Path)
			}
		}
	}

	// also try project context for ledger path
	ctx, err := config.LoadProjectContext(gitRoot)
	if err == nil && ctx != nil {
		defaultLedgerPath := ctx.DefaultLedgerPath()
		if defaultLedgerPath != "" {
			pathsToCheck = append(pathsToCheck, defaultLedgerPath)
		}
	}

	// deduplicate paths
	seen := make(map[string]bool)
	var uniquePaths []string
	for _, p := range pathsToCheck {
		if !seen[p] {
			seen[p] = true
			uniquePaths = append(uniquePaths, p)
		}
	}

	for _, basePath := range uniquePaths {
		matches, err := filepath.Glob(basePath + ".bak.*")
		if err != nil {
			slog.Debug("backup glob failed", "path", basePath, "error", err)
			continue
		}

		for _, match := range matches {
			info := inspectBackupDir(match)
			if info != nil {
				allBackups = append(allBackups, *info)
			}
		}
	}

	return allBackups
}

// inspectBackupDir examines a single backup directory and returns its metadata.
// Returns nil if the path is not a directory.
func inspectBackupDir(path string) *backupDirInfo {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return nil
	}

	info := &backupDirInfo{
		path: path,
	}

	// parse timestamp from .bak.<unix_timestamp> suffix
	base := filepath.Base(path)
	if idx := strings.LastIndex(base, ".bak."); idx >= 0 {
		tsStr := base[idx+5:]
		if ts, err := strconv.ParseInt(tsStr, 10, 64); err == nil {
			info.timestamp = time.Unix(ts, 0)
		}
	}

	// if timestamp parsing failed, fall back to directory modification time
	if info.timestamp.IsZero() {
		info.timestamp = fi.ModTime()
	}

	// calculate directory size
	info.size = dirSize(path)

	// Anything its remote cannot restore pins the backup: uncommitted, untracked
	// and gitignored files alike, and history no remote holds. .sageox/cache/ is
	// gitignored, so a clean plain `git status` says nothing about the session
	// recordings a ledger self-heal moved aside with the clone.
	var outside []string
	var scanErr error
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		info.hasGit = true
		outside, scanErr = gitUnrestorable(path)
	} else {
		// no .git: nothing in this tree can be re-cloned
		outside, scanErr = backupFiles(path)
	}
	if scanErr != nil {
		// fail closed: the daemon moves corrupt repos aside, so an unreadable
		// backup is the likeliest one to hold the only copy of something
		outside = append(outside, "cannot inspect: "+scanErr.Error())
	}

	sessionDirs, err := sessionCacheDirs(path)
	if err != nil {
		outside = append(outside, "cannot inspect sessions: "+err.Error())
	}
	info.sessionDirs = sessionDirs
	info.dirtyFiles = outside
	info.dirty = len(outside) > 0 || len(sessionDirs) > 0

	return info
}

// gitUnrestorable lists what a backup repo holds that its remote cannot give
// back. A clean working tree says nothing about history: the daemon moves a repo
// aside whole, so commits never pushed and stashes exist only in the backup.
// Any check that cannot run returns its error, and the caller keeps the backup.
func gitUnrestorable(path string) ([]string, error) {
	reasons, err := backupGit(path, "status", "--porcelain", "--ignored", "--untracked-files=normal")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	// --branches, not HEAD: an empty repo has no HEAD commit, and a branch ref
	// is what a rebase or reset leaves the unpushed work on. --tags too: a
	// commit a reset left reachable only from a local tag is just as unpushed.
	unpushed, err := backupGit(path, "rev-list", "--branches", "--tags", "--not", "--remotes")
	if err != nil {
		return reasons, fmt.Errorf("list commits no remote holds: %w", err)
	}
	if len(unpushed) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d commit(s) on local branches or tags that no remote holds", len(unpushed)))
	}
	stashes, err := backupGit(path, "stash", "list")
	if err != nil {
		return reasons, fmt.Errorf("list stashes: %w", err)
	}
	if len(stashes) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d stash(es)", len(stashes)))
	}
	return reasons, nil
}

// backupGit runs git in a backup and returns its non-empty output lines.
// GIT_CEILING_DIRECTORIES stops discovery at the backup: with a corrupt .git, git
// would otherwise climb into an enclosing repo and report on that repo instead.
// For status, -unormal overrides a repo-local status.showUntrackedFiles=no that
// would hide untracked files.
func backupGit(path string, args ...string) ([]string, error) {
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(path))
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// backupFiles lists every non-directory entry under root, relative to root.
func backupFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("relative path: %w", err)
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return files, fmt.Errorf("walk %s: %w", root, err)
	}
	return files, nil
}

// sessionCacheDirs lists <backup>/.sageox/cache/sessions/<name>: session
// recordings (raw.jsonl, spool, stream state) that may exist nowhere else. Their
// presence alone pins the backup, whatever git reports about them.
func sessionCacheDirs(backupPath string) ([]string, error) {
	dir := filepath.Join(backupPath, ".sageox", "cache", "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		dirs = append(dirs, filepath.Join(dir, e.Name()))
	}
	return dirs, nil
}

// fixBackupDirectories removes backups whose git history restores every file and
// keeps the rest. Kept session recordings stay in place rather than being restored
// automatically: the live ledger cache may already hold a folder of the same name,
// and which copy wins is a person's call.
func fixBackupDirectories(backups []backupDirInfo) checkResult {
	var removed int
	var skippedPaths, sessionDirs []string

	for _, b := range backups {
		if b.dirty {
			slog.Warn("backup directory holds what its remote cannot restore, keeping it",
				"path", b.path, "not_restorable", len(b.dirtyFiles), "session_dirs", len(b.sessionDirs))
			skippedPaths = append(skippedPaths, b.path)
			sessionDirs = append(sessionDirs, b.sessionDirs...)
			continue
		}

		// safe to remove: empty, or a readable repo with nothing outside its history
		slog.Info("removing backup directory", "path", b.path, "size", humanSize(b.size))
		if err := os.RemoveAll(b.path); err != nil {
			slog.Error("failed to remove backup directory", "path", b.path, "error", err)
			skippedPaths = append(skippedPaths, b.path)
			continue
		}
		removed++
	}

	if len(skippedPaths) == 0 {
		return PassedCheck("Backup directories", fmt.Sprintf("removed %d backup(s)", removed))
	}

	var detail strings.Builder
	fmt.Fprintf(&detail, "Kept %d backup(s); what their remote cannot restore (uncommitted, untracked or gitignored files, unpushed commits, stashes) is never auto-deleted:\n", len(skippedPaths))
	for _, p := range skippedPaths {
		fmt.Fprintf(&detail, "  %s\n", p)
	}
	if len(sessionDirs) > 0 {
		fmt.Fprintf(&detail, "Kept %d session recording folder(s); move any you need into the ledger's .sageox/cache/sessions/: %s\n",
			len(sessionDirs), strings.Join(sessionDirs, ", "))
	}
	detail.WriteString("Inspect before removing manually: none of it can be recovered once deleted")

	msg := fmt.Sprintf("skipped %d (not restorable from git)", len(skippedPaths))
	if removed > 0 {
		msg = fmt.Sprintf("removed %d, skipped %d", removed, len(skippedPaths))
	}
	return WarningCheck("Backup directories", msg, detail.String())
}

// dirSize calculates the total size of a directory tree.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors, best-effort size calculation
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// humanSize formats bytes into a human-readable string.
func humanSize(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)

	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%d MB", bytes/mb)
	case bytes >= kb:
		return fmt.Sprintf("%d KB", bytes/kb)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
