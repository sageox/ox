package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

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
		// store.Open self-heals MappingCorruptError transparently (nuke +
		// recreate + marker) — so it is rare to reach this branch. Keep it as
		// defense-in-depth in case Open's self-heal itself fails (disk full,
		// EACCES on the bleve dir, panic recovery surfacing a different error).
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

	// Self-heal markers indicate Open already nuked + recreated a bleve
	// sub-index in response to mapping corruption. The daemon's next indexing
	// pass will force a full reindex (see internal/daemon/codedb.go); until
	// then, search returns empty for the affected sub-index. Surface the
	// state so the user understands why their search results are degraded
	// and knows the explicit force-now command.
	if healing := store.NeedsReindexMarkers(dataDir); len(healing) > 0 {
		if fix {
			// In --fix mode, trigger the rebuild synchronously by wiping
			// dataDir and letting the next daemon pass repopulate. This
			// matches the existing "code/diff need full reindex" doctor path.
			db.Close()
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index",
				fmt.Sprintf("auto-repair pending for %s; dataDir wiped, run 'ox code index' to rebuild now",
					strings.Join(healing, ", ")))
		}
		return WarningCheck("Code index",
			fmt.Sprintf("auto-repair in progress for sub-index(es) %s — daemon will rebuild on next pass",
				strings.Join(healing, ", ")),
			"force immediate rebuild: 'ox code index --full'")
	}

	return PassedCheck("Code index", "healthy")
}
