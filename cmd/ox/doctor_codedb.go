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

	dataDir := resolveCodeDBDir(projectRoot)

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

	return PassedCheck("Code index", "healthy")
}
