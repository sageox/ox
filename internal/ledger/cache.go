package ledger

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
)

// PreserveCache copies the .sageox/cache/ directory from a checkout to a
// backup location. The cache is gitignored and holds derived data that is
// expensive to rebuild, such as codedb indexes. Returns nil if no cache exists
// (nothing to preserve).
//
// The daemon's blue-green reclone preserves ledger and team-context caches
// through this and RestoreCache; read sync restores an adopted cache through
// RestoreCache.
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
