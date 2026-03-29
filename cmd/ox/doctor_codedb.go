package main

import (
	"errors"
	"os"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugCodeIndex,
		Name:        "Code index",
		Category:    "Code Search",
		FixLevel:    FixLevelSuggested,
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
		if fix {
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "corrupt index removed, run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "failed to open index", "run 'ox doctor --fix' to remove and rebuild")
	}
	defer db.Close()

	if err := db.Store().CheckIntegrity(); errors.Is(err, store.ErrCorrupt) {
		if fix {
			db.Close()
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "corrupt index removed, run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "index corruption detected", "run 'ox doctor --fix' to remove and rebuild")
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
