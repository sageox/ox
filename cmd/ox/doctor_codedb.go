package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugCodeIndex,
		Name:        "Code index",
		Category:    "Code Search",
		FixLevel:    FixLevelAuto,
		Description: "Validates CodeDB index integrity (SQLite + Bleve)",
		Run:         checkCodeIndex,
	})
}

func checkCodeIndex(fix bool) checkResult {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return SkippedCheck("Code index", "not in a project", "")
	}
	return checkCodeIndexAtDir(resolveCodeDBDir(projectRoot), fix)
}

// checkCodeIndexAtDir is the testable core of checkCodeIndex.
// Exported for direct use in tests where the data dir is already known.
func checkCodeIndexAtDir(dataDir string, fix bool) checkResult {

	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return PassedCheck("Code index", "no index (run 'ox code index' to create)")
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		// Targeted self-heal: a single bleve sub-index with an empty mapping
		// document is independently recoverable without nuking the whole
		// dataDir (~1GB+ on real repos). Detect via typed MappingCorruptError
		// from store.openOrCreateBleveIndex; rebuild only that sub-index.
		// "comment" repairs surgically; "code"/"diff" fall back to full
		// dataDir wipe (the original behavior) since they cannot be
		// repopulated from SQL alone.
		var mce *store.MappingCorruptError
		if errors.As(err, &mce) {
			if fix {
				rbErr := store.RebuildBleveSubIndex(dataDir, mce.Name)
				if rbErr == nil {
					return PassedCheck("Code index", fmt.Sprintf("%s sub-index rebuilt; run 'ox code index' to repopulate", mce.Name))
				}
				if errors.Is(rbErr, store.ErrFullReindexRequired) {
					_ = os.RemoveAll(dataDir)
					return PassedCheck("Code index", fmt.Sprintf("%s sub-index needs full reindex; dataDir wiped, run 'ox code index'", mce.Name))
				}
				return FailedCheck("Code index", fmt.Sprintf("rebuild %s sub-index failed: %v", mce.Name, rbErr), "run 'ox code index --full' to rebuild from scratch")
			}
			return FailedCheck("Code index", fmt.Sprintf("%s sub-index is structurally corrupt", mce.Name), "run 'ox doctor --fix' to rebuild only the affected sub-index")
		}
		if fix {
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "corrupt index removed, run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "failed to open index", "run 'ox doctor' to remove and rebuild")
	}
	defer db.Close()

	if err := db.Store().CheckIntegrity(); errors.Is(err, store.ErrCorrupt) {
		if fix {
			db.Close()
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "corrupt index removed, run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "index corruption detected", "run 'ox doctor' to remove and rebuild")
	}

	// Detect "empty index": schema exists but no data was ever written.
	// This happens when indexing was interrupted (daemon crash, context cancel)
	// after the DB was created but before any commits were processed.
	var commitCount, repoCount int
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&commitCount)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM repos").Scan(&repoCount)
	if commitCount == 0 && repoCount == 0 {
		if fix {
			db.Close()
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "empty index removed — run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "index exists but is empty (indexing was never completed)", "run 'ox code index' or 'ox doctor --fix' to rebuild")
	}

	return PassedCheck("Code index", "healthy")
}
