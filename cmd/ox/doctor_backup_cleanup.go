package main

import (
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
		Description: "Detects and cleans up stale .bak.* directories (older than 7 days) left by failed clone operations",
		Run: func(fix bool) checkResult {
			return checkBackupDirectories(fix)
		},
	})
}

// backupDirInfo holds metadata about a discovered backup directory.
type backupDirInfo struct {
	path       string
	timestamp  time.Time
	size       int64
	hasGit     bool
	dirty      bool     // only meaningful when hasGit is true
	dirtyFiles []string // porcelain output lines when dirty
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
		if b.dirty {
			status = fmt.Sprintf("%d uncommitted change(s)", len(b.dirtyFiles))
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

	// check if it contains a .git directory
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		info.hasGit = true
		// check if the working tree is dirty
		cmd := exec.Command("git", "-C", path, "status", "--porcelain")
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			info.dirty = true
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			info.dirtyFiles = lines
		}
	}

	return info
}

// fixBackupDirectories removes clean backup directories and warns about dirty ones.
func fixBackupDirectories(backups []backupDirInfo) checkResult {
	var removed, skipped int
	var skippedPaths []string

	for _, b := range backups {
		if b.dirty {
			// has uncommitted changes - do not remove
			slog.Warn("backup directory has uncommitted changes, skipping removal",
				"path", b.path, "dirty_files", len(b.dirtyFiles))
			skipped++
			skippedPaths = append(skippedPaths, b.path)
			continue
		}

		// safe to remove: either no .git (not a repo) or clean working tree
		slog.Info("removing backup directory", "path", b.path, "size", humanSize(b.size))
		if err := os.RemoveAll(b.path); err != nil {
			slog.Error("failed to remove backup directory", "path", b.path, "error", err)
			skipped++
			skippedPaths = append(skippedPaths, b.path)
			continue
		}
		removed++
	}

	if skipped > 0 && removed > 0 {
		detail := fmt.Sprintf("Skipped %d backup(s) with uncommitted changes:\n", skipped)
		for _, p := range skippedPaths {
			detail += fmt.Sprintf("  %s\n", p)
		}
		detail += "Remove manually or inspect changes first"
		return WarningCheck("Backup directories",
			fmt.Sprintf("removed %d, skipped %d", removed, skipped), detail)
	}

	if skipped > 0 {
		detail := fmt.Sprintf("All %d backup(s) have uncommitted changes:\n", skipped)
		for _, p := range skippedPaths {
			detail += fmt.Sprintf("  %s\n", p)
		}
		detail += "Remove manually or inspect changes first"
		return WarningCheck("Backup directories",
			fmt.Sprintf("skipped %d (uncommitted changes)", skipped), detail)
	}

	return PassedCheck("Backup directories", fmt.Sprintf("removed %d backup(s)", removed))
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
