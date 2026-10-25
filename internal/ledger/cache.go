package ledger

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
)

// PlanPushPendingDir and PlanReviewPendingDir, relative to a Ledger clone's
// root, hold the CLI's retries for plan work not yet known to be on the remote
// (cmd/ox/plan_push_pending.go). Being under .git/, they are never tracked, so
// a fresh clone does not bring them back: the daemon's GC reclone carries them
// from the clone it replaces.
const (
	PlanPushPendingDir   = ".git/ox-plan-push-pending"
	PlanReviewPendingDir = ".git/ox-plan-review-pending"
)

// PreserveCache copies the .sageox/cache/ directory from a checkout to a
// backup location. The cache is gitignored local state that a fresh clone does
// not bring back, such as codedb indexes and session recordings. Returns nil
// if no cache exists (nothing to preserve).
func PreserveCache(srcRepo, cacheBackupDir string) error {
	cacheDir := filepath.Join(srcRepo, ".sageox", "cache")
	if _, err := os.Stat(cacheDir); err != nil {
		return nil // no cache to preserve
	}
	return fileutil.CopyDir(cacheDir, cacheBackupDir)
}

// RestoreCache copies a preserved cache into dstRepo's .sageox/cache/ directory.
// Only a backup that does not exist counts as nothing to restore: callers
// delete the backup once a restore succeeds.
func RestoreCache(cacheBackupDir, dstRepo string) error {
	if _, err := os.Stat(cacheBackupDir); os.IsNotExist(err) {
		return nil // no backup to restore
	} else if err != nil {
		return err
	}
	dstCache := filepath.Join(dstRepo, ".sageox", "cache")
	if err := os.MkdirAll(filepath.Dir(dstCache), 0755); err != nil {
		return fmt.Errorf("create .sageox dir: %w", err)
	}
	return fileutil.CopyDir(cacheBackupDir, dstCache)
}
